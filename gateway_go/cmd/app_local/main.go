// Package main is the Local-mode bundle entrypoint.
//
//	bazel run //:app_local
//
// starts the C++ engine on :50051 and the Go gateway on :8080/:9090,
// waits for the engine Health RPC to report Ready, and runs until
// Ctrl-C, at which point it terminates the gateway first (so no new
// sessions are accepted) and then the engine (so any live session can
// flush transcript state before the model is released).
//
// Design rationale:
//
//   - CLAUDE.md Rule 3 promises "clone it, build it, it just works."
//     This binary is the single-command realization of that promise —
//     no docker-compose, no supervisor config, no two terminals.
//
//   - Both children run as subprocesses rather than being linked in-
//     process because the engine is C++ and linking it into a Go
//     binary would drag the whisper.cpp toolchain into the Go build
//     graph (it doesn't). Subprocesses also give us honest isolation —
//     a crash in one binary doesn't bring down the other.
//
//   - Model discovery is explicit: the launcher refuses to start if
//     `models/ggml-tiny.en.bin` is missing and prints the exact
//     `./tools/scripts/download_models.sh` invocation to fix it.
//     Auto-downloading 75 MB on first run would violate the
//     "predictable startup" principle operators expect.
//
//   - Binary paths are resolved via Bazel runfiles so `bazel run`
//     works regardless of CWD. `bazel run` also sets
//     BUILD_WORKSPACE_DIRECTORY which we use to locate the models/
//     tree at the repo root.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/bazelbuild/rules_go/go/runfiles"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
)

// logger is the launcher's own logger — text handler because this is
// an interactive terminal tool, not a service running in a pod. Child
// processes (engine, gateway) emit whatever format THEIR env chooses;
// streamLines below prefixes their output with "[engine] " / "[gateway] "
// so the three streams (launcher + two children) remain distinguishable
// on a single terminal.
var logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
	Level: slog.LevelInfo,
})).With("tag", "launcher")

