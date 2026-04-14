// Package ws implements the Local-mode viewer transport per ADR-0007
// §"Protocol Choice for Viewer -> Gateway on LAN".
//
// Why WebSocket and not gRPC-Web for Local mode:
//
//   - Browsers require a *secure context* (HTTPS) for many modern
//     APIs. `localhost` is exempt; LAN IPs (e.g., `http://192.168.1.42`)
//     are not.
//   - gRPC-Web typically runs over HTTP/2 + TLS. Plain HTTP/1.1 is
//     fragile and browser-specific.
//   - Real TLS certs on an arbitrary LAN IP are impractical for the
//     "boss scans QR on phone" use case (no DHCP-aware cert minting,
//     `mkcert` requires user CA install).
//
// Plain `ws://` WebSocket is allowed from non-secure-context pages
// on all major browsers, so Local mode terminates viewer transport
// here. The wire payload is binary-marshaled `aegis.v1.ViewerEvent`,
// the same proto that the Cloud-mode gRPC-Web path carries — so the
// frontend's `WebSocketTranscriptStreamProvider` and
// `GrpcWebTranscriptStreamProvider` decode the same Protobuf and the
// UI is transport-agnostic.
//
// The endpoint is `/ws/viewer?session_id=<sid>&token=<jwt>`. The
// frontend (frontend_web/src/providers/TranscriptStreamProvider/
// WebSocketTranscriptStreamProvider.ts) constructs exactly this URL.
package ws

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	cwebsocket "github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
	"github.com/BinHsu/aegis-core/gateway_go/internal/token"
)

// Subprotocol is the value advertised in `Sec-WebSocket-Protocol`
// per ADR-0007 §"Open Implementation Questions": gives us a clean
// upgrade path if v2 ever breaks the framing.
const Subprotocol = "aegis.v1.transcript"

// Config wires the handler to its collaborators. All fields are
// required except Logger.
type Config struct {
	Registry *session.Registry
	Issuer   *token.Issuer

	// SubscriberBuffer sets the per-subscriber fan-out channel size.
	// Defaults to 0 (which session.Subscribe interprets as the
	// ADR-0004 default of 32). A larger value tolerates short
	// receiver stalls; a smaller value reflects a "drop fast" policy.
	SubscriberBuffer int

	// HandshakeTimeout bounds how long the WebSocket upgrade
	// handshake may take. Defaults to 5s.
	HandshakeTimeout time.Duration

	// Logger receives connection-lifecycle events (accept errors,
	// id allocation failures, per-event send errors). When nil the
	// handler stays silent — appropriate for tests where log noise
	// would mask real assertion output.
	Logger *slog.Logger
}

// Handler returns an http.HandlerFunc that upgrades the request to a
// WebSocket and forwards every aegis.v1.ViewerEvent the session
// broadcasts to the peer until either side closes.
//
// Authentication: the JWT join token is mandatory and is bound to
// the session_id in the URL — Verify rejects cross-session replay
// per token.Verify contract.
//
// Closure semantics:
//   - 1000 (Normal) when the session ends and we've sent the final
//     MEETING_STATE_ENDED frame.
//   - 1008 (PolicyViolation) on bad token / unknown session.
//   - 1011 (InternalError) on marshal failure or send error.
//
// CORS: this is a Local-mode endpoint reached via the QR code URL
// from the same host:port as the host UI; we do not set
// Access-Control-Allow-Origin (no cross-origin viewer model exists
// in Local mode). The InsecureSkipVerify option on Accept is set
// because the Origin check is meaningless on the LAN — anyone
// reaching the port already passed the trust boundary documented
// in ADR-0007 §"Constraints and Caveats" (LAN trust assumed).
func Handler(cfg Config) http.HandlerFunc {
	if cfg.Registry == nil {
		panic("ws.Handler: Registry is required")
	}
	if cfg.Issuer == nil {
		panic("ws.Handler: Issuer is required")
	}

	return func(w http.ResponseWriter, r *http.Request) {
		serve(cfg, w, r)
	}
}

