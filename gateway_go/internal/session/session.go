// Package session implements the Go Gateway's in-memory session registry.
//
// Per ADR-0004 Stateless Broadcast Relay, the Gateway holds only
// per-session *routing* state — never transcript content. A Session
// carries the fields required to fan out live ViewerEvents to
// subscribers and to enforce host-only operations (such as
// EndMeeting). Transcript bytes pass through the TranscriptChan
// and are then discarded server-side.
//
// The registry is scoped to a single Gateway replica (ADR-0004
// "No Shared State Between Replicas"). Session-affinity routing at
// the ingress layer (ADR-0001) guarantees that the host and all
// viewers of a given session_id land on the same replica — otherwise
// the viewer would not see the host's transcript. If a replica
// dies, its sessions die with it (ARCHITECTURE.md §11 L2).
//
// Thread-safety:
//   - Registry is safe for concurrent Create/Get/Delete calls.
//   - Session fields exposed as exported for read are guarded by the
//     Session's own RWMutex; callers that need a consistent snapshot
//     across multiple fields must use Session.WithLock.
//
// Phase 2 A2 scope:
//   - Registry with Create / Get / Delete.
//   - Session struct with the subset of ADR-0004's sketch needed by
//     CreateMeeting + EndMeeting.
//
// Phase 2 A5 scope (this file additionally):
//   - Subscribe / Broadcast: per-session fan-out of *aegisv1.ViewerEvent
//     to N subscribers. Non-blocking send: a slow subscriber is dropped-
//     to (gap shows up as a missed sequence on the wire) rather than
//     back-pressuring the producer. Producer is the engine StreamTranscribe
//     consumer (lands in A4); for A5 the WebSocket and gRPC viewer
//     handlers are the only consumers, and tests inject events via
//     Broadcast directly.
//
// Deferred to A3 / A4:
//   - HostConnection / ViewerConnection types (need Pion WebRTC).
//   - LastHostPing enforcement + 30s grace window (ADR-0006).
package session

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
)

// ErrSessionNotFound is returned by Registry.Get and Registry.Delete
// when no session exists for the given id. Callers in the gRPC layer
// translate this to codes.NotFound.
var ErrSessionNotFound = errors.New("session: not found")

// ErrSessionExists is returned by Registry.Create when a session id
// collides with an existing one. Because NewID draws 128 bits from
// crypto/rand, the probability of a collision in practice is
// negligible — but the registry still guards against it to avoid
// silent overwrites.
var ErrSessionExists = errors.New("session: already exists")

// Session is the minimal per-meeting state held by the Gateway.
//
// Fields that are part of the broadcast routing (Viewers, HostConnID,
// LastHostPing) are guarded by the session's own RWMutex; fields set
// at creation and never mutated (ID, CreatedAt, TenantID, RAGID, etc.)
// are safe to read without locking.
type Session struct {
	// Immutable after creation.
	ID                      string
	CreatedAt               time.Time
	ExpiresAt               time.Time // wall-clock termination (ADR-0001: 4h default)
	TokenExpiresAt          time.Time // JWT exp claim (ADR-0001: session_max + 10m)
	TenantID                string    // "" in Local mode (ADR-0007 L7)
	RAGID                   string
	Title                   string
	LanguageHints           []string
	AllowedViewerAccountIDs []string // reserved (ADR-0001 Phase 5+); MVP ignores

	// Mutable state guarded by mu.
	mu           sync.RWMutex
	hostConnID   string
	viewers      map[string]struct{}
	lastHostPing time.Time
	ended        bool
	subscribers  map[*subscriber]struct{}
}

// subscriber owns one fan-out channel and a counter of dropped events
// (events the channel was too full to accept). Tests inspect the
// counter to confirm slow-consumer behavior; the WS / gRPC handlers
// surface the gap implicitly via the proto's `sequence` field gap.
type subscriber struct {
	ch      chan *aegisv1.ViewerEvent
	dropped uint64
}

// NewID returns a URL-safe base64 encoding of 16 crypto-random bytes
// (≈22 characters). Matches the comment on
// CreateMeetingResponse.session_id in proto/aegis/v1/aegis.proto.
func NewID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HostConnID returns the currently-registered host connection id, if any.
// Returns the empty string when no host is bound yet (WebRTC negotiation
// happens in A3).
func (s *Session) HostConnID() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hostConnID
}

// SetHostConnID binds a host connection to this session. Called by
// NegotiateWebRTC in A3.
func (s *Session) SetHostConnID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostConnID = id
}

// AddViewer registers a viewer connection id. Returns the new viewer
// count after insertion.
func (s *Session) AddViewer(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewers[id] = struct{}{}
	return len(s.viewers)
}

// RemoveViewer deregisters a viewer connection id. Returns the remaining
// viewer count.
func (s *Session) RemoveViewer(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.viewers, id)
	return len(s.viewers)
}

// ViewerCount returns the current number of subscribed viewers.
func (s *Session) ViewerCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.viewers)
}

// TouchHost updates LastHostPing. Used by the liveness tracker
// (ADR-0006) to feed the 30-second grace window timer.
func (s *Session) TouchHost(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastHostPing = at
}

// LastHostPing returns the most recent host liveness timestamp.
func (s *Session) LastHostPing() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastHostPing
}

// MarkEnded flips the session into the terminal state and closes
// every subscriber channel so blocked recv loops wake up and emit
// their terminal ENDED frame. Subsequent Gateway RPCs against this
// session return FAILED_PRECONDITION.
//
// Called by Registry.Delete before removal, and by the grace-window
// watchdog (A4) when the host grace window expires.
func (s *Session) MarkEnded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	for sub := range s.subscribers {
		close(sub.ch)
	}
	s.subscribers = nil
}

