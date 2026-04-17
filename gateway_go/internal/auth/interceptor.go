package auth

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// UnaryInterceptor wraps a gRPC unary server handler so that every
// request runs Provider.Authenticate first. On success the Principal
// is attached to ctx and the handler runs with it in scope; on
// failure the request is rejected with codes.Unauthenticated.
//
// Error mapping: any non-nil error from the Provider becomes a
// single UNAUTHENTICATED code — we deliberately do NOT leak the
// provider-specific error text to the caller (no "bad JWKS", no
// "signature mismatch") because those messages are reconnaissance
// aids for an attacker probing the auth path. The original error IS
// surfaced as the gRPC status *message* (the "%v" format), which
// travels in the trailer but not in the HTTP response code — so
// server-side structured logs can still pick it up without exposing
// it on the wire in a way that's visible to simple clients.
//
// A Provider that returns nil (no validation at all) is not valid;
// main.go must supply either NoOpProvider (Local) or a real
// Provider. Passing nil causes a panic on the first request, loud
// and obvious, rather than a silent allow-all.
func UnaryInterceptor(p Provider) grpc.UnaryServerInterceptor {
	if p == nil {
		panic("auth: UnaryInterceptor called with nil Provider")
	}
	return func(
		ctx context.Context,
		req any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		principal, err := p.Authenticate(ctx)
		if err != nil {
			return nil, status.Errorf(codes.Unauthenticated, "auth: %v", err)
		}
		return handler(WithPrincipal(ctx, principal), req)
	}
}

// StreamInterceptor is the streaming-RPC counterpart. It authenticates
// once at stream establishment and attaches the Principal to the
// wrapped stream's context. Per-message auth is intentionally NOT
// supported — once a stream is open, handlers trust its identity
// until it closes. The keepalive timeout from ADR-0006 bounds how
// stale that trust can get (≤ 40 s).
func StreamInterceptor(p Provider) grpc.StreamServerInterceptor {
	if p == nil {
		panic("auth: StreamInterceptor called with nil Provider")
	}
	return func(
		srv any,
		ss grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		principal, err := p.Authenticate(ss.Context())
		if err != nil {
			return status.Errorf(codes.Unauthenticated, "auth: %v", err)
		}
		return handler(srv, &principalStream{
			ServerStream: ss,
			ctx:          WithPrincipal(ss.Context(), principal),
		})
	}
}

// principalStream is the tiny wrapper that lets us override
// ServerStream.Context() to carry the authenticated Principal. gRPC
// doesn't provide a first-class way to inject values into a stream
// context; this is the canonical pattern (see grpc-go's own
// examples/features/interceptor/). The embedded ServerStream
// forwards every other method unchanged.
type principalStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (p *principalStream) Context() context.Context { return p.ctx }
