package ws

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	cwebsocket "github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
	"github.com/BinHsu/aegis-core/gateway_go/internal/token"
)

// setupHandler stands up a Registry + Issuer + WebSocket handler
// behind an httptest.Server. Returns the server (caller closes), the
// registry (test injects events), the issuer (test mints tokens),
// and a derived `ws://` base URL.
func setupHandler(t *testing.T) (*httptest.Server, *session.Registry, *token.Issuer, string) {
	t.Helper()
	reg := session.NewRegistry()
	iss, err := token.NewIssuer()
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	srv := httptest.NewServer(Handler(Config{
		Registry: reg,
		Issuer:   iss,
	}))
	wsBase := strings.Replace(srv.URL, "http://", "ws://", 1)
	return srv, reg, iss, wsBase
}

// dialAndExpectKickoff opens the WebSocket and reads the initial
// MEETING_STATE_ACTIVE frame, asserting it has sequence=1. Returns
// the live connection so the caller can proceed.
func dialAndExpectKickoff(
	t *testing.T,
	wsBase string,
	sessionID, raw string,
) *cwebsocket.Conn {
	t.Helper()
	dialCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, _, err := cwebsocket.Dial(dialCtx, wsBase+"/?session_id="+sessionID+"&token="+raw, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer readCancel()
	mt, b, err := conn.Read(readCtx)
	if err != nil {
		conn.Close(cwebsocket.StatusInternalError, "test fail")
		t.Fatalf("Read kickoff: %v", err)
	}
	if mt != cwebsocket.MessageBinary {
		t.Fatalf("kickoff message type: got %v, want Binary", mt)
	}

	ev := &aegisv1.ViewerEvent{}
	if err := proto.Unmarshal(b, ev); err != nil {
		t.Fatalf("Unmarshal kickoff: %v", err)
	}
	if got := ev.GetSequence(); got != 1 {
		t.Fatalf("kickoff sequence: got %d, want 1", got)
	}
	if got := ev.GetStateChange().GetState(); got != aegisv1.MeetingState_MEETING_STATE_ACTIVE {
		t.Fatalf("kickoff state: got %v, want ACTIVE", got)
	}
	return conn
}

func TestWSHandlerForwardsBroadcast(t *testing.T) {
	srv, reg, iss, wsBase := setupHandler(t)
	defer srv.Close()

	sess, err := reg.Create(session.Config{RAGID: "corpus-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := iss.Issue(sess.ID, sess.TokenExpiresAt)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	conn := dialAndExpectKickoff(t, wsBase, sess.ID, raw)
	defer conn.Close(cwebsocket.StatusNormalClosure, "test done")

	// Wait for Subscribe to register on the server side.
	deadline := time.Now().Add(2 * time.Second)
	for sess.SubscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if sess.SubscriberCount() != 1 {
		t.Fatalf("SubscriberCount: got %d, want 1", sess.SubscriberCount())
	}

	// Broadcast a transcript event.
	delivered, dropped := sess.Broadcast(&aegisv1.ViewerEvent{
		Payload: &aegisv1.ViewerEvent_Transcript{
			Transcript: &aegisv1.TranscriptSegment{
				SegmentId:    7,
				SpeakerLabel: "Speaker_0",
				Text:         "ws roundtrip works",
				IsFinal:      true,
			},
		},
	})
	if delivered != 1 || dropped != 0 {
		t.Fatalf("Broadcast: %d/%d, want 1/0", delivered, dropped)
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	mt, b, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("Read broadcast: %v", err)
	}
	if mt != cwebsocket.MessageBinary {
		t.Fatalf("broadcast message type: got %v, want Binary", mt)
	}
	ev := &aegisv1.ViewerEvent{}
	if err := proto.Unmarshal(b, ev); err != nil {
		t.Fatalf("Unmarshal broadcast: %v", err)
	}
	if got := ev.GetSequence(); got != 2 {
		t.Fatalf("broadcast sequence: got %d, want 2", got)
	}
	tx := ev.GetTranscript()
	if tx == nil {
		t.Fatalf("expected transcript payload, got %+v", ev)
	}
	if tx.GetText() != "ws roundtrip works" {
		t.Fatalf("text: got %q", tx.GetText())
	}
}

func TestWSHandlerSendsTerminalEnded(t *testing.T) {
	srv, reg, iss, wsBase := setupHandler(t)
	defer srv.Close()

	sess, err := reg.Create(session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw, err := iss.Issue(sess.ID, sess.TokenExpiresAt)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	conn := dialAndExpectKickoff(t, wsBase, sess.ID, raw)
	defer conn.Close(cwebsocket.StatusInternalError, "test cleanup")

	// Wait for Subscribe to register before deleting.
	deadline := time.Now().Add(time.Second)
	for sess.SubscriberCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if err := reg.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	mt, b, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("Read terminal ENDED: %v", err)
	}
	if mt != cwebsocket.MessageBinary {
		t.Fatalf("terminal frame type: got %v, want Binary", mt)
	}
	ev := &aegisv1.ViewerEvent{}
	if err := proto.Unmarshal(b, ev); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := ev.GetStateChange().GetState(); got != aegisv1.MeetingState_MEETING_STATE_ENDED {
		t.Fatalf("terminal state: got %v, want ENDED", got)
	}

	// Server should now close the connection.
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatalf("expected connection close after ENDED, got more frames")
	}
}

func TestWSHandlerRejectsBadToken(t *testing.T) {
	srv, reg, iss, wsBase := setupHandler(t)
	defer srv.Close()

	sess, err := reg.Create(session.Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Issue token bound to a *different* session id.
	otherSess, err := reg.Create(session.Config{})
	if err != nil {
		t.Fatalf("Create other: %v", err)
	}
	raw, err := iss.Issue(otherSess.ID, otherSess.TokenExpiresAt)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := cwebsocket.Dial(dialCtx, wsBase+"/?session_id="+sess.ID+"&token="+raw, nil)
	if err == nil {
		t.Fatalf("Dial succeeded with cross-session token; should have failed")
	}
	if resp == nil || resp.StatusCode != 401 {
		var got int
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("status code: got %d, want 401", got)
	}
}

func TestWSHandlerRejectsMissingParams(t *testing.T) {
	srv, _, _, wsBase := setupHandler(t)
	defer srv.Close()

	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := cwebsocket.Dial(dialCtx, wsBase+"/", nil)
	if err == nil {
		t.Fatalf("Dial with no params succeeded; should have failed")
	}
	if resp == nil || resp.StatusCode != 400 {
		var got int
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("status code: got %d, want 400", got)
	}
}

func TestWSHandlerRejectsUnknownSession(t *testing.T) {
	srv, _, iss, wsBase := setupHandler(t)
	defer srv.Close()

	// Mint a token bound to a session that was never inserted.
	raw, err := iss.Issue("ghost-session", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, resp, err := cwebsocket.Dial(dialCtx, wsBase+"/?session_id=ghost-session&token="+raw, nil)
	if err == nil {
		t.Fatalf("Dial for ghost session succeeded; should have failed")
	}
	if resp == nil || resp.StatusCode != 404 {
		var got int
		if resp != nil {
			got = resp.StatusCode
		}
		t.Fatalf("status code: got %d, want 404", got)
	}
}
