// Package main is the entrypoint for the Aegis Core Go Gateway.
//
// Phase 2 A2 turns this process into a dual-server: a gRPC Gateway
// on :9090 implementing aegis.v1.Gateway, plus the pre-existing
// HTTP /healthz probe on :8080. Both use the same engine client so
// CreateMeeting and /healthz see a consistent Health readout.
//
// Future phases:
//
//	Phase 2 A3 : Pion WebRTC ingest — NegotiateWebRTC flips from
//	             UNIMPLEMENTED to a real SDP exchange.
//	Phase 2 A4 : Full pipeline — host audio → WebRTC → PCM → engine
//	             StreamTranscribe → fan-out to viewers.
//	Phase 2 A5 : Cognito JWT middleware + WebSocket viewer transport
//	             for Local mode (ADR-0007).
//
// The binary is built via Bazel:
//
//	./tools/bazelisk/bazelisk build //gateway_go/cmd/gateway:gateway
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	gatewaygrpc "github.com/BinHsu/aegis-core/gateway_go/internal/grpc"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
	"github.com/BinHsu/aegis-core/gateway_go/internal/token"
)

const (
	defaultListenAddr = ":8080"
	defaultGRPCAddr   = ":9090"
	defaultEngineAddr = "localhost:50051"
	version           = "0.1.0-phase2-a2"
	engineRPCTimeout  = 2 * time.Second
)

func main() {
	listenAddr := defaultListenAddr
	if env := os.Getenv("AEGIS_GATEWAY_ADDR"); env != "" {
		listenAddr = env
	}
	grpcAddr := defaultGRPCAddr
	if env := os.Getenv("AEGIS_GATEWAY_GRPC_ADDR"); env != "" {
		grpcAddr = env
	}
	engineAddr := defaultEngineAddr
	if env := os.Getenv("AEGIS_ENGINE_ADDR"); env != "" {
		engineAddr = env
	}

	// Dial the engine. grpc.NewClient (the 1.64+ preferred form) does
	// NOT block on a live TCP connection — the RPC at call time drives
	// the dial. ADR-0006 keepalive tuning lands when we have bidi
	// streams (A4) that need aggressive liveness detection.
	conn, err := grpc.NewClient(
		engineAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("aegis-gateway: grpc.NewClient %q: %v", engineAddr, err)
	}
	defer func() { _ = conn.Close() }()
	engine := aegisv1.NewEngineClient(conn)

	// Session registry + JWT issuer. Both are process-scoped: the
	// registry per ADR-0004 "No Shared State Between Replicas", the
	// issuer's signing key per ADR-0001 "process-scoped random key".
	registry := session.NewRegistry()
	issuer, err := token.NewIssuer()
	if err != nil {
		log.Fatalf("aegis-gateway: token.NewIssuer: %v", err)
	}

	gatewaySvc, err := gatewaygrpc.New(gatewaygrpc.Config{
		Registry:           registry,
		Issuer:             issuer,
		Engine:             engine,
		EngineProbeTimeout: engineRPCTimeout,
	})
	if err != nil {
		log.Fatalf("aegis-gateway: gatewaygrpc.New: %v", err)
	}

	// HTTP server for /healthz. Kept alongside the gRPC server so
	// Kubernetes liveness probes (which want HTTP today) don't have
	// to speak gRPC.
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", makeHealthHandler(engine, engineAddr, registry))
	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// gRPC server for aegis.v1.Gateway.
	grpcSrv := grpc.NewServer()
	aegisv1.RegisterGatewayServer(grpcSrv, gatewaySvc)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		log.Fatalf("aegis-gateway: listen %s: %v", grpcAddr, err)
	}

	// Signal-driven graceful shutdown. ADR-0006's
	// terminationGracePeriodSeconds=14400 drain hook plugs in here in
	// A4 once we have live sessions to drain.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("aegis-gateway: HTTP  listening on %s", listenAddr)
		log.Printf("  engine_addr=%s", engineAddr)
		log.Printf("  version=%s", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("aegis-gateway: HTTP ListenAndServe: %v", err)
		}
	}()

	go func() {
		log.Printf("aegis-gateway: gRPC  listening on %s", grpcAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("aegis-gateway: gRPC Serve: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("aegis-gateway: shutdown signal received; draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shut down HTTP first (fast), then gRPC. GracefulStop waits for
	// in-flight RPCs to complete, which for streams means until the
	// client disconnects. A4 will add a bounded deadline + forced
	// Stop fallback once we have real stream fan-out.
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Printf("aegis-gateway: HTTP shutdown error: %v", err)
	}
	grpcSrv.GracefulStop()
	log.Printf("aegis-gateway: bye")
}

// gatewayHealth is the JSON shape the /healthz endpoint emits.
// Kept intentionally small and stable so operators and uptime
// monitoring can consume it without a proto dependency.
type gatewayHealth struct {
	Ready    bool         `json:"ready"`
	Version  string       `json:"version"`
	Sessions int          `json:"active_sessions"`
	Engine   engineHealth `json:"engine"`
}

type engineHealth struct {
	Reachable bool   `json:"reachable"`
	Addr      string `json:"addr"`
	Ready     bool   `json:"ready,omitempty"`
	Model     string `json:"model,omitempty"`
	Backend   string `json:"backend,omitempty"`
	EngineVer string `json:"version,omitempty"`
	Error     string `json:"error,omitempty"`
}

func makeHealthHandler(
	engine aegisv1.EngineClient,
	engineAddr string,
	registry *session.Registry,
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), engineRPCTimeout)
		defer cancel()

		result := gatewayHealth{
			Ready:    true,
			Version:  version,
			Sessions: registry.Len(),
			Engine:   engineHealth{Addr: engineAddr},
		}

		resp, err := engine.Health(ctx, &aegisv1.HealthRequest{})
		if err != nil {
			// Gateway itself is healthy even if engine isn't —
			// report 200 with engine.reachable=false so uptime
			// alerts can distinguish the two.
			result.Engine.Error = err.Error()
		} else {
			result.Engine.Reachable = true
			result.Engine.Ready = resp.GetReady()
			if info := resp.GetInfo(); info != nil {
				result.Engine.Model = info.GetModel()
				result.Engine.Backend = info.GetBackend()
				result.Engine.EngineVer = info.GetVersion()
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = writeJSON(w, result)
	}
}

func writeJSON(w http.ResponseWriter, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
