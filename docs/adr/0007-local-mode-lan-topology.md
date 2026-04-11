# ADR-0007: Local Mode LAN Topology

- **Status**: Accepted
- **Date**: 2026-04-11
- **Deciders**: Project owner, architect
- **Supersedes**: —
- **Superseded by**: —

## Context

Aegis Core's "Local mode" (`DeployMode=LOCAL`) is the deployment that
satisfies the project's core ethos: **"Clone it, build it, it just
works locally."** A user runs `bazel run //:app_local` and the entire
stack — C++ engine, Go Gateway, and frontend — comes up on a single
machine, no cloud credentials, no network access required.

A natural question: in Local mode, does the product support **multi-
device** usage? Specifically:

- Is the host (staff) machine the **only** device that can participate
  in a meeting?
- Or can a second device on the same Wi-Fi (the boss's phone, a
  secondary laptop) connect to the host machine as a **viewer**?

The V1 Aegis-Prompter (the original Python / Mac-only version at
<https://github.com/BinHsu/Aegis-Prompter>) supported LAN viewers:
the staff ran the engine on a Mac, and the boss could open the staff's
IP address on a second device to see the prompter. This behavior is
part of the V1 user mental model that V2 customers will bring with
them.

This ADR decides whether V2 Local mode preserves that behavior.

## Decision Drivers

- **D1. V1 user mental model continuity.** Existing Aegis users expect
  LAN viewer support. Removing it would be a regression for the V1
  → V2 migration path.
- **D2. Core ethos: minimal struggle.** Any LAN mechanism must not
  require the user to install certificates, configure firewalls
  manually, or run arcane CLI commands.
- **D3. Privacy posture preserved.** Local mode's appeal is stronger
  privacy than cloud mode (no external network). LAN multi-device
  must not accidentally expose the host to untrusted parties.
- **D4. No operational overhead from cloud mechanisms.** Local mode
  has no Cognito, no ACM, no load balancer. Any dependency on cloud-
  mode infrastructure is disqualifying.
- **D5. Architectural parity with cloud mode where possible.** The
  same codebase, the same session model (ADR-0001), the same
  statelessness (ADR-0004), the same liveness handling (ADR-0006).
  Divergence creates maintenance burden.

## Considered Options

### Option A — Single device, single user (no LAN)

Local mode binds the Go Gateway to `localhost` only. Viewer UI runs
on the same machine as the host UI. No second device involvement.

### Option B — LAN multi-device (V1-style) ✅ chosen

Go Gateway binds to all LAN interfaces. A second device on the same
Wi-Fi can open the host's LAN URL to join as a viewer. Mirrors the
V1 behavior.

### Option C — LAN multi-device with mDNS / Bonjour auto-discovery

Same as Option B, but the host advertises itself via mDNS (`_aegis._tcp`)
so viewer devices can find it without typing an IP address. More
polished UX, more complexity.

## Decision Outcome

**We choose Option B (LAN multi-device) for MVP Local mode.**

Option C (mDNS auto-discovery) is **deferred to Phase 5** as an
ergonomic enhancement. For MVP, LAN discovery uses a simpler and
already-battle-tested approach: **QR code on the host UI**.

### Topology

```
  ┌─────────────────────────────────────────┐
  │   Staff Host Laptop                     │
  │                                         │
  │   ┌─────────────────────────────────┐   │
  │   │ Aegis Frontend (host role)      │   │
  │   │ Loaded from http://localhost:P  │   │
  │   │ - Captures audio (WebRTC)       │   │
  │   │ - Shows prompter output         │   │
  │   │ - Displays join QR code         │   │
  │   └─────────────┬───────────────────┘   │
  │                 │                       │
  │   ┌─────────────▼───────────────────┐   │
  │   │ Go Gateway                      │   │
  │   │ Binds 0.0.0.0:P on LAN iface    │   │
  │   │ Single session, local relay     │   │
  │   └─────────────┬───────────────────┘   │
  │                 │ stdin/stdout gRPC     │
  │   ┌─────────────▼───────────────────┐   │
  │   │ C++ Engine (child process)      │   │
  │   │ Spawned by Go supervisor        │   │
  │   └─────────────────────────────────┘   │
  │                                         │
  └────────────────────┬────────────────────┘
                       │
              ┌────────┴─────────┐
              │                  │
              │  Local Wi-Fi     │
              │                  │
   ┌──────────┴──┐        ┌──────┴─────────┐
   │ Boss Phone  │        │ Secondary PC   │
   │ Scans QR →  │        │ (viewer role)  │
   │ http://10...│        │ Types IP URL   │
   │  /view/XYZ  │        │                │
   └─────────────┘        └────────────────┘
```

### Discovery via QR Code

When the host starts a meeting in Local mode, the UI displays a QR
code encoding the viewer join URL:

```
http://<host_lan_ip>:<port>/view/<session_id>?token=<jwt>
```

- The host UI derives `<host_lan_ip>` by enumerating non-loopback
  network interfaces at startup. If multiple LAN interfaces exist
  (Wi-Fi + Ethernet + VPN), the UI presents a picker.
- The port is chosen at startup by binding to port 0 and reading
  back the assigned port (avoiding collisions with other local
  services).
- The session token follows the ADR-0001 Option B scheme (short-
  lived JWT tied to session).
- Viewer scans QR with phone camera → opens in system browser →
  joins meeting.

**Fallback**: if scanning is not possible (e.g., the viewer is on a
laptop without a camera), the host UI also displays the URL in a
copy-friendly format.

### Gateway LAN Binding Behavior

- In Local mode, Go Gateway binds on `0.0.0.0:<port>` (all LAN
  interfaces).
- In Cloud mode, Go Gateway binds on a cluster-internal IP only; the
  external entry point is the ingress controller.
- The bind behavior is controlled by `DeployMode=LOCAL`, consistent
  with ARCHITECTURE §5 "Dual-Mode Parity" and the Ports and Adapters
  pattern.

**Firewall prompt (macOS, Windows)**: the first time the Go Gateway
binds to a LAN interface on macOS, the OS prompts "Do you want the
application to accept incoming network connections?" This is a known
UX friction point. Mitigations:

- README documents this prompt and explains why it is safe to
  approve.
- Future Phase 5 enhancement: code-sign the binary so the prompt
  appears only on first launch (Apple Gatekeeper improves UX for
  signed apps).

### Protocol Choice for Viewer → Gateway on LAN

The LAN topology introduces a subtle protocol problem that does not
exist in Cloud mode:

- Browsers require a **secure context** (HTTPS) for many modern
  APIs. `localhost` is exempt, but **LAN IP addresses are not**.
- A viewer loading `http://192.168.1.42:8080/view/...` on a phone
  browser is not in a secure context.
- gRPC-Web in browsers typically runs over HTTP/2 + TLS. Running
  gRPC-Web over plain HTTP/1.1 works in some implementations but is
  fragile and browser-specific.
- Getting real TLS certs on an arbitrary LAN IP is impractical
  (would need DHCP-aware cert minting; `mkcert` requires user CA
  install, violating D2).

**Decision**: in Local mode, the viewer transport is **WebSocket +
Protobuf** (binary framing), not gRPC-Web. WebSocket over plain
`ws://` is allowed from non-secure-context pages on all major
browsers. Protobuf framing keeps the wire format compatible with
Cloud mode's gRPC-Web for transcript payloads.

The `AudioCaptureProvider` abstraction required by ADR-0002
Constraint 2 is mirrored by a **`TranscriptStreamProvider`**
abstraction on the viewer side, with two implementations:

- `GrpcWebTranscriptStreamProvider` — used in Cloud mode.
- `WebSocketTranscriptStreamProvider` — used in Local mode.

Both deserialize the same Protobuf messages; the difference is only
the wire framing. Call sites remain transport-agnostic.

The **host** transport stays WebRTC in both Cloud and Local mode —
`localhost` is a secure context, so `getUserMedia` /
`getDisplayMedia` work without TLS on the host side.

### Single-Session Local Mode

Local mode supports exactly **one active meeting at a time** per
host machine. There is no multi-tenancy in Local mode; there is no
tenant concept at all. The Go Gateway spawns a single C++ engine
child process and manages a single session.

If a second "New Meeting" is pressed while one is active, the UI
surfaces a confirmation dialog: "Replace current meeting?"

### Stateless Property Preserved

Per ADR-0004, even Local mode keeps the Go Gateway stateless with
respect to meeting content. Transcripts flow through the LAN fan-out
channel and are discarded server-side; the host device holds the
source of truth; viewers see only what they receive live.

The LAN topology changes **who the viewers are** (devices on the
same Wi-Fi vs devices anywhere on the internet), not the
statelessness property.

## Why Not Option A (Single Device)

- Violates **D1**. V1 users expect LAN viewer support.
- Reduces product value for the "I use my laptop to run the engine
  and my boss watches on their phone" scenario, which is a primary
  Local mode use case.
- Saves only modest implementation cost (a few hundred lines for
  LAN binding, QR code generation, firewall docs). Not worth the
  feature loss.

## Why Not Option C (mDNS Auto-Discovery) for MVP

- Adds a Rust / Go dependency (`mdns-sd`, `libmdns`, or equivalent)
  and a network-service registration lifecycle to manage.
- Requires platform-specific permission prompts on some OSes
  (macOS 14+ Local Network Privacy prompt).
- The QR code alternative gives 90% of the UX benefit at 10% of the
  implementation cost.
- mDNS may land in Phase 5 as a polish item; it does not block MVP
  delivery.

## Constraints and Caveats

- **LAN trust model**: anyone on the same Wi-Fi network can attempt
  to reach the host's LAN port. Mitigations:
  - Viewer join requires the session token in the URL (ADR-0001).
  - Tokens are short-lived and session-scoped.
  - The threat model for Local mode assumes the Wi-Fi network is
    **already trusted** by the user (home, office, personal
    hotspot). Local mode is not designed to run safely on a
    coffee-shop Wi-Fi with strangers.
- **Firewall approval required on first run**. Documented in
  `README.md` and the first-run experience.
- **Single-session only**. If the user needs to run two meetings
  simultaneously on one machine, they must use Cloud mode. This is
  an acceptable limitation for Local mode's "simple, portable"
  positioning.
- **No cross-network operation**. A viewer on a different Wi-Fi
  (e.g., the host is on home Wi-Fi, the viewer is on mobile data)
  cannot join a Local mode meeting. For cross-network meetings, use
  Cloud mode.
- **Multicast / UDP broadcast not used** for MVP. The QR code
  mechanism avoids any dependency on multicast (some corporate
  Wi-Fi networks block it).

## Consequences

### Positive

- V1 user mental model preserved: Local mode supports the boss-on-
  phone use case.
- QR code discovery gives a polished UX without mDNS complexity.
- Protocol choice (WebSocket for viewer) side-steps the LAN TLS
  problem cleanly.
- Statelessness property (ADR-0004) preserved in Local mode.
- Host transport remains WebRTC (same as Cloud) — no divergence
  where it would hurt.

### Negative

- **Two viewer transport implementations to maintain**: gRPC-Web
  for Cloud, WebSocket for Local. Mitigated by the
  `TranscriptStreamProvider` abstraction that isolates the
  difference to two files.
- **First-run firewall prompt** is a UX bump for non-technical
  users. Mitigated by docs.
- **LAN trust is assumed**, not enforced. A hostile device on the
  same Wi-Fi could attempt to brute-force session tokens; defense
  relies on the tokens being random enough (128-bit minimum) and
  the sessions being short-lived.
- **Port selection is dynamic**; users cannot rely on a fixed port.
  The QR code hides this, but power users scripting against Aegis
  might find it awkward. Phase 5 could add a `--port` CLI flag.

## Open Implementation Questions (Phase 2 / 3)

- **Interface picker UX**: if the host has Wi-Fi + Ethernet + VPN +
  Docker bridge interfaces, how do we present the choice without
  overwhelming non-technical users? Proposal: show only interfaces
  with RFC 1918 addresses (10/8, 172.16/12, 192.168/16); hide VPN
  and Docker bridges by default; offer an "Advanced" disclosure for
  the rest.
- **QR code library**: use a pure-JS library on the frontend (e.g.,
  `qrcode` npm package) rather than adding a native dependency.
- **WebSocket subprotocol negotiation**: use
  `Sec-WebSocket-Protocol: aegis.v1.transcript` so future protocol
  versions can be negotiated cleanly.
- **Port ≠ localhost dev overlap**: if the developer is running
  another service on a chosen port, collision is possible. Binding
  to port 0 and reading back the OS-assigned port is the robust
  approach.

## Related

- ADR-0001 Session Join Mechanism for Meeting Viewers
- ADR-0002 Desktop Shell Technology (AudioCaptureProvider abstraction)
- ADR-0003 Host Audio Capture Strategy
- ADR-0004 Stateless Broadcast Relay
- ADR-0006 Liveness and Disconnect Handling
- `ARCHITECTURE.md` §5 Dual-Mode Parity (LOCAL vs CLOUD)
- V1 reference: <https://github.com/BinHsu/Aegis-Prompter>
