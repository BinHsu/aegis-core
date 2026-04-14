// Package main is the entrypoint for the Aegis Core Go Gateway.
//
// As of Phase 2 A4, this binary runs three concurrent servers:
//
//   - HTTP on :8080 — /healthz probe (HTTP/1.1, no TLS) and the
//     /ws/viewer Local-mode WebSocket transport for viewer events
//     (ADR-0007).
//   - gRPC on :9090 — aegis.v1.Gateway service for Cloud-mode
//     viewers (gRPC-Web in front of this) plus the host's
//     CreateMeeting / EndMeeting / NegotiateWebRTC RPCs.
//
// All servers share one Engine client (the C++ engine on :50051), one
// Session registry, one JWT issuer, and one WebRTC Negotiator — so a
// token issued by CreateMeeting is accepted by JoinAsViewer (gRPC) and
// /ws/viewer (WebSocket) interchangeably, and ICE state is tied to the
// same session lifetime.
//
// A4 wires the full audio pipeline. The factory closure injected into
// internal/grpc as AudioPipelineStart captures the process ctx, the
// Negotiator, and the Engine client so internal/grpc stays free of
// pion and pipeline imports. On NegotiateWebRTC success, the closure
// pulls RTP payloads off negotiator.AudioChan(sid), decodes them via
// pion/opus, and sends them as PcmChunks to the engine's
// StreamTranscribe stream; transcript egress is fanned out via
// session.Broadcast to viewers on both transports.
//
// The binary is built via Bazel:
//
//	./tools/bazelisk/bazelisk build //gateway_go/cmd/gateway:gateway
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/auth"
	gatewaygrpc "github.com/BinHsu/aegis-core/gateway_go/internal/grpc"
	"github.com/BinHsu/aegis-core/gateway_go/internal/logging"
	"github.com/BinHsu/aegis-core/gateway_go/internal/pipeline"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
	"github.com/BinHsu/aegis-core/gateway_go/internal/token"
	aegiswebrtc "github.com/BinHsu/aegis-core/gateway_go/internal/webrtc"
	"github.com/BinHsu/aegis-core/gateway_go/internal/ws"
)

const (
	// Empty host in ":8080" / ":9090" is Go's "bind on all interfaces"
	// spelling (net.Listen unpacks it to 0.0.0.0:8080). This is the
	// Local-mode LAN-viewer requirement per ADR-0007 — explicit here
	// because a maintainer might otherwise "narrow" it to localhost
	// thinking it's a security improvement. It is NOT: the LAN QR-code
	// viewer flow depends on other devices on the LAN being able to
	// reach the Gateway. Cloud mode sits behind an ingress that does
	// the narrowing at the network layer (ADR-0006 / ARCH §5).
	defaultListenAddr = ":8080"
	defaultGRPCAddr   = ":9090"
	defaultEngineAddr = "localhost:50051"
	version           = "0.1.0-phase2-a4"
	engineRPCTimeout  = 2 * time.Second

	// defaultDrainTimeout is the bound on shutdown. Short enough for a
	// Local-mode developer's Ctrl-C to feel instant; long enough for
	// in-flight viewer streams to drain via GracefulStop. Cloud mode
	// sets AEGIS_GATEWAY_DRAIN_TIMEOUT=14400s to match the
	// terminationGracePeriodSeconds in ADR-0006.
	defaultDrainTimeout = 30 * time.Second

	// keepaliveTime / keepaliveTimeout follow ADR-0006 exactly: a viewer
	// that silently vanishes (laptop lid closed, network cable pulled)
	// is detected within Time + Timeout = 40 s and its fan-out channel
	// is reclaimed. Without this, Go gRPC's 2-hour default keepalive
	// would hold fan-out resources for disconnected viewers.
	keepaliveTime           = 30 * time.Second
	keepaliveTimeout        = 10 * time.Second
	keepaliveMinTime        = 10 * time.Second
)

