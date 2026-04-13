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
	google.golang.org/grpc v1.80.0
	google.golang.org/protobuf v1.36.11
)

require (
	github.com/golang-jwt/jwt/v5 v5.3.1
	golang.org/x/net v0.49.0 // indirect
	golang.org/x/sys v0.40.0 // indirect
	golang.org/x/text v0.33.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260120221211-b8f7ae30c516 // indirect
)
