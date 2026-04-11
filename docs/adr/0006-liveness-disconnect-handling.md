# ADR-0006: Liveness and Disconnect Handling

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

A real-time meeting system must decide, for every connection in the
data path, **what "disconnected" means, how quickly it is detected, and
what happens when it fires**. Aegis Core has three data-path
connections per active session:

1. **Host → Go Gateway**, via WebRTC (UDP / DTLS / SRTP for media,
   DataChannel for control).
2. **Go Gateway → C++ Engine**, via native gRPC (HTTP/2).
3. **Viewer → Go Gateway**, via gRPC-Web (Cloud) or WebSocket
   (Local, see ADR-0007) — both over HTTP/2 long-lived
   streams.

Each of these has a different native keep-alive mechanism, a different
failure mode, and a different user-visible consequence when it breaks.
This ADR codifies the liveness policy across all three and the
graceful shutdown policy for rolling deployments.

A related question — what happens when the host or engine **crashes**
(as opposed to a transient network issue) — is covered in
`ARCHITECTURE.md` §11 Known Limitations L1 and L2. This ADR focuses on
the **transient disconnect** path and the detection / teardown
mechanics.

## Decision Drivers

- **D1. Real-world networks flap.** WiFi handovers, elevator blind
  spots, VPN re-negotiations, and screen-sleep-resume events routinely
  cause 2–15 second connectivity gaps. Treating every flap as a
  permanent disconnect kills meetings for trivial reasons.
- **D2. Permanent disconnects must still be detected quickly.** If
  the host laptop is truly gone (crashed, unplugged, OS killed the
  browser tab), the session must terminate within a bounded time so
  that compute resources are freed and viewers are informed.
- **D3. Use protocol-native mechanisms, don't reinvent.** WebRTC,
  gRPC/HTTP2, and WebSocket all have built-in liveness mechanisms.
  Writing custom heartbeats on top of them is both redundant and a
  common source of bugs.
- **D4. Graceful rolling updates are non-negotiable.** Kubernetes
  rolling updates must not kill active meetings; Go Gateway pods must
  drain existing sessions before terminating.
