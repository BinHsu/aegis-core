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
//   - Model discovery is explicit: the launcher refuses to start if the
//     required models aren't present under `models/` in the CAS layout
//     (`models/<id>/<sha>.<ext>` per ADR-0026) and prints the exact
//     `./tools/scripts/download_models.sh` invocation to fix it.
//     Auto-downloading 75 MB on first run would violate the
//     "predictable startup" principle operators expect. The engine
//     itself re-verifies every required entry (stat + size + SHA-256);
//     the launcher's job is just the fast-fail ergonomic check.
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
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
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

	// defaultGracePeriod is how long engine + gateway each get after
	// SIGTERM before we escalate to SIGKILL. The engine's gRPC
	// Server.Wait exits promptly on SIGTERM; the gateway's graceful
	// stop drains in < 10 s unless a viewer stream is stuck.
	// ADR-0006's 14400 s drain window is Cloud-mode only — Local mode
	// ships with a sharp 10 s deadline because an impatient developer
	// is a worse failure mode than a dropped viewer stream here.
	defaultGracePeriod = 10 * time.Second

	// frontendGracePeriod is deliberately shorter. The Vite dev server
	// is reached via the aspect_rules_js pnpm bash wrapper, which
	// traps SIGTERM and calls `kill -TERM $child; wait $child`. The
	// second `wait` empirically hangs >10 s in our setup — Vite's
	// internal shutdown chain (HMR socket, esbuild daemon, dep
	// pre-bundle cache flush) converges slowly. A dev server has
	// nothing valuable to flush; SIGKILL via the process group after
	// 3 s is the pragmatic right answer.
	frontendGracePeriod = 3 * time.Second
)

// withFrontend toggles the optional Vite dev-server child. Default off
// to preserve the original `bazel run //:app_local` behavior of "engine
// + gateway only" — historical scripts and CI lanes that hit the
// launcher should keep working without modification. Turn it on for
// the full host-UI demo:
//
//	./tools/bazelisk/bazelisk run //:app_local -- --with-frontend
var withFrontend = flag.Bool(
	"with-frontend",
	false,
	"Also start the Vite dev server (Phase 3 host UI on http://localhost:5173).",
)

func main() {
	flag.Parse()
	if err := run(); err != nil {
		logger.Error("launch failed", "err", err)
		os.Exit(1)
	}
}

// child is what the supervisor tracks for each subprocess. `wait` fires
// (exactly once) with the Wait() error when the subprocess exits.
//
// `gracePeriod` controls how long terminate() waits for a clean exit
// after SIGTERM before escalating to SIGKILL. Per-child because the
// shutdown semantics differ:
//
//   - engine: keep the default; whisper releases its model in <1s but
//     we leave headroom for any in-flight transcription final flush.
//   - gateway: keep the default; ADR-0006 GracefulStop drains
//     viewer streams which can take a few seconds with active subscribers.
//   - frontend (Vite via pnpm wrapper): shorter, because Vite's SIGTERM
//     path goes through aspect_rules_js's bash wrapper → trap →
//     `wait $child` → Vite's own slow shutdown chain, which empirically
//     takes >10s to converge. There is nothing valuable to flush on a
//     dev server; SIGKILL via the process group is fine.
type child struct {
	name        string
	cmd         *exec.Cmd
	gracePeriod time.Duration
	// pgroup is true iff startChild was called with newProcessGroup=true
	// (i.e. Setpgid was requested). Determines whether terminate()
	// sends signals to the whole process group or just the direct pid.
	pgroup bool

	// done is CLOSED (never value-sent-on) by startChild's waiter
	// goroutine when cmd.Wait returns. We use a close-based broadcast
	// rather than a single-consumer `<-chan error` because both
	// superviseUntilShutdown's fan-in AND terminate() need to observe
	// the exit — if wait were a one-shot receive, whichever got there
	// first would consume it and the other would block forever.
	done <-chan struct{}
	// waitErr holds the error from cmd.Wait. Read ONLY after `done` is
	// closed. Accessing before that is a data race.
	waitErr *error
}

// signalChild sends sig to the given command's process. When pgroup
// is true, the signal is sent to the whole process group (negative-
// pid convention) — required for grandchild propagation through the
// frontend's pnpm→node→vite chain. Safe to call on an already-dead
// process (syscall.Kill returns ESRCH which we ignore).
func signalChild(cmd *exec.Cmd, pgroup bool, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	target := cmd.Process.Pid
	if pgroup {
		target = -target
	}
	_ = syscall.Kill(target, sig)
}

