// Package main is the entrypoint for the Aegis Core Go Gateway.
//
// Phase 2 A1 turns this process from a standalone HTTP server into
// a gRPC *client* of the C++ engine (aegis.v1.Engine). The Gateway
// probes the engine's Health RPC on startup and again on every
// /healthz request, reporting the combined status so operators can
// distinguish "gateway alive, engine unreachable" from
// "both alive" from "gateway degraded".
//
// Future phases:
//
//	Phase 2 A2 : Gateway service (aegis.v1.Gateway) — CreateMeeting,
//	             NegotiateWebRTC, JoinAsViewer, EndMeeting RPCs.
//	             Session registry (ADR-0004). JWT middleware (ADR-0001).
//	Phase 2 A3 : Pion WebRTC ingest from the browser host.
//	Phase 2 A4 : Full pipeline — host audio → WebRTC → PCM → engine
//	             StreamTranscribe → fan-out to viewers.
//	Phase 2 A5 : WebSocket viewer transport for Local mode (ADR-0007).
//
// The binary is built via Bazel:
//
//	./tools/bazelisk/bazelisk build //gateway_go/cmd/gateway:gateway
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	aegisv1 "github.com/BinHsu/aegis-core/gen/go/aegis/v1"
)

const (
	defaultListenAddr = ":8080"
	defaultEngineAddr = "localhost:50051"
	version           = "0.1.0-phase2-a1"
	engineRPCTimeout  = 2 * time.Second
)

func main() {
	listenAddr := defaultListenAddr
	if env := os.Getenv("AEGIS_GATEWAY_ADDR"); env != "" {
		listenAddr = env
	}
	engineAddr := defaultEngineAddr
	if env := os.Getenv("AEGIS_ENGINE_ADDR"); env != "" {
		engineAddr = env
	}

	// Dial the engine. grpc.NewClient (the 1.64+ preferred form) does
	// NOT block on a live TCP connection — the RPC at call time drives
	// the dial. For Phase 2 A1 this is fine; ADR-0006 keepalive tuning
	// (30s/10s) lands in A2 once we wire keepalive.ClientParameters
	// on a client connection that has real traffic to keep alive.
	conn, err := grpc.NewClient(
		engineAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.Fatalf("aegis-gateway: grpc.NewClient %q: %v", engineAddr, err)
	}
	defer func() { _ = conn.Close() }()
	engine := aegisv1.NewEngineClient(conn)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", makeHealthHandler(engine, engineAddr))

	srv := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Signal-driven graceful shutdown. Phase 2 A2 aligns this with
	// ADR-0006's terminationGracePeriodSeconds=14400 drain — the
	// plumbing below is the hook point.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("aegis-gateway: listening on %s", listenAddr)
		log.Printf("  engine_addr=%s", engineAddr)
		log.Printf("  version=%s", version)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("aegis-gateway: ListenAndServe: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("aegis-gateway: shutdown signal received; draining")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("aegis-gateway: shutdown error: %v", err)
	}
	log.Printf("aegis-gateway: bye")
}

// gatewayHealth is the JSON shape the /healthz endpoint emits.
// Kept intentionally small and stable so operators and uptime
// monitoring can consume it without a proto dependency.
type gatewayHealth struct {
	Ready   bool         `json:"ready"`
	Version string       `json:"version"`
	Engine  engineHealth `json:"engine"`
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
) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), engineRPCTimeout)
		defer cancel()

		result := gatewayHealth{
			Ready:   true,
			Version: version,
			Engine:  engineHealth{Addr: engineAddr},
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