- **D5. State transitions must be observable.** The user experience
  during transient loss must tell the user what is happening ("Host
  reconnecting..."), not leave them staring at a frozen screen.

## Policy

### Host ↔ Go Gateway (WebRTC)

**Mechanism**: ICE Consent Freshness, per **RFC 7675**, as implemented
by the Pion Go WebRTC library.

- Every ~5 seconds, each peer sends a STUN Binding Request over the
  ICE candidate pair to confirm the other side still wants packets.
- If no response arrives for **30 seconds** (`Ta` in RFC 7675), the
  connection is declared failed.
- This is **RFC default behavior**, already implemented in Pion. We
  do not add custom timers or heartbeats.

**State machine** (from the WebRTC connection state spec):

```
              packets flowing
   ┌──────────>  Connected  <─────────┐
   │                 │                │
   │                 │ ~5s silence    │ recovery
   │                 ▼                │
   │           Disconnected  ─────────┘
   │                 │
   │                 │ no recovery for 30s
   │                 ▼
   └────────── Failed  ← session teardown
```

- `Connected`: live, PCM flowing normally.
- `Disconnected`: transient loss, inside the 30-second grace window.
  Viewers are shown a **"Host reconnecting..."** banner. Go Gateway
  **pauses** forwarding audio to the C++ engine (see "Pause/Resume
  Control Message" below).
- `Failed`: grace window expired. Session is terminated; viewers are
  disconnected with a clear "Meeting ended" message.
- `Connected` ← `Disconnected`: transient loss recovered. Viewers see
  the banner clear. Audio forwarding resumes.

**Implementation in Go Gateway**:

```go
peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
    switch state {
    case webrtc.PeerConnectionStateConnected:
        session.OnHostConnected()
    case webrtc.PeerConnectionStateDisconnected:
        session.OnHostTransientLoss()  // pause engine, notify viewers
    case webrtc.PeerConnectionStateFailed:
        session.Terminate("host connection failed")
    }
})
```

No additional timer is written in Aegis code — Pion drives the state
transitions.

### Go Gateway ↔ C++ Engine (native gRPC)

**Mechanism**: gRPC / HTTP/2 keep-alive pings.

- Server-side `keepalive.ServerParameters`:
  - `Time: 30 * time.Second` — send a PING if no frames for 30
    seconds.
  - `Timeout: 10 * time.Second` — if no ACK within 10 seconds, close
    the connection.
- Both sides run inside the same Kubernetes pod (or the same host
  in Local mode) so the network path is loopback / cluster-internal;
  failures here indicate pod failure, not transient network issues.
- If the C++ engine connection dies mid-session, the session is
  terminated (`ARCHITECTURE.md` §11 L2). The Go Gateway notifies the
  host via a DataChannel control message and drops all viewers.

**Rationale for these values**: the Go ↔ C++ link is intra-pod /
intra-host and should be rock-solid. A 30 / 10 second window is
conservative enough to tolerate a stop-the-world GC pause on the Go
side and an inference spike on the C++ side, without letting a truly
failed engine hang for too long.

### Viewer ↔ Go Gateway (gRPC-Web / HTTP/2)

**Mechanism**: HTTP/2 PING frames via Go's `google.golang.org/grpc`
`keepalive.ServerParameters`.

**Problem with defaults**: Go gRPC's default server keepalive is
`Time: 2 * time.Hour` — far too slow for detecting viewer disconnects
within a normal meeting's duration. A viewer who closes their laptop
lid would be considered "still connected" for two hours under default
settings, occupying fan-out resources.

**Aegis settings**:

```go
grpc.KeepaliveParams(keepalive.ServerParameters{
    Time:    30 * time.Second,   // send PING every 30s if idle
    Timeout: 10 * time.Second,   // drop if no PONG in 10s
})

grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
    MinTime:             10 * time.Second,
    PermitWithoutStream: true,
})
```

- Viewer disconnect detection window: **up to 40 seconds**.
- Does not need to match the 30-second host grace period — viewers
  are cheap fan-out targets; slower detection is acceptable.

### Pause/Resume Control Message (Go ↔ C++)

When the host enters `Disconnected` (transient loss), the Go Gateway
stops receiving PCM. It must tell the C++ engine to **pause**, not
interpret the silence as end-of-stream, and not flush a final
transcript segment prematurely.

To enable this cleanly, the gRPC `StreamTranscribe` protocol includes
a control-message `oneof`:

```proto
message IngestMessage {
  oneof payload {
    PcmChunk     pcm      = 1;
    ControlEvent control  = 2;
  }
}

message ControlEvent {
  enum Kind {
    UNSPECIFIED = 0;
    PAUSE       = 1;
    RESUME      = 2;
    END_STREAM  = 3;
  }
  Kind kind = 1;
}
```

- Go GW sends `ControlEvent{PAUSE}` on `Disconnected`.
- Go GW sends `ControlEvent{RESUME}` on `Disconnected → Connected`.
- Go GW sends `ControlEvent{END_STREAM}` on `Failed` or user-
  initiated meeting end.
- C++ engine's state machine treats `PAUSE` as "retain buffers, don't
  flush, don't emit end-of-utterance markers." `RESUME` resumes
  normal ingest. `END_STREAM` flushes any pending transcript and
  tears down.

This keeps the C++ engine's internal whisper state consistent across
transient loss.

## Graceful Shutdown on Rolling Updates

Kubernetes rolling updates are a daily operational event. They must
**never** kill active meetings. Go Gateway pods must drain cleanly.

**Mechanism**:

1. Pod manifest sets `terminationGracePeriodSeconds: 1800` (30
   minutes). This is the maximum acceptable drain time — longer than
   a typical meeting, shorter than truly abusive use cases.
2. Go Gateway installs a `SIGTERM` handler that:
   a. **Stops accepting new sessions** — `CreateMeeting` returns
      `UNAVAILABLE` and the load balancer drains the pod.
   b. **Keeps existing sessions running** until they end naturally
      (host disconnects, user ends meeting, or 30-minute cap is hit).
   c. When the last session ends **or** the 30-minute cap is
      reached, calls `http.Server.Shutdown(ctx)` and exits.
3. Kubernetes load balancer (ALB / NGINX ingress) marks the pod
   `NotReady` during termination, so new traffic lands on other
   replicas.
4. On hard 30-minute expiration, the kubelet sends SIGKILL. Any
   sessions still active at that moment are terminated — they were
   already abnormally long and the operational signal (need to
   deploy) is more important than the edge case.

**Session-affinity consideration**: because a session is owned by
exactly one Go Gateway replica (ADR-0004), a draining replica means
its owned sessions must complete on that replica; they are not
migrated. This is consistent with the "no shared state between
replicas" property.

## Decision Outcome

**We adopt the policy above:**

- **Host↔GW**: RFC 7675 ICE Consent Freshness (30s grace) via Pion
  default behavior.
- **GW↔Engine**: 30s PING / 10s timeout gRPC keepalive; any failure
  terminates session (L2).
- **Viewer↔GW**: 30s PING / 10s timeout HTTP/2 keepalive via custom
  `keepalive.ServerParameters`.
- **Pause/Resume**: `ControlEvent` proto oneof on the Go↔C++ ingest
  stream, pausing C++ during host transient loss.
- **Graceful shutdown**: `terminationGracePeriodSeconds: 1800` with
  SIGTERM handler that stops new sessions and lets existing ones
  drain.

### Why These Values

- **30s host grace** matches RFC 7675's `Ta` exactly — protocol-native,
  no custom timers, empirically validated by decades of WebRTC
  deployments to cover common transient events.
- **30s/10s gRPC keepalive on both intra-pod and viewer links**
  balances quick failure detection against false positives under
  brief GC pauses. Defaults of 2 hours would be dangerously slow for
  the meeting use case; values below 15s risk false positives.
- **30-minute drain cap** comfortably covers the 95th-percentile
  meeting length without allowing a stuck session to block a deploy
  indefinitely.

### Why Not Stricter

- A "zero-grace" policy (any flap terminates) was explicitly rejected
  (see conversation history): trading a tiny reliability gain for a
  massive UX regression.
- Going below 30 seconds on host liveness would require overriding
  Pion's defaults, which is both unnecessary work and a risk of
  diverging from battle-tested code.

### Why Not More Lenient

- A 60-second or 120-second host grace would tie up C++ engine RAM
  and GPU (if applicable) for longer than necessary. The 30-second
  window is already generous enough for common transient events.

## Consequences

### Positive

- Real-world transient events (WiFi handover, VPN re-negotiation,
  screen sleep) are absorbed without killing meetings.
- Permanent disconnects are detected and cleaned up within bounded
  time.
- Protocol-native mechanisms mean no custom timer code to maintain.
- Pause/Resume semantics keep whisper.cpp's internal state clean
  across transient loss.
- Rolling updates can happen any time without killing active
  meetings.

### Negative

- A stuck session can block a Go Gateway pod from terminating for
  up to 30 minutes. Operationally acceptable but must be visible in
  dashboards (a pod stuck in "Terminating" state for 25+ minutes
  should page the on-call).
- The Pause/Resume protocol adds modest complexity to the C++
  engine's state machine. This is a one-time cost and is well worth
  the UX benefit.
- Viewer disconnect detection is up to 40 seconds, meaning a closed-
  laptop viewer continues to count against fan-out capacity for that
  window. Acceptable because fan-out cost is low.
- Pion's defaults could change in a future version. We should pin
  the Pion version in Bazel and revisit this ADR on upgrades.

## Open Implementation Questions (Phase 2)

These are implementation details, not architectural decisions — they
are noted here so the Phase 2 engineer does not rediscover them.

- **Backoff on rapid reconnect loops**: if a host enters
  Disconnected → Connected → Disconnected repeatedly (e.g., flaky
  WiFi), should there be a circuit breaker that treats N flaps in M
  seconds as equivalent to a hard Failed? Proposal: yes, 5 flaps in
  60 seconds → terminate. To be confirmed in Phase 2.
- **Viewer notification on transient loss**: the banner "Host
  reconnecting..." is the UX spec, but the mechanism to push it
  (gRPC-Web server-streaming control channel, separate event stream,
  or piggybacked on transcript stream) is a Phase 2 choice.
- **Metrics and SLO for host stability**: add a metric
  `aegis_host_transient_loss_total` labeled by session_id, to observe
  the frequency of transient events in the wild. Useful for tuning
  the grace window later.

## Related

- ADR-0001 Session Join Mechanism for Meeting Viewers
- ADR-0004 Stateless Broadcast Relay
- ADR-0007 Local Mode LAN Topology
- `ARCHITECTURE.md` §4 Data Flow
- `ARCHITECTURE.md` §11 Known Limitations L1 / L2
- RFC 7675 "Session Traversal Utilities for NAT (STUN) Usage for
  Consent Freshness"
- Pion WebRTC documentation
- `google.golang.org/grpc/keepalive`