func main() {
	// Structured logger from env (AEGIS_LOG_FORMAT, AEGIS_LOG_LEVEL).
	// SetDefault makes slog.Info/Warn/Error calls throughout the tree
	// honor the configuration, which matters once internal/pipeline
	// and the factory closure below use the package-level slog logger.
	logger := logging.SetDefault().With("pkg", "gateway")

	// die emits a structured Error record and terminates. We use this
	// rather than log.Fatalf so startup failures appear in the same
	// format as runtime failures — critical for ops tooling that greps
	// for `"level":"ERROR"` to trigger on-call.
	die := func(msg string, args ...any) {
		logger.Error(msg, args...)
		os.Exit(1)
	}

	listenAddr := defaultListenAddr
	if env := os.Getenv("AEGIS_GATEWAY_ADDR"); env != "" {
		listenAddr = env
	}
	grpcAddr := defaultGRPCAddr
	if env := os.Getenv("AEGIS_GATEWAY_GRPC_ADDR"); env != "" {
		grpcAddr = env
	}
	engineAddr := defaultEngineAddr
	if env := os.Getenv("AEGIS_ENGINE_ADDR"); env != "" {
		engineAddr = env
	}

	// Dial the engine. grpc.NewClient (the 1.64+ preferred form) does
	// NOT block on a live TCP connection — the RPC at call time drives
	// the dial. Keepalive settings match ADR-0006 so a dead engine
	// (pod crash, cable pull) is detected within 40 s on any open
	// StreamTranscribe bidi stream and the pipeline errors out instead
	// of hanging forever.
	conn, err := grpc.NewClient(
		engineAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                keepaliveTime,
			Timeout:             keepaliveTimeout,
			PermitWithoutStream: true,
		}),
	)
	if err != nil {
		die("grpc.NewClient", "engine_addr", engineAddr, "err", err)
	}
	defer func() { _ = conn.Close() }()
	engine := aegisv1.NewEngineClient(conn)

	// Session registry + JWT issuer. Both are process-scoped: the
	// registry per ADR-0004 "No Shared State Between Replicas", the
	// issuer's signing key per ADR-0001 "process-scoped random key".
	registry := session.NewRegistry()
	issuer, err := token.NewIssuer()
	if err != nil {
		die("token.NewIssuer", "err", err)
	}

	// Signal-driven process context. Shared between all long-lived
	// subsystems: audio pipelines (so they tear down on SIGTERM), the
	// HTTP/gRPC servers' shutdown path, and anything we add later.
	// Creating it up here — rather than right before the <-ctx.Done()
	// wait — lets the pipeline factory closure capture a process-scoped
	// ctx that outlives the per-RPC NegotiateWebRTC context.
	processCtx, stopSignals := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	negotiator := aegiswebrtc.New()

	// Factory closure for the audio pipeline. Called once per
	// successful NegotiateWebRTC. Returns a stop function the gRPC
	// service calls on EndMeeting (or on re-negotiation). Captures
	// processCtx so the pipeline outlives the NegotiateWebRTC RPC
	// context (which cancels as soon as the RPC returns).
	startAudioPipeline := func(sess *session.Session, sessionID string) (func(), error) {
		// Per-pipeline context derived from the process context —
		// cancelled by stop() on EndMeeting, or on process shutdown
		// via processCtx propagation.
		pipeCtx, pipeCancel := context.WithCancel(processCtx)

		p, pErr := pipeline.New(pipeCtx, engine, sess, sessionID)
		if pErr != nil {
			pipeCancel()
			return nil, pErr
		}

		audioCh := negotiator.AudioChan(sessionID)
		iceCh := negotiator.ICEChan(sessionID)

		go func() {
			for {
				select {
				case payload, ok := <-audioCh:
					if !ok {
						// Channel is never closed by the Negotiator
						// (see AudioChan docs); this case exists for
						// symmetry if that contract ever changes.
						return
					}
					if err := p.WriteRTPPayload(payload); err != nil {
						// Single-frame decode/send errors are logged
						// but not fatal — one corrupt Opus packet
						// shouldn't kill an otherwise-healthy session.
						logger.Warn("pipeline write", "session_id", sessionID, "err", err)
					}
				case state, ok := <-iceCh:
					if !ok {
						return
					}
					switch state {
					case aegiswebrtc.ICEStateDisconnected:
						_ = p.SendControl(aegisv1.ControlKind_CONTROL_KIND_PAUSE)
					case aegiswebrtc.ICEStateConnected:
						_ = p.SendControl(aegisv1.ControlKind_CONTROL_KIND_RESUME)
					case aegiswebrtc.ICEStateFailed:
						// Terminal — emit END_STREAM and exit; the
						// engine will flush and close the stream.
						_ = p.SendControl(aegisv1.ControlKind_CONTROL_KIND_END_STREAM)
						return
					}
				case <-pipeCtx.Done():
					return
				}
			}
		}()

		stop := func() {
			pipeCancel() // release the pump goroutine
			p.Close()    // END_STREAM + CloseSend + drain egress
		}
		return stop, nil
	}

	gatewaySvc, err := gatewaygrpc.New(gatewaygrpc.Config{
		Registry:           registry,
		Issuer:             issuer,
		Engine:             engine,
		WebRTCNegotiator:   negotiator,
		AudioPipelineStart: startAudioPipeline,
		EngineProbeTimeout: engineRPCTimeout,
	})
	if err != nil {
		die("gatewaygrpc.New", "err", err)
	}

	// Auth provider: Local mode uses NoOp (synthetic "local" Principal
	// on every RPC); Cloud mode would swap in a StaticJWTProvider or
	// (future) a real Cognito client — see internal/auth for the port
	// definition and Phase 2 "Known Gaps" in ROADMAP.md for the
	// Cognito-integration scope that is descoped from this phase.
	authProvider := auth.NoOpProvider{}

	// gRPC server for aegis.v1.Gateway. Interceptors fire in registration
	// order: auth first (attaches Principal to ctx); future additions
	// (request logging, telemetry, rate limit) slot in after it.
	// Keepalive params follow ADR-0006: a viewer whose stream is idle
	// for `keepaliveTime` triggers a PING; no PONG within
	// `keepaliveTimeout` tears down the connection and reclaims the
	// fan-out channel. EnforcementPolicy.MinTime caps how often clients
	// may send their own pings — without it, a misbehaving client could
	// trip gRPC's "too many pings" disconnect.
	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(auth.UnaryInterceptor(authProvider)),
		grpc.StreamInterceptor(auth.StreamInterceptor(authProvider)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    keepaliveTime,
			Timeout: keepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveMinTime,
			PermitWithoutStream: true,
		}),
	)
	aegisv1.RegisterGatewayServer(grpcSrv, gatewaySvc)

	// HTTP server for /healthz, the Local-mode /ws/viewer transport
	// (ADR-0007), AND the Cloud-mode gRPC-Web bridge. The WebSocket
	// endpoint shares the registry + issuer with the gRPC Gateway so
	// a single token works on either transport.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", makeHealthHandler(engine, engineAddr, registry))
	mux.HandleFunc("/ws/viewer", ws.Handler(ws.Config{
		Registry: registry,
		Issuer:   issuer,
		Logger:   logger.With("pkg", "ws"),
	}))

	// gRPC-Web wrapper: same grpcSrv, spoken over HTTP/1.1 with
	// grpc-web framing so browsers (which cannot speak native HTTP/2
	// gRPC with trailers) can reach CreateMeeting / JoinAsViewer. The
	// origin allowlist is permissive here because Local mode runs on
	// the host's LAN; Cloud mode will replace this closure with an
	// explicit allowlist tied to the Cognito-issued frontend origin
	// (tracked in Phase 2 Known Gaps — ROADMAP.md).
	wrappedGrpc := grpcweb.WrapServer(
		grpcSrv,
		grpcweb.WithOriginFunc(func(string) bool { return true }),
		grpcweb.WithAllowNonRootResource(true),
	)

	httpSrv := &http.Server{
		Addr: listenAddr,
		// Route order:
		//   1. grpc-web (content-type / Accept sniff) → wrapped server
		//   2. everything else → existing mux (/healthz, /ws/viewer)
		// The sniff-first pattern means grpc-web requests don't need a
		// dedicated URL prefix — gRPC service methods hit paths like
		// /aegis.v1.Gateway/CreateMeeting which don't collide with our
		// native HTTP handlers. IsGrpcWebRequest + the CORS preflight
		// check cover both simple and preflighted browser calls.
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wrappedGrpc.IsGrpcWebRequest(r) || wrappedGrpc.IsAcceptableGrpcCorsRequest(r) {
				wrappedGrpc.ServeHTTP(w, r)
				return
			}
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		die("gRPC listen", "addr", grpcAddr, "err", err)
	}

	// Signal handling is rooted at processCtx (declared above). Pipelines
	// observe processCtx cancellation through their pipeCtx parent; this
	// goroutine still waits on <-processCtx.Done() for the shutdown dance.
	// ADR-0006's terminationGracePeriodSeconds=14400 drain hook plugs
	// in here in a later phase once we have live sessions to drain.

	go func() {
		logger.Info("HTTP server listening",
			"addr", listenAddr,
			"endpoints", "/healthz,/ws/viewer,grpc-web",
			"engine_addr", engineAddr,
			"version", version,
		)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			die("HTTP ListenAndServe", "err", err)
		}
	}()

	go func() {
		logger.Info("gRPC server listening", "addr", grpcAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			die("gRPC Serve", "err", err)
		}
	}()

	<-processCtx.Done()
	logger.Info("shutdown signal received; draining")

	drainTimeout := defaultDrainTimeout
	if env := os.Getenv("AEGIS_GATEWAY_DRAIN_TIMEOUT"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			drainTimeout = d
		} else {
			logger.Warn("invalid AEGIS_GATEWAY_DRAIN_TIMEOUT; using default",
				"given", env, "default", defaultDrainTimeout)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()

	// Shut down HTTP first (fast) — the healthz probe and /ws/viewer
	// paths don't participate in long-running sessions; their shutdown
	// is bounded by shutdownCtx.
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP shutdown error", "err", err)
	}

	// gRPC GracefulStop waits for in-flight streams to finish, which
	// for a long-lived viewer can mean "until the client disconnects".
	// Race it against shutdownCtx so an impolite viewer can't block
	// shutdown beyond the drain deadline — the forced Stop() fallback
	// is how ADR-0006's 14400 s terminationGracePeriodSeconds stays a
	// hard upper bound and not a lower bound.
	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		logger.Warn("drain deadline exceeded; forcing Stop", "drain_timeout", drainTimeout)
		grpcSrv.Stop()
		<-stopped
	}
	logger.Info("bye")
}

