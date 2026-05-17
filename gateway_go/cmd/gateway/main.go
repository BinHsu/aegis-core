// Package main is the entrypoint for the Aegis Core Go Gateway.
//
// As of Phase 2 A4, this binary runs three concurrent servers:
//
//   - HTTP on :8080 — /healthz liveness probe + /readyz drain-aware
//     readiness probe (HTTP/1.1, no TLS) and the /ws/viewer Local-mode
//     WebSocket transport for viewer events (ADR-0007).
//   - gRPC on :9090 — aegis.v1.Gateway service for Cloud-mode
//     viewers (gRPC-Web in front of this) plus the host's
//     CreateMeeting / EndMeeting / NegotiateWebRTC RPCs.
//
// All servers share one Engine client (the C++ engine on :50051), one
// Session registry, one JWT issuer, and one WebRTC Negotiator — so a
// token issued by CreateMeeting is accepted by JoinAsViewer (gRPC) and
// /ws/viewer (WebSocket) interchangeably, and ICE state is tied to the
// same session lifetime.
//
// A4 wires the full audio pipeline. The factory closure injected into
// internal/grpc as AudioPipelineStart captures the process ctx, the
// Negotiator, and the Engine client so internal/grpc stays free of
// pion and pipeline imports. On NegotiateWebRTC success, the closure
// pulls RTP payloads off negotiator.AudioChan(sid) and forwards them
// to the engine as OpusChunks via Pipeline.WriteRTPPayload — codec
// work lives on the engine side per ADR-0016. Transcript egress is
// fanned out via session.Broadcast to viewers on both transports.
//
// The binary is built via Bazel:
//
//	./tools/bazelisk/bazelisk build //gateway_go/cmd/gateway:gateway
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"

	aegisv1 "github.com/BinHsu/aegis-core/gateway_go/gen/go/aegis/v1"
	"github.com/BinHsu/aegis-core/gateway_go/internal/auth"
	corspolicy "github.com/BinHsu/aegis-core/gateway_go/internal/cors"
	gatewaygrpc "github.com/BinHsu/aegis-core/gateway_go/internal/grpc"
	"github.com/BinHsu/aegis-core/gateway_go/internal/health"
	"github.com/BinHsu/aegis-core/gateway_go/internal/logging"
	"github.com/BinHsu/aegis-core/gateway_go/internal/metrics"
	"github.com/BinHsu/aegis-core/gateway_go/internal/pipeline"
	"github.com/BinHsu/aegis-core/gateway_go/internal/profiling"
	"github.com/BinHsu/aegis-core/gateway_go/internal/session"
	"github.com/BinHsu/aegis-core/gateway_go/internal/token"
	"github.com/BinHsu/aegis-core/gateway_go/internal/tracing"
	aegiswebrtc "github.com/BinHsu/aegis-core/gateway_go/internal/webrtc"
	"github.com/BinHsu/aegis-core/gateway_go/internal/ws"
)