func serve(cfg Config, w http.ResponseWriter, r *http.Request) {
	// Nil-safe log helper — when Config.Logger is nil (tests), emit
	// nothing. Keeps every call site free of a nil check. The wrapper
	// closes over cfg.Logger by value, not by reference, so any later
	// mutation of cfg is a no-op from the handler's perspective.
	log := func(level slog.Level, msg string, args ...any) {
		if cfg.Logger != nil {
			cfg.Logger.Log(r.Context(), level, msg, args...)
		}
	}

	q := r.URL.Query()
	sessionID := q.Get("session_id")
	rawToken := q.Get("token")
	if sessionID == "" || rawToken == "" {
		http.Error(w, "session_id and token are required", http.StatusBadRequest)
		return
	}

	if _, err := cfg.Issuer.Verify(rawToken, sessionID); err != nil {
		// Use plain HTTP 401 here (not 1008) — we have not yet
		// upgraded. Browsers surface the 401 to the page so the
		// frontend can render an error.
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	sess, err := cfg.Registry.Get(sessionID)
	if err != nil {
		if errors.Is(err, session.ErrSessionNotFound) {
			http.Error(w, "session not found", http.StatusNotFound)
			return
		}
		http.Error(w, "registry error", http.StatusInternalServerError)
		return
	}
	if sess.Ended() {
		http.Error(w, "session already ended", http.StatusGone)
		return
	}

	conn, err := cwebsocket.Accept(w, r, &cwebsocket.AcceptOptions{
		Subprotocols:       []string{Subprotocol},
		InsecureSkipVerify: true,
	})
	if err != nil {
		log(slog.LevelWarn, "ws accept", "err", err)
		return
	}
	// Defer a generic InternalError close; happy paths overwrite it
	// with the appropriate code before returning.
	defer conn.Close(cwebsocket.StatusInternalError, "handler exit")

	// Tag the viewer in the session for accounting (matches gRPC
	// JoinAsViewer behavior).
	connID, err := session.NewID()
	if err != nil {
		log(slog.LevelError, "ws new conn id", "err", err, "session_id", sessionID)
		conn.Close(cwebsocket.StatusInternalError, "id alloc failed")
		return
	}
	sess.AddViewer(connID)
	defer sess.RemoveViewer(connID)

	events, unsubscribe := sess.Subscribe(cfg.SubscriberBuffer)
	defer unsubscribe()

	// Bind the connection's lifetime to the request context so a
	// shutdown of the HTTP server propagates here. The frontend's
	// reconnect logic handles the resulting close gracefully.
	ctx := r.Context()

	var seq uint64 = 1
	if err := sendEvent(ctx, conn, &aegisv1.ViewerEvent{
		Sequence: seq,
		Payload: &aegisv1.ViewerEvent_StateChange{
			StateChange: &aegisv1.MeetingStateChange{
				State:  aegisv1.MeetingState_MEETING_STATE_ACTIVE,
				Reason: "joined",
			},
		},
	}); err != nil {
		log(slog.LevelWarn, "ws send kickoff", "err", err, "session_id", sessionID, "conn_id", connID)
		return
	}

	// Loop: forward broadcast events until the session ends or the
	// peer closes. Renumber per-subscription so a slow-consumer drop
	// shows up as a sequence gap, matching the proto comment.
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				seq++
				_ = sendEvent(ctx, conn, &aegisv1.ViewerEvent{
					Sequence: seq,
					Payload: &aegisv1.ViewerEvent_StateChange{
						StateChange: &aegisv1.MeetingStateChange{
							State:  aegisv1.MeetingState_MEETING_STATE_ENDED,
							Reason: "session terminated",
						},
					},
				})
				conn.Close(cwebsocket.StatusNormalClosure, "session ended")
				return
			}
			seq++
			out := &aegisv1.ViewerEvent{
				Sequence:  seq,
				EmittedAt: ev.GetEmittedAt(),
				Payload:   ev.Payload,
			}
			if err := sendEvent(ctx, conn, out); err != nil {
				log(slog.LevelWarn, "ws send event", "err", err, "session_id", sessionID, "conn_id", connID, "seq", seq)
				return
			}
		case <-ctx.Done():
			conn.Close(cwebsocket.StatusNormalClosure, "client gone")
			return
		}
	}
}

// sendEvent marshals ev and writes it as a single binary WebSocket
// frame. We use a per-write context derived from the surrounding
// request so a stuck send eventually unblocks under shutdown.
func sendEvent(ctx context.Context, conn *cwebsocket.Conn, ev *aegisv1.ViewerEvent) error {
	wireBytes, err := proto.Marshal(ev)
	if err != nil {
		return err
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return conn.Write(writeCtx, cwebsocket.MessageBinary, wireBytes)
}