const (
	engineAddr = "localhost:50051"

	// healthPollTimeout covers whisper.cpp loading a 75 MB model +
	// backend init. Empirically ~2 s on M-series, < 5 s on a cold
	// Linux CI VM with cached model. 30 s is generous but worth it:
	// a false "engine did not start" error is much worse than an
	// extra second of polling on slow machines.
	healthPollTimeout  = 30 * time.Second
	healthPollInterval = 200 * time.Millisecond

	// shutdownGracePeriod is how long each child gets after SIGTERM
	// before we escalate to SIGKILL. The engine's gRPC Server.Wait
	// exits promptly on SIGTERM; the gateway's graceful stop drains
	// in < 10 s unless a viewer stream is stuck. ADR-0006's
	// 14400 s drain window is Cloud-mode only — Local mode ships
	// with a sharp 10 s deadline because an impatient developer is
	// a worse failure mode than a dropped viewer stream here.
	shutdownGracePeriod = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		logger.Error("launch failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	enginePath, gatewayPath, err := locateBinaries()
	if err != nil {
		return err
	}
	modelPath, err := locateModel()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Engine first — the gateway will immediately try to Health-probe
	// it during CreateMeeting, so it must be listening before we
	// accept any viewer traffic.
	engine, engineWait, err := startChild(ctx, "engine", enginePath,
		[]string{"AEGIS_MODEL_PATH=" + modelPath})
	if err != nil {
		return fmt.Errorf("start engine: %w", err)
	}

	if err := waitEngineReady(ctx, engineAddr); err != nil {
		logger.Error("engine did not become ready", "err", err)
		terminate(engine, engineWait)
		return err
	}
	logger.Info("engine ready", "addr", engineAddr, "model", filepath.Base(modelPath))

	// Gateway second — now that the engine is known-good, any /healthz
	// hit on the gateway will report "engine.reachable=true".
	gateway, gatewayWait, err := startChild(ctx, "gateway", gatewayPath,
		[]string{"AEGIS_ENGINE_ADDR=" + engineAddr})
	if err != nil {
		terminate(engine, engineWait)
		return fmt.Errorf("start gateway: %w", err)
	}
	logger.Info("gateway up", "http", ":8080", "grpc", ":9090")
	logger.Info("press Ctrl-C to stop")

	// Wait for either (a) a signal, or (b) an unexpected child exit.
	// An unexpected exit is treated as a fatal system failure — we
	// don't try to restart, because Local mode is an interactive dev
	// experience where surfacing the crash to the developer is more
	// valuable than pretending it didn't happen.
	return superviseUntilShutdown(ctx, engine, gateway, engineWait, gatewayWait)
}

// locateBinaries returns the runfiles paths to the engine and gateway
// binaries. The paths are prefixed with the apparent workspace name
// "aegis_core" (matching MODULE.bazel line 17); runfiles.Rlocation
// maps that onto the actual on-disk path for the current Bazel config.
func locateBinaries() (engine, gateway string, err error) {
	rf, err := runfiles.New()
	if err != nil {
		return "", "", fmt.Errorf("runfiles.New: %w", err)
	}
	engine, err = rf.Rlocation("aegis_core/engine_cpp/cmd/engine/engine")
	if err != nil {
		return "", "", fmt.Errorf("locate engine binary in runfiles: %w", err)
	}
	gateway, err = rf.Rlocation("aegis_core/gateway_go/cmd/gateway/gateway_/gateway")
	if err != nil {
		return "", "", fmt.Errorf("locate gateway binary in runfiles: %w", err)
	}
	if _, err := os.Stat(engine); err != nil {
		return "", "", fmt.Errorf("engine binary not found at %s: %w", engine, err)
	}
	if _, err := os.Stat(gateway); err != nil {
		return "", "", fmt.Errorf("gateway binary not found at %s: %w", gateway, err)
	}
	return engine, gateway, nil
}

// locateModel resolves the ggml-tiny.en.bin path. Bazel's `run`
// command sets BUILD_WORKSPACE_DIRECTORY to the repo root, where
// models/ lives (see tools/scripts/download_models.sh).
func locateModel() (string, error) {
	ws := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	if ws == "" {
		return "", errors.New(
			"BUILD_WORKSPACE_DIRECTORY not set — app_local must be invoked " +
				"via `bazel run //:app_local`, not executed directly")
	}
	model := filepath.Join(ws, "models", "ggml-tiny.en.bin")
	if _, err := os.Stat(model); err != nil {
		return "", fmt.Errorf(
			"whisper model not found at %s\n"+
				"  run `./tools/scripts/download_models.sh` from the repo root to fetch it",
			model)
	}
	return model, nil
}

// startChild spawns a subprocess with its stdout/stderr line-buffered
// and tagged. Returns the *exec.Cmd and a channel that fires with the
// Wait() error when the child exits.
//
// extraEnv entries ("KEY=VALUE") are appended to os.Environ() so the
// child inherits the launcher's PATH etc. while still getting its
// required configuration.
func startChild(
	ctx context.Context,
	tag, path string,
	extraEnv []string,
) (*exec.Cmd, <-chan error, error) {
	// Plain exec.Command — NOT CommandContext, because the latter's
	// default cancellation behavior is SIGKILL. We want explicit
	// control over shutdown (SIGTERM first, then SIGKILL after the
	// grace period) which superviseUntilShutdown handles.
	cmd := exec.Command(path)
	cmd.Env = append(os.Environ(), extraEnv...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, err
	}
	// Prefix each line with "[tag]" so two children's interleaved
	// output stays legible. os.Stdout/Stderr flushing is line-
	// buffered under a TTY and unbuffered when redirected — bufio's
	// scanner is correct in both.
	go streamLines(stdout, "["+tag+"] ", os.Stdout)
	go streamLines(stderr, "["+tag+"] ", os.Stderr)

	logger.Info("starting child", "tag", tag, "path", path)
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// Honor the launcher context: if we receive SIGINT while a child
	// is mid-Start, propagate it immediately.
	go func() {
		<-ctx.Done()
		if cmd.Process == nil {
			return
		}
		// Signal once; superviseUntilShutdown handles any escalation.
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}()

	return cmd, waitCh, nil
}

// streamLines copies lines from r to w, prepending prefix to each.
// Exits when r is closed (on child exit).
func streamLines(r io.Reader, prefix string, w io.Writer) {
	scanner := bufio.NewScanner(r)
	// Engine log lines can carry the whisper_print_system_info string
	// which at ~200 chars is well inside the default 64 KiB buffer;
	// no MaxScanTokenSize bump needed.
	for scanner.Scan() {
		fmt.Fprintf(w, "%s%s\n", prefix, scanner.Text())
	}
}

// waitEngineReady polls the engine's Health RPC until it reports
// Ready=true or the deadline is hit. Using the real RPC (not just a
// TCP connect) means we don't hand control back to main until the
// whisper model has actually loaded — avoiding a race where the
// gateway's first CreateMeeting call would otherwise fail with
// "engine not ready" seconds after startup.
func waitEngineReady(ctx context.Context, addr string) error {
	conn, err := grpc.NewClient(addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial engine: %w", err)
	}
	defer func() { _ = conn.Close() }()
	client := aegisv1.NewEngineClient(conn)

	deadline := time.Now().Add(healthPollTimeout)
	ticker := time.NewTicker(healthPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		resp, err := client.Health(probeCtx, &aegisv1.HealthRequest{})
		cancel()
		if err == nil && resp.GetReady() {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("engine %s did not become ready within %v (last error: %v)",
					addr, healthPollTimeout, err)
			}
			return fmt.Errorf("engine %s reachable but not Ready within %v", addr, healthPollTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// superviseUntilShutdown blocks until either ctx is cancelled (user
// hit Ctrl-C) or one of the children exits unexpectedly. On normal
// shutdown, the gateway is stopped first so no new sessions are
// admitted during engine teardown. On unexpected exit, the surviving
// sibling is stopped and a non-nil error is returned.
func superviseUntilShutdown(
	ctx context.Context,
	engine, gateway *exec.Cmd,
	engineWait, gatewayWait <-chan error,
) error {
	select {
	case err := <-gatewayWait:
		logger.Error("gateway exited unexpectedly", "err", err)
		terminate(engine, engineWait)
		return fmt.Errorf("gateway exited: %w", err)
	case err := <-engineWait:
		logger.Error("engine exited unexpectedly", "err", err)
		terminate(gateway, gatewayWait)
		return fmt.Errorf("engine exited: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received; stopping gateway")
		// startChild's ctx-done goroutine already SIGTERM'd both
		// children when ctx cancelled, so this is just drainage.
		// We still call terminate() for its SIGKILL-after-grace-period
		// behavior in case a child ignored SIGTERM.
		terminate(gateway, gatewayWait)
		logger.Info("gateway down; stopping engine")
		terminate(engine, engineWait)
		logger.Info("bye")
		return nil
	}
}

// terminate waits for the child to exit on its own (typically because
// SIGTERM was already sent). If it does not exit within the grace
// period, it is force-killed. Safe to call after the child has already
// exited (the Wait channel will fire immediately).
func terminate(cmd *exec.Cmd, waitCh <-chan error) {
	var once sync.Once
	sigterm := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
	}
	// Nudge — no-op if startChild's goroutine already delivered it.
	once.Do(sigterm)

	select {
	case <-waitCh:
	case <-time.After(shutdownGracePeriod):
		logger.Warn("child did not exit within grace; escalating to SIGKILL",
			"grace", shutdownGracePeriod)
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waitCh
	}
}
