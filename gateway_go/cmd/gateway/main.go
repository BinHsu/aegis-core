// Package main is the entrypoint for the Aegis Core Go Gateway.
//
// Phase 1 Session 5 ships a minimum viable gateway: a net/http server
// that answers /healthz. Subsequent phases layer in:
//
//	Phase 2 : Pion WebRTC ingest from host; gRPC client to the C++
//	          engine; per-session fan-out to viewers; JWT middleware
//	          for ADR-0001 invite tokens; ADR-0007 WebSocket transport
//	          for Local mode viewers; ADR-0006 keepalive + graceful
//	          shutdown (SIGTERM drains to session_max_lifetime).
//	Phase 4 : Hexagonal auth/storage/telemetry ports so the Go GW can
//	          boot in Cloud mode (Cognito, DynamoDB) or Local mode
//	          (dummy auth, SQLite) behind the same interfaces.
//
// The binary is built via Bazel:
//
//	./tools/bazelisk/bazelisk build //gateway_go/cmd/gateway:gateway
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultAddr = ":8080"
	version     = "0.1.0-phase1-s5"
)

func main() {
	addr := defaultAddr
	if env := os.Getenv("AEGIS_GATEWAY_ADDR"); env != "" {
		addr = env
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"ready":true,"version":%q}`, version)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Signal-driven graceful shutdown. Phase 2 extends this to
	// terminationGracePeriodSeconds=14400 matching session_max_lifetime
	// per ADR-0006; the plumbing here is the hook point.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("aegis-gateway: listening on %s", addr)
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
