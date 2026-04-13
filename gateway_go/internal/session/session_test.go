package session

import (
	"testing"
	"time"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
)

// makeStateEvent crafts a minimal ViewerEvent for fan-out tests.
// Production code uses real transcript / hint payloads; for unit
// tests the type of payload doesn't matter — only the routing does.
func makeStateEvent(seq uint64, reason string) *aegisv1.ViewerEvent {
	return &aegisv1.ViewerEvent{
		Sequence: seq,
		Payload: &aegisv1.ViewerEvent_StateChange{
			StateChange: &aegisv1.MeetingStateChange{
				State:  aegisv1.MeetingState_MEETING_STATE_ACTIVE,
				Reason: reason,
			},
		},
	}
}

func TestNewIDIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id, err := NewID()
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id after %d draws: %q", i, id)
		}
		seen[id] = struct{}{}
		// 16 bytes -> base64.RawURLEncoding -> ceil(16 * 4 / 3) = 22.
		if got := len(id); got != 22 {
			t.Fatalf("unexpected id length: got %d, want 22 (id=%q)", got, id)
		}
	}
}

func TestRegistryCreateGetDelete(t *testing.T) {
	fixedNow := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	reg := newRegistryWithClock(func() time.Time { return fixedNow })

	sess, err := reg.Create(Config{
		TenantID:      "tenant-a",
		RAGID:         "corpus-42",
		Title:         "Q2 pricing",
		LanguageHints: []string{"en-US", "zh-TW"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if sess.ID == "" {
		t.Fatalf("expected non-empty session id")
	}
	if got, want := sess.ExpiresAt.Sub(fixedNow), 4*time.Hour; got != want {
		t.Fatalf("default ExpiresAt offset: got %v, want %v", got, want)
	}
	if got, want := sess.TokenExpiresAt.Sub(fixedNow), 4*time.Hour+10*time.Minute; got != want {
		t.Fatalf("default TokenExpiresAt offset: got %v, want %v", got, want)
	}
	if sess.TenantID != "tenant-a" || sess.RAGID != "corpus-42" {
		t.Fatalf("config not round-tripped: %+v", sess)
	}

	got, err := reg.Get(sess.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != sess {
		t.Fatalf("Get returned different pointer")
	}

	if err := reg.Delete(sess.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !sess.Ended() {
		t.Fatalf("session should be marked ended after Delete")
	}
	if _, err := reg.Get(sess.ID); err != ErrSessionNotFound {
		t.Fatalf("Get after Delete: got %v, want ErrSessionNotFound", err)
	}
	if err := reg.Delete(sess.ID); err != ErrSessionNotFound {
		t.Fatalf("second Delete: got %v, want ErrSessionNotFound", err)
	}
}

func TestRegistryCustomLifetime(t *testing.T) {
	fixedNow := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	reg := newRegistryWithClock(func() time.Time { return fixedNow })

	sess, err := reg.Create(Config{
		SessionMaxLifetime: 30 * time.Minute,
		TokenGrace:         1 * time.Minute,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got, want := sess.ExpiresAt.Sub(fixedNow), 30*time.Minute; got != want {
		t.Fatalf("custom ExpiresAt: got %v, want %v", got, want)
	}
	if got, want := sess.TokenExpiresAt.Sub(fixedNow), 31*time.Minute; got != want {
		t.Fatalf("custom TokenExpiresAt: got %v, want %v", got, want)
	}
}

func TestSessionViewerAccounting(t *testing.T) {
	reg := NewRegistry()
	sess, err := reg.Create(Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if got := sess.ViewerCount(); got != 0 {
		t.Fatalf("fresh session: got %d viewers, want 0", got)
	}
	if got := sess.AddViewer("v1"); got != 1 {
		t.Fatalf("AddViewer v1: got count %d, want 1", got)
	}
	if got := sess.AddViewer("v2"); got != 2 {
		t.Fatalf("AddViewer v2: got count %d, want 2", got)
	}
	// Idempotent: re-adding same id keeps the set size the same.
	if got := sess.AddViewer("v1"); got != 2 {
		t.Fatalf("AddViewer v1 again: got count %d, want 2", got)
	}
	if got := sess.RemoveViewer("v1"); got != 1 {
		t.Fatalf("RemoveViewer v1: got count %d, want 1", got)
	}
	// Removing unknown id is a no-op that doesn't throw.
	if got := sess.RemoveViewer("nobody"); got != 1 {
		t.Fatalf("RemoveViewer unknown: got count %d, want 1", got)
	}
}

func TestSessionHostTracking(t *testing.T) {
	sess := &Session{viewers: map[string]struct{}{}}
	if got := sess.HostConnID(); got != "" {
		t.Fatalf("fresh host conn id: got %q, want empty", got)
	}
	sess.SetHostConnID("host-abc")
	if got := sess.HostConnID(); got != "host-abc" {
		t.Fatalf("after SetHostConnID: got %q, want host-abc", got)
	}

	t0 := time.Unix(1, 0)
	sess.TouchHost(t0)
	if got := sess.LastHostPing(); !got.Equal(t0) {
		t.Fatalf("LastHostPing: got %v, want %v", got, t0)
	}
}

func TestSessionBroadcastReachesAllSubscribers(t *testing.T) {
	reg := NewRegistry()
	sess, err := reg.Create(Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	chA, unsubA := sess.Subscribe(8)
	defer unsubA()
	chB, unsubB := sess.Subscribe(8)
	defer unsubB()

	if got := sess.SubscriberCount(); got != 2 {
		t.Fatalf("SubscriberCount: got %d, want 2", got)
	}

	delivered, dropped := sess.Broadcast(makeStateEvent(1, "hello"))
	if delivered != 2 || dropped != 0 {
		t.Fatalf("Broadcast: delivered=%d dropped=%d, want 2/0", delivered, dropped)
	}

	for name, ch := range map[string]<-chan *aegisv1.ViewerEvent{"A": chA, "B": chB} {
		select {
		case ev := <-ch:
			if ev.GetSequence() != 1 {
				t.Errorf("%s: sequence=%d, want 1", name, ev.GetSequence())
			}
			if ev.GetStateChange().GetReason() != "hello" {
				t.Errorf("%s: reason=%q, want hello", name, ev.GetStateChange().GetReason())
			}
		case <-time.After(time.Second):
			t.Errorf("%s: timed out waiting for fan-out event", name)
		}
	}
}

func TestSessionBroadcastDropsOnSlowSubscriber(t *testing.T) {
	reg := NewRegistry()
	sess, err := reg.Create(Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Buffer of 1 — second Broadcast must drop because nobody read.
	_, unsub := sess.Subscribe(1)
	defer unsub()

	if d, dr := sess.Broadcast(makeStateEvent(1, "first")); d != 1 || dr != 0 {
		t.Fatalf("first Broadcast: %d/%d, want 1/0", d, dr)
	}
	if d, dr := sess.Broadcast(makeStateEvent(2, "second")); d != 0 || dr != 1 {
		t.Fatalf("second Broadcast: %d/%d, want 0/1 (slow consumer drop)", d, dr)
	}
}

func TestSessionUnsubscribeIsIdempotent(t *testing.T) {
	reg := NewRegistry()
	sess, err := reg.Create(Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ch, unsub := sess.Subscribe(4)
	if got := sess.SubscriberCount(); got != 1 {
		t.Fatalf("after Subscribe: %d, want 1", got)
	}
	unsub()
	unsub() // second call must not panic on close-of-closed-channel
	if got := sess.SubscriberCount(); got != 0 {
		t.Fatalf("after unsub: %d, want 0", got)
	}
	if _, ok := <-ch; ok {
		t.Fatalf("channel should be closed after unsubscribe")
	}
}

func TestSessionMarkEndedClosesAllSubscribers(t *testing.T) {
	reg := NewRegistry()
	sess, err := reg.Create(Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	chA, unsubA := sess.Subscribe(4)
	defer unsubA()
	chB, unsubB := sess.Subscribe(4)
	defer unsubB()

	sess.MarkEnded()

	for name, ch := range map[string]<-chan *aegisv1.ViewerEvent{"A": chA, "B": chB} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("%s: expected closed channel, got open recv", name)
			}
		case <-time.After(time.Second):
			t.Errorf("%s: channel not closed by MarkEnded", name)
		}
	}

	// Broadcast on ended session is a no-op.
	if d, dr := sess.Broadcast(makeStateEvent(1, "after-end")); d != 0 || dr != 0 {
		t.Fatalf("post-end Broadcast: %d/%d, want 0/0", d, dr)
	}

	// Second MarkEnded must be safe.
	sess.MarkEnded()
}

func TestSubscribeOnEndedSessionReturnsClosedChannel(t *testing.T) {
	reg := NewRegistry()
	sess, err := reg.Create(Config{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	sess.MarkEnded()

	ch, unsub := sess.Subscribe(4)
	defer unsub()

	if _, ok := <-ch; ok {
		t.Fatalf("expected closed channel for Subscribe on ended session")
	}
}

func TestRegistryLen(t *testing.T) {
	reg := NewRegistry()
	for i := 0; i < 3; i++ {
		if _, err := reg.Create(Config{}); err != nil {
			t.Fatalf("Create %d: %v", i, err)
		}
	}
	if got := reg.Len(); got != 3 {
		t.Fatalf("Len: got %d, want 3", got)
	}
}
