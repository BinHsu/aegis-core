package webrtc

import (
	"context"
	"testing"
	"time"

	pwebrtc "github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

func TestNegotiateInvalidInputs(t *testing.T) {
	n := New()
	ctx := context.Background()

	// Empty sessionID must be rejected before a PeerConnection is created.
	if _, err := n.Negotiate(ctx, "", "v=0\r\n"); err == nil {
		t.Fatal("empty sessionID: expected error, got nil")
	}

	// Empty offerSDP must be rejected immediately.
	if _, err := n.Negotiate(ctx, "sess-1", ""); err == nil {
		t.Fatal("empty offerSDP: expected error, got nil")
	}

	// Malformed SDP causes SetRemoteDescription to fail. The session
	// should be cleaned up (PacketsReceived returns 0 afterward).
	if _, err := n.Negotiate(ctx, "sess-bad-sdp", "not-sdp"); err == nil {
		t.Fatal("bad SDP: expected error, got nil")
	}
	if n.PacketsReceived("sess-bad-sdp") != 0 {
		t.Fatal("PacketsReceived after bad SDP should be 0 (cleanup failed)")
	}
}

func TestNegotiateCloseIsIdempotent(t *testing.T) {
	n := New()

	// Close on an unknown session must be a no-op (no error, no panic).
	if err := n.Close("ghost-session"); err != nil {
		t.Fatalf("Close unknown session: %v", err)
	}
	// Repeated close also no-op.
	if err := n.Close("ghost-session"); err != nil {
		t.Fatalf("Close unknown session (2nd): %v", err)
	}
	if n.PacketsReceived("ghost-session") != 0 {
		t.Fatal("PacketsReceived for unknown session should be 0")
	}
}

// TestNegotiateLoopbackICEAndRTP establishes a real Pion-to-Pion ICE
// connection on loopback, then sends five Opus frames and confirms the
// server-side OnTrack goroutine has incremented the RTP counter.
//
// This test requires the host to have a loopback interface with host
// ICE candidates (127.0.0.1 / ::1). It is the Phase 2 A3 proof-of-life
// for the full WebRTC receive path.
func TestNegotiateLoopbackICEAndRTP(t *testing.T) {
	// Overall test deadline; ICE on loopback typically connects in <1s.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	const sessionID = "loopback-sess"

	n := New()
	defer func() { _ = n.Close(sessionID) }()

	// ------------------------------------------------------------------
	// 1. Browser-side PeerConnection with an Opus audio track.
	// ------------------------------------------------------------------
	browserPC, err := pwebrtc.NewPeerConnection(pwebrtc.Configuration{})
	if err != nil {
		t.Fatalf("browser NewPeerConnection: %v", err)
	}
	defer func() { _ = browserPC.Close() }()

	track, err := pwebrtc.NewTrackLocalStaticSample(
		pwebrtc.RTPCodecCapability{MimeType: pwebrtc.MimeTypeOpus},
		"audio-1", "stream-1",
	)
	if err != nil {
		t.Fatalf("NewTrackLocalStaticSample: %v", err)
	}
	if _, err := browserPC.AddTrack(track); err != nil {
		t.Fatalf("AddTrack: %v", err)
	}

	// ------------------------------------------------------------------
	// 2. Browser creates a non-trickle offer (gather all candidates first).
	// ------------------------------------------------------------------
	offer, err := browserPC.CreateOffer(nil)
	if err != nil {
		t.Fatalf("CreateOffer: %v", err)
	}
	gatherBrowser := pwebrtc.GatheringCompletePromise(browserPC)
	if err := browserPC.SetLocalDescription(offer); err != nil {
		t.Fatalf("browser SetLocalDescription: %v", err)
	}
	select {
	case <-gatherBrowser:
	case <-ctx.Done():
		t.Fatal("browser ICE gathering timed out")
	}

	// ------------------------------------------------------------------
	// 3. Server negotiates: SDP offer → answer (blocks until ICE gathered).
	// ------------------------------------------------------------------
	negotiateCtx, nCancel := context.WithTimeout(ctx, 15*time.Second)
	defer nCancel()

	answerSDP, err := n.Negotiate(negotiateCtx, sessionID, browserPC.LocalDescription().SDP)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if answerSDP == "" {
		t.Fatal("Negotiate returned empty answerSDP")
	}

	// ------------------------------------------------------------------
	// 4. Register ICE state handler BEFORE SetRemoteDescription to avoid
	//    missing the Connected transition if ICE completes quickly.
	// ------------------------------------------------------------------
	connCh := make(chan struct{}, 1)
	browserPC.OnICEConnectionStateChange(func(s pwebrtc.ICEConnectionState) {
		if s == pwebrtc.ICEConnectionStateConnected || s == pwebrtc.ICEConnectionStateCompleted {
			select {
			case connCh <- struct{}{}:
			default:
			}
		}
	})

	if err := browserPC.SetRemoteDescription(pwebrtc.SessionDescription{
		Type: pwebrtc.SDPTypeAnswer,
		SDP:  answerSDP,
	}); err != nil {
		t.Fatalf("browser SetRemoteDescription: %v", err)
	}

	select {
	case <-connCh:
		// ICE connected.
	case <-ctx.Done():
		t.Fatalf("ICE did not connect: browser ICEConnectionState=%v",
			browserPC.ICEConnectionState())
	}

	// ------------------------------------------------------------------
	// 5. Send 5 Opus frames. The server OnTrack goroutine must count them.
	// ------------------------------------------------------------------
	// 80 bytes of arbitrary payload — the counter only checks RTP header
	// arrival, not codec validity.
	silentOpus := make([]byte, 80)
	for i := 0; i < 5; i++ {
		if err := track.WriteSample(media.Sample{
			Data:     silentOpus,
			Duration: 20 * time.Millisecond,
		}); err != nil {
			t.Fatalf("WriteSample[%d]: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// ------------------------------------------------------------------
	// 6. Poll until the server-side RTP counter is non-zero.
	// ------------------------------------------------------------------
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n.PacketsReceived(sessionID) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("PacketsReceived stayed 0 after 3s; browser ICEState=%v",
				browserPC.ICEConnectionState())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