func run() error {
	enginePath, gatewayPath, err := locateBinaries()
	if err != nil {
		return err
	}
	modelRoot, manifestPath, err := locateModelRoot()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Engine first — the gateway will immediately try to Health-probe
	// it during CreateMeeting, so it must be listening before we
	// accept any viewer traffic.
	//
	// AEGIS_MODEL_PATH is the CAS ROOT dir (ADR-0026 layout) —
	// engine's manifest walker resolves <root>/<id>/<sha>.<ext> for
	// each required=true entry. AEGIS_MANIFEST_PATH points to the
	// bundled manifest.json which declares what `required=true` means.
	engineCmd, engineDone, engineErr, err := startChild(ctx, "engine", enginePath,
		nil, []string{
			"AEGIS_MODEL_PATH=" + modelRoot,
			"AEGIS_MANIFEST_PATH=" + manifestPath,
			// Disable engine's Prometheus exposer in LOCAL mode —
			// it defaults to :8081 which would collide with the
			// gateway's own :8081 Prometheus exposer on the same
			// host. C-Obs-1 / ADR-0033 document the default-on
			// posture and this opt-out channel.
			"AEGIS_ENGINE_METRICS_ADDR=",
		},
		false /* newProcessGroup */)
	if err != nil {
		return fmt.Errorf("start engine: %w", err)
	}
	engine := child{
		name:        "engine",
		cmd:         engineCmd,
		done:        engineDone,
		waitErr:     engineErr,
		gracePeriod: defaultGracePeriod,
		pgroup:      false,
	}

	if err := waitEngineReady(ctx, engineAddr); err != nil {
		logger.Error("engine did not become ready", "err", err)
		terminate(engine)
		return err
	}
	logger.Info("engine ready", "addr", engineAddr, "model_root", modelRoot)

	// Gateway second — now that the engine is known-good, any /healthz
	// hit on the gateway will report "engine.reachable=true".
	gatewayCmd, gatewayDone, gatewayErr, err := startChild(ctx, "gateway", gatewayPath,
		nil, []string{"AEGIS_ENGINE_ADDR=" + engineAddr},
		false /* newProcessGroup */)
	if err != nil {
		terminate(engine)
		return fmt.Errorf("start gateway: %w", err)
	}
	gateway := child{
		name:        "gateway",
		cmd:         gatewayCmd,
		done:        gatewayDone,
		waitErr:     gatewayErr,
		gracePeriod: defaultGracePeriod,
		pgroup:      false,
	}
	logger.Info("gateway up", "http", ":8080", "grpc", ":9090")

	// Children are stored in startup order; superviseUntilShutdown
	// tears them down in REVERSE so the gateway gets a chance to
	// drain before the engine releases its model.
	children := []child{engine, gateway}

	if *withFrontend {
		// Frontend script lives in the source tree, not under Bazel's
		// runfiles — use BUILD_WORKSPACE_DIRECTORY (the same env that
		// resolves the model path) to find it.
		ws := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
		frontendScript := filepath.Join(ws, "tools", "scripts", "frontend.sh")
		if _, err := os.Stat(frontendScript); err != nil {
			terminateAll(children)
			return fmt.Errorf(
				"frontend script not found at %s — was the repo restructured? %w",
				frontendScript, err)
		}
		frontendCmd, frontendDone, frontendErr, err := startChild(ctx, "frontend",
			frontendScript, []string{"dev"}, nil,
			true /* newProcessGroup — pnpm→node→vite grandchildren */)
		if err != nil {
			terminateAll(children)
			return fmt.Errorf("start frontend: %w", err)
		}
		children = append(children, child{
			name:        "frontend",
			cmd:         frontendCmd,
			done:        frontendDone,
			waitErr:     frontendErr,
			gracePeriod: frontendGracePeriod,
			pgroup:      true,
		})
		logger.Info("frontend dev server starting",
			"url", "http://localhost:5173",
			"hint", "Vite usually reports 'Local: http://...' within ~200 ms")
	}

	logger.Info("press Ctrl-C to stop")
	return superviseUntilShutdown(ctx, children)
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

// locateModelRoot resolves (modelRoot, manifestPath) for the engine's
// CAS preflight walker (ADR-0026). Bazel's `run` command sets
// BUILD_WORKSPACE_DIRECTORY to the repo root, where models/ lives.
//
// The launcher does NOT re-verify SHA-256 — that is the engine's job
// (engine_cpp/src/models/manifest_loader). All the launcher promises is
// "the manifest + models directory are present so the engine can
// actually boot"; any deeper corruption surfaces as a fast-fail when
// the engine process starts.
func locateModelRoot() (modelRoot, manifestPath string, err error) {
	ws := os.Getenv("BUILD_WORKSPACE_DIRECTORY")
	if ws == "" {
		return "", "", errors.New(
			"BUILD_WORKSPACE_DIRECTORY not set — app_local must be invoked " +
				"via `bazel run //:app_local`, not executed directly")
	}
	modelRoot = filepath.Join(ws, "models")
	manifestPath = filepath.Join(modelRoot, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		return "", "", fmt.Errorf(
			"model manifest not found at %s\n"+
				"  the repo should ship this file — if missing, `git status` to investigate",
			manifestPath)
	}
	if _, err := os.Stat(modelRoot); err != nil {
		return "", "", fmt.Errorf(
			"models directory not found at %s\n"+
				"  run `./tools/scripts/download_models.sh` from the repo root to populate it",
			modelRoot)
	}
	return modelRoot, manifestPath, nil
}

// startChild spawns a subprocess with its stdout/stderr line-buffered
// and tagged. Returns the *exec.Cmd, a `done` channel that is CLOSED
// (never sent-to) when cmd.Wait returns, and a pointer into which the
// Wait error is stored. The close-broadcast pattern lets multiple
// observers watch the same exit event without draining a one-shot
// channel — critical because both superviseUntilShutdown's fan-in and
// terminate() need to wait for the same exit.
//
// args / extraEnv entries are passed through verbatim. extraEnv
// ("KEY=VALUE") is appended to os.Environ() so the child inherits the
// launcher's PATH etc. while still getting its required configuration.
// args may be nil for binaries that take no positional arguments.
//
// newProcessGroup toggles `SysProcAttr.Setpgid=true`. Use it ONLY for
// children whose descendants might escape a direct SIGTERM — the
// frontend case specifically: `frontend.sh` execs `pnpm`, which forks
// `node`, which runs `vite`, which spawns esbuild daemons. Without
// Setpgid our SIGTERM hits pnpm, pnpm exits, grandchildren carry on.
// For engine / gateway (well-behaved Go binaries that handle SIGTERM
// themselves and fork nothing), DO NOT set Setpgid — it empirically
// breaks `cmd.Wait()`'s fast-reap path on darwin (process exits but
// Wait hangs until the grace period escalates), turning a 3 ms
// shutdown into a 12 s one.
func startChild(
	ctx context.Context,
	tag, path string,
	args []string,
	extraEnv []string,
	newProcessGroup bool,
) (*exec.Cmd, <-chan struct{}, *error, error) {
	// Plain exec.Command — NOT CommandContext, because the latter's
	// default cancellation behavior is SIGKILL. We want explicit
	// control over shutdown (SIGTERM first, then SIGKILL after the
	// grace period) which superviseUntilShutdown handles.
	cmd := exec.Command(path, args...)
	cmd.Env = append(os.Environ(), extraEnv...)
	if newProcessGroup {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, err
	}
	// Prefix each line with "[tag]" so two children's interleaved
	// output stays legible. os.Stdout/Stderr flushing is line-
	// buffered under a TTY and unbuffered when redirected — bufio's
	// scanner is correct in both.
	go streamLines(stdout, "["+tag+"] ", os.Stdout)
	go streamLines(stderr, "["+tag+"] ", os.Stderr)

	logger.Info("starting child", "tag", tag, "path", path)
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, err
	}

	// The close-broadcast pattern: one goroutine owns cmd.Wait, writes
	// the error to `waitErr`, then close()s `done`. Everyone else
	// observes the close (safe for arbitrary number of receivers).
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = cmd.Wait()
		close(done)
	}()

	// Honor the launcher context: if we receive SIGINT while a child
	// is mid-Start, propagate it immediately. For pgroup-leader
	// children (newProcessGroup=true) send the signal to the whole
	// process group (negative pid convention) so grandchildren go
	// too; otherwise the direct-pid signal is enough.
	go func() {
		<-ctx.Done()
		if cmd.Process == nil {
			return
		}
		signalChild(cmd, newProcessGroup, syscall.SIGTERM)
	}()

	return cmd, done, &waitErr, nil
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
// hit Ctrl-C) or one of the children exits unexpectedly. The
// `children` slice MUST be in startup order; teardown happens in
// reverse so the most-dependent layer (e.g. the frontend) goes first
// and the foundational layer (engine) goes last.
//
// On unexpected exit, every other child is torn down in startup-
// reverse order and a non-nil error is returned naming the offender.
func superviseUntilShutdown(ctx context.Context, children []child) error {
	if len(children) == 0 {
		return errors.New("supervise: no children to watch")
	}

	// Fan each child's `done` close into one stream so a single select
	// can react to "any unexpected exit" alongside ctx.Done(). Since
	// `done` is close-broadcast (not one-shot receive), these reader
	// goroutines do NOT consume the event — terminate() can observe
	// the same close later.
	type childExit struct {
		name string
		err  error
	}
	exits := make(chan childExit, len(children))
	for _, c := range children {
		c := c
		go func() {
			<-c.done
			exits <- childExit{name: c.name, err: *c.waitErr}
		}()
	}

	select {
	case ce := <-exits:
		logger.Error("child exited unexpectedly", "child", ce.name, "err", ce.err)
		// Stop everything else, in startup-reverse order.
		for i := len(children) - 1; i >= 0; i-- {
			if children[i].name == ce.name {
				continue // already exited
			}
			terminate(children[i])
		}
		return fmt.Errorf("child %s exited: %w", ce.name, ce.err)

	case <-ctx.Done():
		logger.Info("shutdown signal received; tearing down")
		// startChild's ctx-done goroutine already SIGTERM'd every
		// child when ctx cancelled. Walk in reverse so the gateway
		// drains BEFORE the engine releases its model, and the
		// frontend (if present) goes first because Vite has nothing
		// stateful to flush.
		for i := len(children) - 1; i >= 0; i-- {
			logger.Info("stopping", "child", children[i].name)
			terminate(children[i])
		}
		logger.Info("bye")
		return nil
	}
}