const (
	// Empty host in ":8080" / ":9090" is Go's "bind on all interfaces"
	// spelling (net.Listen unpacks it to 0.0.0.0:8080). This is the
	// Local-mode LAN-viewer requirement per ADR-0007 — explicit here
	// because a maintainer might otherwise "narrow" it to localhost
	// thinking it's a security improvement. It is NOT: the LAN QR-code
	// viewer flow depends on other devices on the LAN being able to
	// reach the Gateway. Cloud mode sits behind an ingress that does
	// the narrowing at the network layer (ADR-0006 / ARCH §5).
	defaultListenAddr = ":8080"
	defaultGRPCAddr   = ":9090"
	// defaultMetricsAddr is the Prometheus pull endpoint per ldz #46
	// §"Q3" (K8s controller-runtime convention: :8080 main + :8081
	// mgmt/metrics companion). Bound via a dedicated http.Server so
	// scrape traffic is isolated from the application HTTP surface.
	defaultMetricsAddr = ":8081"
	defaultEngineAddr  = "localhost:50051"
	version           = "0.1.0-phase2-a4"
	engineRPCTimeout  = 2 * time.Second

	// defaultDrainTimeout is the bound on shutdown. Short enough for a
	// Local-mode developer's Ctrl-C to feel instant; long enough for
	// in-flight viewer streams to drain via GracefulStop. Cloud mode
	// sets AEGIS_GATEWAY_DRAIN_TIMEOUT=14400s to match the
	// terminationGracePeriodSeconds in ADR-0006.
	defaultDrainTimeout = 30 * time.Second

	// keepaliveTime / keepaliveTimeout follow ADR-0006 exactly: a viewer
	// that silently vanishes (laptop lid closed, network cable pulled)
	// is detected within Time + Timeout = 40 s and its fan-out channel
	// is reclaimed. Without this, Go gRPC's 2-hour default keepalive
	// would hold fan-out resources for disconnected viewers.
	keepaliveTime           = 30 * time.Second
	keepaliveTimeout        = 10 * time.Second
	keepaliveMinTime        = 10 * time.Second
)