// gatewayHealth is the JSON shape the /healthz endpoint emits.
// Kept intentionally small and stable so operators and uptime
// monitoring can consume it without a proto dependency.
type gatewayHealth struct {
	Ready    bool         `json:"ready"`
	Version  string       `json:"version"`
	Sessions int          `json:"active_sessions"`
	Engine   engineHealth `json:"engine"`
}

type engineHealth struct {
	Reachable bool   `json:"reachable"`
	Addr      string `json:"addr"`
	Ready     bool   `json:"ready,omitempty"`
	Model     string `json:"model,omitempty"`
	Backend   string `json:"backend,omitempty"`
	EngineVer string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

func makeHealthHandler(
	engine aegisv1.EngineClient,
	engineAddr string,
	registry *session.Registry,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), engineRPCTimeout)
		defer cancel()

		result := gatewayHealth{
			Ready:    true,
			Version:  version,
			Sessions: registry.Len(),
			Engine:   engineHealth{Addr: engineAddr},
		}

		resp, err := engine.Health(ctx, &aegisv1.HealthRequest{})
		if err != nil {
			// Gateway itself is healthy even if engine isn't —
			// report 200 with engine.reachable=false so uptime
			// alerts can distinguish the two.
			result.Engine.Error = err.Error()
		} else {
			result.Engine.Reachable = true
			result.Engine.Ready = resp.GetReady()
			if info := resp.GetInfo(); info != nil {
				result.Engine.Model = info.GetModel()
				result.Engine.Backend = info.GetBackend()
				result.Engine.EngineVer = info.GetVersion()
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = writeJSON(w, result)
	}
}

func writeJSON(w http.ResponseWriter, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