// terminateAll is the bail-out helper for partial-startup failures
// — e.g. engine + gateway started but frontend.sh wasn't on disk.
// Tears children down in startup-reverse order without checking ctx.
func terminateAll(children []child) {
	for i := len(children) - 1; i >= 0; i-- {
		// Send SIGTERM ourselves because no signal-handler goroutine
		// has fired yet (we're in the partial-startup error path).
		signalChild(children[i].cmd, children[i].pgroup, syscall.SIGTERM)
		terminate(children[i])
	}
}

// terminate waits for the child to exit on its own (typically because
// SIGTERM was already sent). If it does not exit within its configured
// gracePeriod, it is force-killed. Safe to call after the child has
// already exited (the Wait channel will fire immediately).
//
// All signal targets use `-cmd.Process.Pid` (the negative-pid kill
// convention) so the entire process group set up by startChild's
// Setpgid receives the signal — required for grandchild propagation
// (the frontend's `pnpm → node → vite` chain doesn't reliably
// forward signals down the chain by default).
func terminate(c child) {
	// Nudge SIGTERM — no-op if startChild's ctx-done goroutine
	// already delivered it (kernel accepts duplicate signals cheaply).
	signalChild(c.cmd, c.pgroup, syscall.SIGTERM)

	grace := c.gracePeriod
	if grace <= 0 {
		grace = defaultGracePeriod
	}

	select {
	case <-c.done:
	case <-time.After(grace):
		logger.Warn("child did not exit within grace; escalating to SIGKILL",
			"child", c.name, "grace", grace)
		signalChild(c.cmd, c.pgroup, syscall.SIGKILL)
		// Even after SIGKILL, `cmd.Wait()` can block if a descendant
		// kept our stdout/stderr pipe fds open (the Vite case — pnpm
		// bash wrapper, node, esbuild child all inherit the pipe; the
		// daemon-ish ones sometimes escape the pgroup via setsid). Go's
		// exec.Cmd.Wait blocks until the pipe EOFs, which won't happen
		// while a detached grandchild holds the write end. Abandon
		// reaping after a short second deadline — the launcher is
		// about to exit anyway; init will clean up zombies.
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			logger.Warn("wait did not return after SIGKILL; abandoning reap",
				"child", c.name,
				"note", "pipe fds likely held by detached grandchild; init will reap")
			if c.cmd.Process != nil {
				_ = c.cmd.Process.Release()
			}
		}
	}
}