func main() {
	// Structured logger from env (AEGIS_LOG_FORMAT, AEGIS_LOG_LEVEL).
	// SetDefault makes slog.Info/Warn/Error calls throughout the tree
	// honor the configuration, which matters once internal/pipeline
	// and the factory closure below use the package-level slog logger.
	logger := logging.SetDefault().With("pkg", "gateway")

	// die emits a structured Error record and terminates. We use this
	// rather than log.Fatalf so startup failures appear in the same
	// format as runtime failures — critical for ops tooling that greps
	// for `"level":"ERROR"` to trigger on-call.
	die := func(msg string, args ...any) {
		logger.Error(msg, args...)
		os.Exit(1)
	}

	// OpenTelemetry tracer-provider per ADR-0005 R4 + Phase 4d ROADMAP.
	// Init once early so subsequent grpc.NewServer / NewClient calls
	// can wire the otelgrpc stats handlers against the global provider.
	// DEPLOY_MODE picks the exporter — local/cloud-test → stdout,
	// cloud → OTLP gRPC (the OTEL_EXPORTER_OTLP_ENDPOINT env var
	// flows naturally into otlptracegrpc.New as the SDK's default).
	//
	// Failure to dial the cloud collector at Init time is logged but
	// non-fatal — span emission falls back to dropping silently per
	// the SDK's default. SLO-first observability: dropped traces are
	// degradation, not outage.
	deployMode := tracing.DeployMode(strings.ToLower(strings.TrimSpace(os.Getenv("DEPLOY_MODE"))))
	if deployMode == "" {
		deployMode = tracing.ModeLocal
	}
	tracerShutdown, err := tracing.Init(context.Background(), deployMode)
	if err != nil {
		logger.Warn("tracing.Init failed — gateway runs without OTLP export",
			"deploy_mode", string(deployMode), "err", err.Error())
	} else {
		// Spans now exist on request contexts: swap the bootstrap
		// logger for a trace-aware one so every record carries
		// trace_id / span_id (joinable to its trace in Tempo) plus
		// the pod / node identifiers from the K8s Downward API
		// (AEGIS_POD_NAME / AEGIS_NODE_NAME — empty in Local mode,
		// in which case the fields are simply omitted).
		logger = logging.SetTraceAwareDefault(
			os.Getenv("AEGIS_POD_NAME"), os.Getenv("AEGIS_NODE_NAME"),
		).With("pkg", "gateway")
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := tracerShutdown(ctx); err != nil {
				logger.Warn("tracing.Shutdown", "err", err.Error())
			}
		}()
	}

	// Continuous profiling — the 4th observability signal (ADR-0035).
	// Wired AFTER tracing.Init so profiles and traces share the same
	// service identity, and BEFORE the listeners come up so the
	// flame-graph captures the full process lifetime.
	//
	// Fail-soft, mirroring the tracing posture above: an empty
	// AEGIS_PYROSCOPE_ENDPOINT degrades profiling to a no-op, and a
	// non-empty-but-unreachable endpoint surfaces as a warning here —
	// never fatal. aegis-core ships this before the landing-zone has
	// provisioned Grafana Cloud Pyroscope ingest, so a missing backend
	// must be a degradation, not a startup blocker.
	profiler, err := profiling.Start(profiling.Config{
		ApplicationName: tracing.ServiceName,
		Endpoint:        strings.TrimSpace(os.Getenv("AEGIS_PYROSCOPE_ENDPOINT")),
	})
	if err != nil {
		logger.Warn("profiling.Start failed — gateway runs without continuous profiling",
			"err", err.Error())
	} else {
		defer func() {
			if err := profiler.Stop(); err != nil {
				logger.Warn("profiling.Stop", "err", err.Error())
			}
		}()
	}

	listenAddr := defaultListenAddr
	if env := os.Getenv("AEGIS_GATEWAY_ADDR"); env != "" {
		listenAddr = env
	}
	grpcAddr := defaultGRPCAddr
	if env := os.Getenv("AEGIS_GATEWAY_GRPC_ADDR"); env != "" {
		grpcAddr = env
	}
	// AEGIS_GATEWAY_METRICS_ADDR overrides the default:
	//   unset        → `:8081` (default, enabled)
	//   non-empty    → use the provided addr (e.g., `127.0.0.1:9999`)
	//   explicitly "" → disable the third server entirely; `/metrics`
	//                   is simply unreachable. app_local uses this to
	//                   keep dev-mode bind surface minimal.
	metricsAddr := defaultMetricsAddr
	if env, ok := os.LookupEnv("AEGIS_GATEWAY_METRICS_ADDR"); ok {
		metricsAddr = env
	}
	engineAddr := defaultEngineAddr
	if env := os.Getenv("AEGIS_ENGINE_ADDR"); env != "" {
		engineAddr = env
	}

	// Dial the engine. grpc.NewClient (the 1.64+ preferred form) does
	// NOT block on a live TCP connection — the RPC at call time drives
	// the dial.
	//
	// Keepalive settings match ADR-0006: a dead engine (pod crash,
	// cable pull) is detected within 40 s on any open StreamTranscribe
	// bidi stream and the pipeline errors out instead of hanging
	// forever.
	//
	// WithDefaultServiceConfig(round_robin) is ADR-0017 topology
	// N:N-readiness. When `engineAddr` is a single host:port (Phase 2
	// local demo), round_robin is a no-op — there's one endpoint to
	// pick from. When Phase 4+ Cloud deployment points at a resolver
	// target like `dns:///engine.aegis.svc.cluster.local:50051`
	// (Kubernetes Headless Service), gRPC's DNS resolver expands to
	// N pod IPs and round-robin picks one per new StreamTranscribe
	// stream. Each bidi stream is then automatically session-pinned
	// to the chosen engine for its lifetime (gRPC stream semantics,
	// no extra code). NEVER remove this line to "simplify" — it's a
	// ~free one-time cost that makes horizontal engine scaling a
	// deployment concern, not a code refactor.
	conn, err := grpc.NewClient(
		engineAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                keepaliveTime,
			Timeout:             keepaliveTimeout,
			PermitWithoutStream: true,
		}),
		// otelgrpc stats handler — auto-creates client spans for
		// each engine RPC and injects the W3C `traceparent` header
		// so a parent span on the gateway side stitches into the
		// engine-side span tree (whenever the engine adopts OTel).
		grpc.WithStatsHandler(otelgrpc.NewClientHandler()),
	)
	if err != nil {
		die("grpc.NewClient", "engine_addr", engineAddr, "err", err)
	}
	defer func() { _ = conn.Close() }()
	engine := aegisv1.NewEngineClient(conn)

	// Session registry + JWT issuer. Both are process-scoped: the
	// registry per ADR-0004 "No Shared State Between Replicas", the
	// issuer's signing key per ADR-0001 "process-scoped random key".
	registry := session.NewRegistry()
	issuer, err := token.NewIssuer()
	if err != nil {
		die("token.NewIssuer", "err", err)
	}

	// Signal-driven process context. Shared between all long-lived
	// subsystems: audio pipelines (so they tear down on SIGTERM), the
	// HTTP/gRPC servers' shutdown path, and anything we add later.
	// Creating it up here — rather than right before the <-ctx.Done()
	// wait — lets the pipeline factory closure capture a process-scoped
	// ctx that outlives the per-RPC NegotiateWebRTC context.
	processCtx, stopSignals := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	negotiator := aegiswebrtc.New()

	// Factory closure for the audio pipeline. Called once per
	// successful NegotiateWebRTC. Returns a stop function the gRPC
	// service calls on EndMeeting (or on re-negotiation). Captures
	// processCtx so the pipeline outlives the NegotiateWebRTC RPC
	// context (which cancels as soon as the RPC returns).
	startAudioPipeline := func(sess *session.Session, sessionID string) (func(), error) {
		// Per-pipeline context derived from the process context —
		// cancelled by stop() on EndMeeting, or on process shutdown
		// via processCtx propagation.
		pipeCtx, pipeCancel := context.WithCancel(processCtx)

		p, pErr := pipeline.New(pipeCtx, engine, sess, sessionID)
		if pErr != nil {
			pipeCancel()
			return nil, pErr
		}

		audioCh := negotiator.AudioChan(sessionID)
		iceCh := negotiator.ICEChan(sessionID)

		go func() {
			for {
				select {
				case payload, ok := <-audioCh:
					if !ok {
						// Channel is never closed by the Negotiator
						// (see AudioChan docs); this case exists for
						// symmetry if that contract ever changes.
						return
					}
					if err := p.WriteRTPPayload(payload); err != nil {
						// Single-frame decode/send errors are logged
						// but not fatal — one corrupt Opus packet
						// shouldn't kill an otherwise-healthy session.
						logger.Warn("pipeline write", "session_id", sessionID, "err", err)
					}
				case state, ok := <-iceCh:
					if !ok {
						return
					}
					switch state {
					case aegiswebrtc.ICEStateDisconnected:
						_ = p.SendControl(aegisv1.ControlKind_CONTROL_KIND_PAUSE)
						metrics.HostTransientLossTotal.Inc()
					case aegiswebrtc.ICEStateConnected:
						_ = p.SendControl(aegisv1.ControlKind_CONTROL_KIND_RESUME)
					case aegiswebrtc.ICEStateFailed:
						// Terminal — emit END_STREAM and exit; the
						// engine will flush and close the stream.
						_ = p.SendControl(aegisv1.ControlKind_CONTROL_KIND_END_STREAM)
						return
					}
				case <-pipeCtx.Done():
					return
				}
			}
		}()

		stop := func() {
			pipeCancel() // release the pump goroutine
			p.Close()    // END_STREAM + CloseSend + drain egress
		}
		return stop, nil
	}

	gatewaySvc, err := gatewaygrpc.New(gatewaygrpc.Config{
		Registry:           registry,
		Issuer:             issuer,
		Engine:             engine,
		WebRTCNegotiator:   negotiator,
		AudioPipelineStart: startAudioPipeline,
		EngineProbeTimeout: engineRPCTimeout,
	})
	if err != nil {
		die("gatewaygrpc.New", "err", err)
	}

	// Auth provider: picked by DEPLOY_MODE per ADR-0034 §LOCAL mode
	// posture. One binary, three modes, one env-var switch:
	//
	//   local        → NoOpProvider (synthetic "local" Principal; ADR-0007)
	//   cloud        → OIDCProvider (Cognito JWKS; ADR-0034 §D1)
	//   cloud-test   → StaticJWTProvider (HS256 pre-shared-secret;
	//                  Phase 2 A2 scaffold preserved for integration-
	//                  test scenarios)
	//
	// Empty / unset DEPLOY_MODE defaults to local to preserve Phase 3
	// LAN demo posture. Unrecognised values panic loudly rather than
	// silently degrade to NoOp — a typo like `DEPLOY_MODE=prod` must
	// not accidentally disable auth in a cloud deploy.
	authProvider, err := buildAuthProvider(processCtx)
	if err != nil {
		die("build auth provider", "err", err)
	}

	// gRPC server for aegis.v1.Gateway. Interceptors fire in registration
	// order: auth first (attaches Principal to ctx); future additions
	// (request logging, telemetry, rate limit) slot in after it.
	// Keepalive params follow ADR-0006: a viewer whose stream is idle
	// for `keepaliveTime` triggers a PING; no PONG within
	// `keepaliveTimeout` tears down the connection and reclaims the
	// fan-out channel. EnforcementPolicy.MinTime caps how often clients
	// may send their own pings — without it, a misbehaving client could
	// trip gRPC's "too many pings" disconnect.
	grpcSrv := grpc.NewServer(
		// Interceptor chain — order matters: auth first so Principal
		// is in ctx before the handler runs; metrics second so it
		// sees the final handler error + measures total handler time
		// including any downstream auth-decision work. Chain* allows
		// multiple interceptors; plain UnaryInterceptor/StreamInterceptor
		// only accepts a single function.
		grpc.ChainUnaryInterceptor(
			auth.UnaryInterceptor(authProvider),
			metrics.UnaryInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			auth.StreamInterceptor(authProvider),
			metrics.StreamInterceptor(),
		),
		// otelgrpc stats handler — auto-creates server spans for
		// every inbound RPC. Attribute filtering happens in the
		// tracing exporter via the ADR-0005 R4 allowlist before
		// any payload-shaped key reaches the OTLP wire.
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    keepaliveTime,
			Timeout: keepaliveTimeout,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             keepaliveMinTime,
			PermitWithoutStream: true,
		}),
	)
	aegisv1.RegisterGatewayServer(grpcSrv, gatewaySvc)

	// HTTP server for /healthz, the Local-mode /ws/viewer transport
	// (ADR-0007), AND the Cloud-mode gRPC-Web bridge. The WebSocket
	// endpoint shares the registry + issuer with the gRPC Gateway so
	// a single token works on either transport.
	// Origin allowlist policy: AEGIS_ALLOWED_ORIGINS (comma-separated
	// origins) toggles between Local-mode permissive (default; ADR-0007
	// LAN scope) and Cloud-mode strict allowlist (e.g.
	// "https://aegis-app.staging.binhsu.org" — ADR-0027 split-subdomain
	// frontend deploy). Built once at startup; immutable thereafter.
	originPolicy := corspolicy.New()
	if originPolicy.Permissive() {
		logger.Info("CORS policy: permissive (Local mode default)", "env", corspolicy.EnvVar)
	} else {
		logger.Info("CORS policy: strict allowlist (Cloud mode)", "env", corspolicy.EnvVar)
	}

	// Drain-aware readiness gate (ADR-0006 §Graceful Shutdown). Created
	// NOT ready so /readyz answers 503 in the window between mux wiring
	// and listener bind. Flipped true once all three listeners are
	// confirmed up (see metrics.Up.Set below), and false as the first
	// action of shutdown so the orchestrator stops routing NEW traffic
	// while in-flight requests/streams drain. /healthz stays the
	// unconditional liveness probe; /readyz is the readiness probe.
	readiness := health.NewReadiness()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", makeHealthHandler(engine, engineAddr, registry))
	mux.Handle("/readyz", readiness)
	mux.HandleFunc("/lan-ip", corsAllowed(originPolicy, makeLANIPHandler()))
	mux.HandleFunc("/ws/viewer", ws.Handler(ws.Config{
		Registry: registry,
		Issuer:   issuer,
		Logger:   logger.With("pkg", "ws"),
	}))

	// gRPC-Web wrapper: same grpcSrv, spoken over HTTP/1.1 with
	// grpc-web framing so browsers (which cannot speak native HTTP/2
	// gRPC with trailers) can reach CreateMeeting / JoinAsViewer. The
	// origin allowlist follows the same Policy used for native HTTP
	// CORS (ADR-0027) so Local mode stays permissive and Cloud mode
	// gets the strict subdomain allowlist for free.
	wrappedGrpc := grpcweb.WrapServer(
		grpcSrv,
		grpcweb.WithOriginFunc(originPolicy.Allow),
		grpcweb.WithAllowNonRootResource(true),
	)

	httpSrv := &http.Server{
		Addr: listenAddr,
		// Route order:
		//   1. grpc-web (content-type / Accept sniff) → wrapped server
		//   2. everything else → existing mux (/healthz, /ws/viewer)
		// The sniff-first pattern means grpc-web requests don't need a
		// dedicated URL prefix — gRPC service methods hit paths like
		// /aegis.v1.Gateway/CreateMeeting which don't collide with our
		// native HTTP handlers. IsGrpcWebRequest + the CORS preflight
		// check cover both simple and preflighted browser calls.
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if wrappedGrpc.IsGrpcWebRequest(r) || wrappedGrpc.IsAcceptableGrpcCorsRequest(r) {
				wrappedGrpc.ServeHTTP(w, r)
				return
			}
			mux.ServeHTTP(w, r)
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		die("gRPC listen", "addr", grpcAddr, "err", err)
	}

	// Third HTTP server — Prometheus pull endpoint. Serves exactly
	// /metrics + nothing else; NotFound on any other path. Isolated
	// from the application HTTP surface on :8080 so scrape traffic
	// (which can be frequent under ServiceMonitor) cannot interfere
	// with /healthz probes or /ws/viewer streams.
	//
	// When metricsAddr is empty (explicit "" via env), metricsSrv
	// stays nil and the goroutine + shutdown paths below short-
	// circuit. /metrics is simply unreachable in that mode.
	var metricsSrv *http.Server
	if metricsAddr != "" {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", metrics.Handler())
		metricsSrv = &http.Server{
			Addr:              metricsAddr,
			Handler:           metricsMux,
			ReadHeaderTimeout: 5 * time.Second,
		}
	}

	// Signal handling is rooted at processCtx (declared above). Pipelines
	// observe processCtx cancellation through their pipeCtx parent; this
	// goroutine still waits on <-processCtx.Done() for the shutdown dance.
	// ADR-0006's drain path opens with the readiness.SetReady(false) flip
	// at the top of the shutdown block below; the
	// terminationGracePeriodSeconds=14400 budget bounds the drain via
	// AEGIS_GATEWAY_DRAIN_TIMEOUT.

	go func() {
		logger.Info("HTTP server listening",
			"addr", listenAddr,
			"endpoints", "/healthz,/readyz,/ws/viewer,grpc-web",
			"engine_addr", engineAddr,
			"version", version,
		)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			die("HTTP ListenAndServe", "err", err)
		}
	}()

	go func() {
		logger.Info("gRPC server listening", "addr", grpcAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			die("gRPC Serve", "err", err)
		}
	}()

	if metricsSrv != nil {
		go func() {
			logger.Info("metrics server listening", "addr", metricsAddr, "path", "/metrics")
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				die("metrics ListenAndServe", "err", err)
			}
		}()
	} else {
		logger.Info("metrics disabled (AEGIS_GATEWAY_METRICS_ADDR set to empty)")
	}

	// Signal readiness to scrapers once all three listeners are up.
	// Set exactly once here; clearing on shutdown isn't load-bearing
	// because the metrics server stops alongside everything else.
	metrics.Up.Set(1.0)

	// All three listeners are confirmed up — open the /readyz gate so
	// the orchestrator starts routing traffic to this pod.
	readiness.SetReady(true)

	// Publish registry.Len() into the active_sessions gauge on a
	// short poll. The registry has no change-notification channel,
	// so polling is the honest minimum wiring. Frequency is bounded
	// at "often enough to be useful for dashboards, infrequent enough
	// to be a rounding-error CPU cost" — 5 seconds satisfies both.
	sessionPollCtx, stopSessionPoll := context.WithCancel(processCtx)
	defer stopSessionPoll()
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				metrics.ActiveSessions.Set(float64(registry.Len()))
			case <-sessionPollCtx.Done():
				return
			}
		}
	}()

	<-processCtx.Done()
	logger.Info("shutdown signal received; draining")

	// First action of shutdown: close the /readyz gate so it answers
	// 503. This opens the drain window BEFORE the servers stop
	// accepting — the orchestrator marks the pod NotReady and routes
	// new traffic to other replicas while in-flight requests/streams
	// below finish (ADR-0006 §Graceful Shutdown).
	readiness.SetReady(false)

	drainTimeout := defaultDrainTimeout
	if env := os.Getenv("AEGIS_GATEWAY_DRAIN_TIMEOUT"); env != "" {
		if d, err := time.ParseDuration(env); err == nil && d > 0 {
			drainTimeout = d
		} else {
			logger.Warn("invalid AEGIS_GATEWAY_DRAIN_TIMEOUT; using default",
				"given", env, "default", defaultDrainTimeout)
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainTimeout)
	defer cancel()

	// Shut down HTTP first (fast) — the healthz probe and /ws/viewer
	// paths don't participate in long-running sessions; their shutdown
	// is bounded by shutdownCtx.
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		logger.Warn("HTTP shutdown error", "err", err)
	}

	// Metrics server is Prometheus-scrape-only; Shutdown is fast.
	// Shares the same shutdownCtx deadline as the main HTTP server.
	if metricsSrv != nil {
		if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("metrics shutdown error", "err", err)
		}
	}

	// gRPC GracefulStop waits for in-flight streams to finish, which
	// for a long-lived viewer can mean "until the client disconnects".
	// Race it against shutdownCtx so an impolite viewer can't block
	// shutdown beyond the drain deadline — the forced Stop() fallback
	// is how ADR-0006's 14400 s terminationGracePeriodSeconds stays a
	// hard upper bound and not a lower bound.
	stopped := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-shutdownCtx.Done():
		logger.Warn("drain deadline exceeded; forcing Stop", "drain_timeout", drainTimeout)
		grpcSrv.Stop()
		<-stopped
	}
	logger.Info("bye")
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

