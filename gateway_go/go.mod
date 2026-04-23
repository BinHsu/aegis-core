// Go module root for the Aegis Core gateway (gateway_go/ per ADR-0008).
// The module path includes the gateway_go/ directory segment so import
// paths mirror the on-disk layout — important in this polyglot monorepo
// where engine_cpp/, frontend_web/, and a future tauri_rs/ all sit
// alongside this Go tree. The proto's option go_package tracks this
// path: "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1".
//
// Phase 2 brings Pion WebRTC, gRPC-Web termination, the session
// registry, and JWT middleware per ADR-0006 and ADR-0007.

module github.com/BinHsu/aegis-core/gateway_go

go 1.24.0

toolchain go1.24.12

require (
	github.com/bazelbuild/rules_go v0.60.0
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v4 v4.3.0 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/desertbit/timer v1.0.1 // indirect
	github.com/goccy/go-json v0.10.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/lestrrat-go/blackmagic v1.0.3 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc v1.0.6 // indirect
	github.com/lestrrat-go/iter v1.0.2 // indirect
	github.com/lestrrat-go/option v1.0.1 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pion/datachannel v1.6.0 // indirect
	github.com/pion/dtls/v3 v3.1.2 // indirect
	github.com/pion/ice/v4 v4.2.2 // indirect
	github.com/pion/interceptor v0.1.44 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.16 // indirect
	github.com/pion/rtp v1.10.1 // indirect
	github.com/pion/sctp v1.9.4 // indirect
	github.com/pion/sdp/v3 v3.0.18 // indirect
	github.com/pion/srtp/v3 v3.0.10 // indirect
	github.com/pion/stun/v3 v3.1.1 // indirect
	github.com/pion/transport/v4 v4.0.1 // indirect
	github.com/pion/turn/v4 v4.1.4 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/rs/cors v1.11.1 // indirect
	github.com/segmentio/asm v1.2.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/time v0.10.0 // indirect
	nhooyr.io/websocket v1.8.17 // indirect
)

require (
	github.com/coder/websocket v1.8.14
	github.com/golang-jwt/jwt/v5 v5.3.1
	github.com/improbable-eng/grpc-web v0.15.0
	github.com/lestrrat-go/jwx/v2 v2.1.6
	github.com/pion/webrtc/v4 v4.2.11
	github.com/prometheus/client_golang v1.23.2
	golang.org/x/net v0.50.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260120221211-b8f7ae30c516 // indirect
)