// Ended reports whether the session has been terminated.
func (s *Session) Ended() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ended
}

// Subscribe registers a fan-out channel for the lifetime of the
// returned unsubscribe func. The channel is buffered to `buffer`
// events; if the consumer is too slow Broadcast drops events for
// this subscriber rather than back-pressuring the producer
// (a slow viewer must not stall the engine ingest goroutine — see
// ADR-0010 ResourceBudget rationale).
//
// If the session has already ended, Subscribe returns a closed
// channel and a no-op unsubscribe. The caller's recv loop will
// observe the close on the next iteration.
//
// Unsubscribe is idempotent. Callers MUST call it (typically via
// `defer`) so the subscriber map does not leak across long-running
// sessions.
func (s *Session) Subscribe(buffer int) (<-chan *aegisv1.ViewerEvent, func()) {
	if buffer <= 0 {
		buffer = 32 // ADR-0004 sketch: "bounded channel (capacity ~32)"
	}
	sub := &subscriber{ch: make(chan *aegisv1.ViewerEvent, buffer)}

	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		close(sub.ch)
		return sub.ch, func() {}
	}
	s.subscribers[sub] = struct{}{}
	s.mu.Unlock()

	var once sync.Once
	unsub := func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if s.subscribers == nil {
				// MarkEnded already closed this channel.
				return
			}
			if _, ok := s.subscribers[sub]; !ok {
				return
			}
			delete(s.subscribers, sub)
			close(sub.ch)
		})
	}
	return sub.ch, unsub
}

// Broadcast non-blocking sends ev to every current subscriber. A
// subscriber whose buffer is full has the event dropped (its `dropped`
// counter is incremented). On a session that has already ended,
// Broadcast is a no-op.
//
// Returns the number of subscribers that received the event and the
// number that dropped it. Useful for tests and operational metrics.
func (s *Session) Broadcast(ev *aegisv1.ViewerEvent) (delivered, dropped int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.ended {
		return 0, 0
	}
	for sub := range s.subscribers {
		select {
		case sub.ch <- ev:
			delivered++
		default:
			sub.dropped++
			dropped++
		}
	}
	return delivered, dropped
}

// SubscriberCount reports how many fan-out subscribers are currently
// registered. Distinct from ViewerCount, which counts authenticated
// connection ids — Subscribe is the underlying primitive that the
// gRPC handler and the WebSocket handler both use.
func (s *Session) SubscriberCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.subscribers)
}

// Config captures the create-time inputs for a new session — a direct
// mapping of aegis.v1.CreateMeetingRequest after auth / validation.
type Config struct {
	TenantID                string
	RAGID                   string
	Title                   string
	LanguageHints           []string
	AllowedViewerAccountIDs []string

	// Lifetimes; zero values cause the registry to substitute ADR-0001
	// defaults (SessionMaxLifetime = 4h, TokenGrace = 10m).
	SessionMaxLifetime time.Duration
	TokenGrace         time.Duration
}

// Registry is an in-memory map of session_id → *Session, safe for
// concurrent use. One instance per Gateway replica.
type Registry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
	now      func() time.Time // injection point for tests
}

// NewRegistry constructs an empty Registry using the real wall clock.
func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string]*Session),
		now:      time.Now,
	}
}

// newRegistryWithClock is the test seam.
func newRegistryWithClock(now func() time.Time) *Registry {
	return &Registry{
		sessions: make(map[string]*Session),
		now:      now,
	}
}

// Create materializes a new Session from the given config, with a
// fresh random id. Returns the created session so the caller can
// derive the join token from it.
func (r *Registry) Create(cfg Config) (*Session, error) {
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	return r.createWithID(id, cfg)
}

// createWithID exists for tests that need deterministic ids.
func (r *Registry) createWithID(id string, cfg Config) (*Session, error) {
	life := cfg.SessionMaxLifetime
	if life <= 0 {
		life = 4 * time.Hour // ADR-0001 recommended default
	}
	grace := cfg.TokenGrace
	if grace <= 0 {
		grace = 10 * time.Minute // ADR-0001 recommended default
	}

	now := r.now()
	sess := &Session{
		ID:                      id,
		CreatedAt:               now,
		ExpiresAt:               now.Add(life),
		TokenExpiresAt:          now.Add(life + grace),
		TenantID:                cfg.TenantID,
		RAGID:                   cfg.RAGID,
		Title:                   cfg.Title,
		LanguageHints:           append([]string(nil), cfg.LanguageHints...),
		AllowedViewerAccountIDs: append([]string(nil), cfg.AllowedViewerAccountIDs...),
		viewers:                 make(map[string]struct{}),
		lastHostPing:            now,
		subscribers:             make(map[*subscriber]struct{}),
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.sessions[id]; exists {
		return nil, ErrSessionExists
	}
	r.sessions[id] = sess
	return sess, nil
}

// Get looks up a session by id. Returns ErrSessionNotFound if missing.
func (r *Registry) Get(id string) (*Session, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sess, ok := r.sessions[id]
	if !ok {
		return nil, ErrSessionNotFound
	}
	return sess, nil
}

// Delete removes a session from the registry, marking it ended first
// so any in-flight goroutine observing Session.Ended() can bail out.
// Returns ErrSessionNotFound if the id was not present.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	sess, ok := r.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	sess.MarkEnded()
	delete(r.sessions, id)
	return nil
}

// Len reports the number of active sessions. Useful for /healthz
// reporting and tests.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}