// lanIPResponse is the /lan-ip JSON shape — consumed by HostPage to
// build a LAN-reachable QR code URL for viewers scanning from a
// phone. `best` is the first-usable candidate; `candidates` is the
// full list in case the host's machine has multiple LAN interfaces
// (Wi-Fi + wired, or multiple docks) and the first guess is wrong.
type lanIPResponse struct {
	Best       string   `json:"best"`
	Candidates []string `json:"candidates"`
}

// makeLANIPHandler introspects the host's network interfaces for
// non-loopback IPv4 addresses — the addresses a LAN-local viewer's
// phone can reach. Browser JS cannot query this directly (same-
// origin + no local-network API); the gateway is the authoritative
// source because it's the thing binding on 0.0.0.0:8080 that the
// phone would connect to.
//
// Scope (all deliberate):
//   - IPv4 only. Most consumer Wi-Fi routers don't serve IPv6 on the
//     LAN segment, and QR codes with v6 addresses encode poorly at the
//     scannable-from-a-foot-away density.
//   - Skip loopback (127.0.0.1), link-local (169.254.0.0/16 APIPA),
//     and interfaces that are administratively down.
func makeLANIPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		candidates := detectLANIPv4s()
		resp := lanIPResponse{Candidates: candidates}
		if len(candidates) > 0 {
			resp.Best = candidates[0]
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = writeJSON(w, resp)
	}
}

