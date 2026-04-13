package session

import (
	"testing"
	"time"
)

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
