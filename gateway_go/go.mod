// Go module root for the Aegis Core gateway (gateway_go/ per ADR-0008).
// The module name matches what proto/aegis/v1/aegis.proto declared via
// option go_package = "github.com/BinHsu/aegis-core/gen/go/aegis/v1;aegisv1".
//
// Phase 1 Session 5 keeps this module minimal — a pure net/http server
// on :8080 responding to /healthz. Phase 2 brings Pion WebRTC, gRPC-Web
// termination, session registry, and JWT middleware per ADR-0006 and
// ADR-0007. Those additions will land in this module.

module github.com/BinHsu/aegis-core

go 1.24