func detectLANIPv4s() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			ip4 := ip.To4()
			if ip4 == nil {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out
}

// corsPermissive wraps a handler with permissive CORS headers — lets
// the Vite dev server (cross-origin at http://*:5173) fetch
// /lan-ip from the gateway at :8080 without preflight surprises.
// corsAllowed wraps a handler with CORS headers driven by a Policy.
//
// Permissive policy (Local mode) → `Access-Control-Allow-Origin: *`
// (preserves pre-Slice-5 behaviour exactly).
//
// Strict policy (Cloud mode) → echoes the request's Origin header back
// when the Policy allows it (with `Vary: Origin` so caches don't mix
// responses), or omits the header entirely when the origin is denied
// (browser sees opaque "CORS error", which is the correct denial signal
// — distinct from "no CORS configured at all").
func corsAllowed(p *corspolicy.Policy, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		switch {
		case p.Permissive():
			w.Header().Set("Access-Control-Allow-Origin", "*")
		case p.Allow(origin):
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "content-type")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

// buildAuthProvider selects the auth.Provider implementation at startup
// based on the DEPLOY_MODE env var, per ADR-0034 §LOCAL mode posture.
//
//	""     / "local" → NoOpProvider  (synthetic "local" Principal; ADR-0007)
//	"cloud"          → OIDCProvider   (Cognito JWKS + claim mapping; ADR-0034 §D1)
//	"cloud-test"     → StaticJWTProvider (HS256 pre-shared-secret; Phase 2 A2 scaffold)
//
// Unrecognised DEPLOY_MODE values return an error that causes main to
// die() — a typo like `DEPLOY_MODE=prod` must not silently fall through
// to NoOp in a cloud deploy.
//
// Required env vars per mode:
//
//	local        — (none)
//	cloud        — AEGIS_COGNITO_ISSUER, AEGIS_COGNITO_AUDIENCE;
//	               optional AEGIS_COGNITO_JWKS_URL (default derived from issuer).
//	cloud-test   — AEGIS_JWT_STATIC_SECRET; optional AEGIS_JWT_STATIC_AUDIENCE.
func buildAuthProvider(ctx context.Context) (auth.Provider, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("DEPLOY_MODE")))
	switch mode {
	case "", "local":
		return auth.NoOpProvider{}, nil

	case "cloud":
		issuer := os.Getenv("AEGIS_COGNITO_ISSUER")
		audience := os.Getenv("AEGIS_COGNITO_AUDIENCE")
		if issuer == "" || audience == "" {
			return nil, fmt.Errorf("DEPLOY_MODE=cloud requires AEGIS_COGNITO_ISSUER and AEGIS_COGNITO_AUDIENCE")
		}
		return auth.NewOIDCProvider(ctx, auth.OIDCConfig{
			Issuer:   issuer,
			Audience: audience,
			JWKSURL:  os.Getenv("AEGIS_COGNITO_JWKS_URL"),
		})

	case "cloud-test":
		secret := os.Getenv("AEGIS_JWT_STATIC_SECRET")
		if secret == "" {
			return nil, errors.New("DEPLOY_MODE=cloud-test requires AEGIS_JWT_STATIC_SECRET")
		}
		return auth.StaticJWTProvider{
			Secret:           []byte(secret),
			ExpectedAudience: os.Getenv("AEGIS_JWT_STATIC_AUDIENCE"),
		}, nil

	default:
		return nil, fmt.Errorf("unrecognized DEPLOY_MODE %q (valid: local, cloud, cloud-test)", mode)
	}
}
