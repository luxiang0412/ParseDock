// Command docparse is the single ParseDock binary. One code path, four roles:
//
//	docparse --role standalone    API + Coordinator + one local worker
//	docparse --role coordinator   the only stateful process (SQLite + files)
//	docparse --role api           stateless gateway replicas
//	docparse --role worker        CPU/GPU parse workers
//
// See docs/ARCHITECTURE.md for the topology and docs/DECISIONS.md for the
// single-binary ADR.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"parsedock.dev/parsedock/internal/api"
	"parsedock.dev/parsedock/internal/coordinator"
	"parsedock.dev/parsedock/internal/engines"
	"parsedock.dev/parsedock/internal/jobs"
	"parsedock.dev/parsedock/internal/storage"
	"parsedock.dev/parsedock/internal/sysinfo"
	"parsedock.dev/parsedock/internal/tlsutil"
	"parsedock.dev/parsedock/internal/worker"
)

// serviceVersion is the released artefact version; apiVersion is the OpenAPI document
// version. They move together, and scripts/check_versions.py enforces that.
const (
	serviceVersion = "0.1.2"
	apiVersion     = "0.1.2"
)

type flags struct {
	role             string
	listen           string
	internalListen   string
	coordinatorURL   string
	dataDir          string
	dbPath           string
	workDir          string
	internalToken    string
	workerToken      string
	trustedProxies   string
	bodyReadTimeout  time.Duration
	apiKeys          string
	apiKeysFile      string
	quotaConcurrent  int
	quotaJobsPerDay  int
	quotaBytesPerDay int64
	quotaStorageMB   int64
	maxExpandedMB    int64
	devInsecure      bool
	logLevel         string
	engines          string
	sandbox          string

	ttl             time.Duration
	maxTTL          time.Duration
	maxLifetime     time.Duration
	failedRetention time.Duration
	cleanupInterval time.Duration
	downloadLease   time.Duration
	workerLease     time.Duration
	heartbeat       time.Duration
	tombstone       time.Duration
	maxAttempts     int
	maxUploadMB     int64
	maxPages        int
	minFreeGB       int64
	rateLimit       float64
	rateBurst       int

	workerID          string
	workerConcurrency int
	deviceType        string
	maxParseMinutes   int
	workMaxAge        time.Duration
	workScrubInterval time.Duration

	xbergBin         string
	xbergLibDir      string
	xbergOCR         bool
	xbergOCRLang     string
	xbergOCRBackend  string
	xbergTimeout     time.Duration
	doclingRSTimeout time.Duration
	mineruTimeout    time.Duration
	engineMaxRSSMB   int64
	escalateScans    bool
	pollInterval     time.Duration
	version          bool
	engineMinVRAM    string
	tlsCert          string
	tlsKey           string
	tlsClientCA      string
	tlsCA            string
	tlsServerName    string
	tlsInsecure      bool
	doclingRSBin     string
	doclingRSModel   string
	doclingRSText    bool
	doclingServe     string
	doclingServeGPU  bool
	tikaURL          string
	mineruBin        string
	mineruTier       string
	mineruHome       string

	check bool
}

func main() {
	f, err := parseFlags()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(2)
	}
	if err := run(f); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// parseFlags reads flags with PARSEDOCK_* environment defaults. A malformed numeric or
// duration environment value is a startup error: silently falling back to the default
// would run a deployment with limits nobody configured.
func parseFlags() (*flags, error) {
	envErrs = nil
	f := &flags{}
	fs := flag.NewFlagSet("docparse", flag.ExitOnError)
	fs.StringVar(&f.role, "role", env("PARSEDOCK_ROLE", "standalone"), "standalone|coordinator|api|worker")
	fs.StringVar(&f.listen, "listen", env("PARSEDOCK_LISTEN", "127.0.0.1:8080"), "public API listen address")
	fs.StringVar(&f.internalListen, "internal-listen", env("PARSEDOCK_INTERNAL_LISTEN", "127.0.0.1:8081"), "coordinator internal listen address")
	fs.StringVar(&f.coordinatorURL, "coordinator-url", env("PARSEDOCK_COORDINATOR_URL", "http://127.0.0.1:8081"), "coordinator base URL")
	fs.StringVar(&f.dataDir, "data-dir", env("PARSEDOCK_DATA_DIR", "./runtime/data"), "data directory (coordinator only)")
	fs.StringVar(&f.dbPath, "db", env("PARSEDOCK_DB", ""), "sqlite path (default <data-dir>/coordinator.db)")
	fs.StringVar(&f.workDir, "work-dir", env("PARSEDOCK_WORK_DIR", "./runtime/work"), "worker scratch directory")
	fs.DurationVar(&f.pollInterval, "poll-interval", durEnv("PARSEDOCK_POLL_INTERVAL", 250*time.Millisecond),
		"how long an idle worker waits between claim attempts; this is the latency floor when the queue is shallow")
	fs.StringVar(&f.internalToken, "internal-token", env("PARSEDOCK_INTERNAL_TOKEN", ""),
		"gateway<->coordinator token; the only credential that may assert an owner (generated in-process for standalone)")
	fs.StringVar(&f.workerToken, "worker-token", env("PARSEDOCK_WORKER_TOKEN", ""),
		"separate worker credential (coordinator + workers); when set, workers cannot call /v1 or operator endpoints (generated in-process for standalone)")
	fs.StringVar(&f.trustedProxies, "trusted-proxies", env("PARSEDOCK_TRUSTED_PROXIES", ""),
		"api: comma list of CIDRs/addresses whose X-Forwarded-For is believed (default none: the TCP peer is the client)")
	fs.DurationVar(&f.bodyReadTimeout, "body-read-timeout", durEnv("PARSEDOCK_BODY_READ_TIMEOUT", api.DefaultBodyReadTimeout),
		"api/coordinator: max time to receive one request body (bodies <= 1 MiB: at most 1m); 0 disables")
	fs.StringVar(&f.apiKeys, "api-keys", env("PARSEDOCK_API_KEYS", ""), "comma list <token:owner[:scopes]> or sha256:<hex>:owner[:scopes]")
	fs.StringVar(&f.apiKeysFile, "api-keys-file", env("PARSEDOCK_API_KEYS_FILE", ""), "file with one key entry per line (preferred: argv is world-readable)")
	fs.IntVar(&f.quotaConcurrent, "quota-concurrent", intEnv("PARSEDOCK_QUOTA_CONCURRENT", 0), "max queued+running jobs per owner (0 = unlimited)")
	fs.IntVar(&f.quotaJobsPerDay, "quota-jobs-per-day", intEnv("PARSEDOCK_QUOTA_JOBS_PER_DAY", 0), "max jobs per owner per rolling 24h (0 = unlimited)")
	fs.Int64Var(&f.quotaBytesPerDay, "quota-bytes-per-day", int64Env("PARSEDOCK_QUOTA_BYTES_PER_DAY", 0), "max input bytes per owner per rolling 24h (0 = unlimited)")
	fs.Int64Var(&f.quotaStorageMB, "quota-storage-mb", int64Env("PARSEDOCK_QUOTA_STORAGE_MB", 0), "max live result bytes per owner (0 = unlimited)")
	fs.Int64Var(&f.maxExpandedMB, "max-expanded-mb", int64Env("PARSEDOCK_MAX_EXPANDED_MB", 512), "OOXML expanded-size cap (xberg itself caps at 500 MiB; docling-rs at 10M cells per sheet)")
	fs.BoolVar(&f.devInsecure, "dev-insecure", env("PARSEDOCK_DEV_INSECURE", "") == "1", "allow anonymous access (tests only)")
	fs.StringVar(&f.logLevel, "log-level", env("PARSEDOCK_LOG_LEVEL", "info"), "debug|info|warn|error")
	fs.StringVar(&f.engines, "engines", env("PARSEDOCK_ENGINES", "native"), "worker engines (comma list)")
	fs.StringVar(&f.sandbox, "sandbox", env("PARSEDOCK_SANDBOX", ""), "none|unshare-net")

	fs.DurationVar(&f.ttl, "result-ttl", durEnv("PARSEDOCK_RESULT_TTL", time.Hour), "default result TTL")
	fs.DurationVar(&f.maxTTL, "max-result-ttl", durEnv("PARSEDOCK_MAX_RESULT_TTL", 24*time.Hour), "client TTL ceiling")
	fs.DurationVar(&f.maxLifetime, "max-job-lifetime", durEnv("PARSEDOCK_MAX_JOB_LIFETIME", 24*time.Hour), "absolute job lifetime")
	fs.DurationVar(&f.failedRetention, "failed-retention", durEnv("PARSEDOCK_FAILED_RETENTION", 10*time.Minute), "failed job artifact retention")
	fs.DurationVar(&f.cleanupInterval, "cleanup-interval", durEnv("PARSEDOCK_CLEANUP_INTERVAL", 60*time.Second), "cleanup period")
	fs.DurationVar(&f.downloadLease, "download-lease", durEnv("PARSEDOCK_DOWNLOAD_LEASE", 60*time.Second), "download lease TTL")
	fs.DurationVar(&f.workerLease, "worker-lease", durEnv("PARSEDOCK_WORKER_LEASE", 60*time.Second), "worker lease TTL")
	fs.DurationVar(&f.heartbeat, "heartbeat", durEnv("PARSEDOCK_HEARTBEAT", 10*time.Second), "worker heartbeat interval")
	fs.DurationVar(&f.tombstone, "tombstone-ttl", durEnv("PARSEDOCK_TOMBSTONE_TTL", 10*time.Minute), "tombstone retention")
	fs.IntVar(&f.maxAttempts, "max-attempts", intEnv("PARSEDOCK_MAX_ATTEMPTS", 3), "per-job attempt limit")
	fs.Int64Var(&f.maxUploadMB, "max-upload-mb", int64Env("PARSEDOCK_MAX_UPLOAD_MB", 64), "upload size limit (MiB)")
	fs.IntVar(&f.maxPages, "max-pages", intEnv("PARSEDOCK_MAX_PAGES", 2000),
		"PDF page limit; the pre-flight count is a heuristic, so this bounds work rather than guaranteeing a precise cut")
	fs.Int64Var(&f.minFreeGB, "min-free-gb", int64Env("PARSEDOCK_MIN_FREE_GB", 2), "disk safety watermark (GiB)")
	fs.Float64Var(&f.rateLimit, "rate-limit", floatEnv("PARSEDOCK_RATE_LIMIT", 20), "per-owner requests/second, per API replica (0 disables)")
	fs.IntVar(&f.rateBurst, "rate-burst", intEnv("PARSEDOCK_RATE_BURST", 40), "per-owner burst, per API replica")

	fs.StringVar(&f.workerID, "worker-id", env("PARSEDOCK_WORKER_ID", ""), "worker id (generated when empty)")
	fs.IntVar(&f.workerConcurrency, "worker-concurrency", intEnv("PARSEDOCK_WORKER_CONCURRENCY", 1), "concurrent tasks per worker")
	fs.StringVar(&f.deviceType, "device-type", env("PARSEDOCK_DEVICE_TYPE", "cpu"), "cpu|gpu")
	fs.IntVar(&f.maxParseMinutes, "max-parse-minutes", intEnv("PARSEDOCK_MAX_PARSE_MINUTES", 30), "engine time budget")
	fs.DurationVar(&f.workMaxAge, "work-max-age", durEnv("PARSEDOCK_WORK_MAX_AGE", time.Hour), "remove crashed attempt dirs older than this")
	fs.DurationVar(&f.workScrubInterval, "work-scrub-interval", durEnv("PARSEDOCK_WORK_SCRUB_INTERVAL", 10*time.Minute), "periodic scratch sweep interval")

	fs.StringVar(&f.xbergBin, "xberg-bin", env("PARSEDOCK_XBERG_BIN", ""), "path to xberg CLI")
	fs.StringVar(&f.xbergLibDir, "xberg-lib-dir", env("PARSEDOCK_XBERG_LIB_DIR", ""), "dir with xberg shared libs")
	fs.BoolVar(&f.xbergOCR, "xberg-ocr", env("PARSEDOCK_XBERG_OCR", "1") == "1", "enable OCR for xberg")
	fs.StringVar(&f.xbergOCRLang, "xberg-ocr-lang", env("PARSEDOCK_XBERG_OCR_LANG", "eng"), "OCR language (ISO 639-3 for tesseract, e.g. eng/chi_sim; xberg also accepts en/ch)")
	fs.StringVar(&f.xbergOCRBackend, "xberg-ocr-backend", env("PARSEDOCK_XBERG_OCR_BACKEND", ""), "OCR backend for xberg: unset/tesseract (default) or paddle-ocr (PP-OCRv6, better for Chinese)")
	fs.DurationVar(&f.xbergTimeout, "xberg-timeout", durEnv("PARSEDOCK_XBERG_TIMEOUT", 10*time.Minute), "per-document xberg ceiling (capped by --max-parse-minutes)")
	fs.DurationVar(&f.doclingRSTimeout, "docling-rs-timeout", durEnv("PARSEDOCK_DOCLING_RS_TIMEOUT", 15*time.Minute), "per-document docling-rs ceiling (capped by --max-parse-minutes)")
	fs.DurationVar(&f.mineruTimeout, "mineru-timeout", durEnv("PARSEDOCK_MINERU_TIMEOUT", 30*time.Minute), "per-document MinerU ceiling (capped by --max-parse-minutes)")
	fs.StringVar(&f.tlsCert, "tls-cert", env("PARSEDOCK_TLS_CERT", ""), "server certificate (PEM) for the coordinator listener")
	fs.StringVar(&f.tlsKey, "tls-key", env("PARSEDOCK_TLS_KEY", ""), "server private key (PEM)")
	fs.StringVar(&f.tlsClientCA, "tls-client-ca", env("PARSEDOCK_TLS_CLIENT_CA", ""), "CA that must sign client certificates (enables mTLS)")
	fs.StringVar(&f.tlsCA, "tls-ca", env("PARSEDOCK_TLS_CA", ""), "CA to verify the coordinator certificate (api/worker)")
	fs.StringVar(&f.tlsServerName, "tls-server-name", env("PARSEDOCK_TLS_SERVER_NAME", ""), "expected server name in the certificate")
	fs.BoolVar(&f.tlsInsecure, "tls-insecure", env("PARSEDOCK_TLS_INSECURE", "") == "1", "skip server certificate verification (development only)")
	fs.StringVar(&f.engineMinVRAM, "engine-min-vram-mb", env("PARSEDOCK_ENGINE_MIN_VRAM_MB", ""),
		"per-engine free VRAM requirement for gpu_required jobs, e.g. mineru:8192,docling-rs:4096")
	fs.BoolVar(&f.escalateScans, "escalate-scans", env("PARSEDOCK_ESCALATE_SCANS", "1") == "1", "retry a CJK scan once with the accurate engine when the fast engine warns about OCR quality")
	fs.Int64Var(&f.engineMaxRSSMB, "engine-max-rss-mb", int64Env("PARSEDOCK_ENGINE_MAX_RSS_MB", 0), "kill an engine process above this RSS (0 = unlimited; cgroups are the deployment-level limit)")
	fs.StringVar(&f.doclingRSBin, "docling-rs-bin", env("PARSEDOCK_DOCLING_RS_BIN", ""), "path to docling-rs CLI")
	fs.StringVar(&f.doclingRSModel, "docling-rs-models", env("PARSEDOCK_DOCLING_RS_MODELS", ""), "dir containing .models")
	fs.BoolVar(&f.doclingRSText, "docling-rs-text-layer", env("PARSEDOCK_DOCLING_RS_TEXT_LAYER", "") == "1", "docling-rs text-layer-only mode")
	fs.StringVar(&f.doclingServe, "docling-serve-url", env("PARSEDOCK_DOCLING_SERVE_URL", ""), "docling-serve base URL")
	fs.BoolVar(&f.doclingServeGPU, "docling-serve-gpu", env("PARSEDOCK_DOCLING_SERVE_GPU", "") == "1", "mark docling-serve as GPU-capable")
	fs.StringVar(&f.tikaURL, "tika-url", env("PARSEDOCK_TIKA_URL", ""), "tika server base URL")
	fs.StringVar(&f.mineruBin, "mineru-bin", env("PARSEDOCK_MINERU_BIN", ""), "path to mineru CLI")
	fs.StringVar(&f.mineruTier, "mineru-tier", env("PARSEDOCK_MINERU_TIER", "flash"), "mineru tier (flash = local only)")
	fs.StringVar(&f.mineruHome, "mineru-home", env("PARSEDOCK_MINERU_HOME", ""), "ephemeral MINERU_HOME")

	fs.BoolVar(&f.check, "check", false, "print capabilities and exit")
	fs.BoolVar(&f.version, "version", false, "print the service and API versions as JSON and exit")
	_ = fs.Parse(os.Args[1:])
	if len(envErrs) > 0 {
		return nil, fmt.Errorf("invalid environment: %w", errors.Join(envErrs...))
	}
	// A short --worker-lease (the lifecycle tests use 5 s) with the default heartbeat would
	// let every lease expire between renewals. Only an explicitly configured heartbeat is
	// held to the rule in validate(); the default follows the lease.
	heartbeatSet := os.Getenv("PARSEDOCK_HEARTBEAT") != ""
	fs.Visit(func(fl *flag.Flag) {
		if fl.Name == "heartbeat" {
			heartbeatSet = true
		}
	})
	if !heartbeatSet && f.workerLease > 0 && f.heartbeat >= f.workerLease {
		f.heartbeat = f.workerLease / 3
	}
	return f, nil
}

// validate rejects flag combinations that would only fail later (or never visibly).
func (f *flags) validate() error {
	if f.maxUploadMB <= 0 {
		return fmt.Errorf("--max-upload-mb must be > 0, got %d", f.maxUploadMB)
	}
	if f.maxExpandedMB <= 0 {
		return fmt.Errorf("--max-expanded-mb must be > 0, got %d", f.maxExpandedMB)
	}
	if f.maxPages <= 0 {
		return fmt.Errorf("--max-pages must be > 0, got %d", f.maxPages)
	}
	if f.heartbeat <= 0 {
		return fmt.Errorf("--heartbeat must be > 0, got %s", f.heartbeat)
	}
	// The worker lease is the coordinator's setting; a worker cannot see it.
	if (f.role == "coordinator" || f.role == "standalone") && f.heartbeat >= f.workerLease {
		return fmt.Errorf("--heartbeat (%s) must be shorter than --worker-lease (%s), or every lease expires between renewals",
			f.heartbeat, f.workerLease)
	}
	if f.rateLimit < 0 {
		return fmt.Errorf("--rate-limit must be >= 0 (0 disables it), got %v", f.rateLimit)
	}
	if f.bodyReadTimeout < 0 {
		return fmt.Errorf("--body-read-timeout must be >= 0 (0 disables it), got %s", f.bodyReadTimeout)
	}
	if _, err := api.ParseTrustedProxies(f.trustedProxies); err != nil {
		return err
	}
	// A worker may hold only the worker token; the pair is the coordinator's concern.
	if f.role == "coordinator" || f.role == "standalone" {
		return coordinator.ValidateTokens(f.internalToken, f.workerToken)
	}
	return nil
}

// workerCredential is what a worker presents: the worker token when the deployment
// has split the credentials, otherwise the shared internal token.
func (f *flags) workerCredential() string {
	if f.workerToken != "" {
		return f.workerToken
	}
	return f.internalToken
}

func run(f *flags) error {
	level := slog.LevelInfo
	switch strings.ToLower(f.logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	cfg := coordinator.DefaultConfig()
	cfg.ResultTTL = f.ttl
	cfg.MaxResultTTL = f.maxTTL
	cfg.MaxJobLifetime = f.maxLifetime
	cfg.FailedRetention = f.failedRetention
	cfg.CleanupInterval = f.cleanupInterval
	cfg.DownloadLeaseTTL = f.downloadLease
	cfg.WorkerLeaseTTL = f.workerLease
	cfg.HeartbeatInterval = f.heartbeat
	cfg.TombstoneTTL = f.tombstone
	cfg.MaxAttempts = f.maxAttempts
	cfg.MaxUploadBytes = f.maxUploadMB << 20
	cfg.Limits = storage.Limits{
		MaxBytes:           f.maxUploadMB << 20,
		MaxPages:           f.maxPages,
		MaxZipUncompressed: f.maxExpandedMB << 20,
		MaxZipRatio:        200,
		MaxZipEntries:      5000,
	}
	cfg.Engines = engineSpecs(f)
	cfg.Quota = coordinator.Quota{
		MaxConcurrent:   f.quotaConcurrent,
		MaxJobsPerDay:   f.quotaJobsPerDay,
		MaxBytesPerDay:  f.quotaBytesPerDay,
		MaxStorageBytes: f.quotaStorageMB << 20,
	}

	if f.check {
		return printCheck(f, cfg)
	}
	if f.version {
		// Machine-readable so scripts/check_versions.py can compare it with the OpenAPI
		// document and both SDKs without parsing log text.
		fmt.Printf("{\"version\":\"%s\",\"api\":\"%s\"}\n", serviceVersion, apiVersion)
		return nil
	}

	// Fail at startup rather than on the first scanned document: an unknown OCR
	// backend is a deployment typo, and xberg would only report it per request.
	if err := engines.ValidateOCRBackend(f.xbergOCRBackend); err != nil {
		return err
	}
	if f.role == "standalone" {
		if err := ensureStandaloneTokens(f, logger); err != nil {
			return err
		}
	}
	if err := f.validate(); err != nil {
		return err
	}
	if f.devInsecure {
		logger.Warn("DEV-INSECURE MODE: anonymous /v1 access is allowed when no API keys are configured, " +
			"short API keys are accepted and the internal-listener binding check is off. Never run this in production.")
	}
	if f.tlsInsecure {
		logger.Warn("--tls-insecure: the coordinator certificate is NOT verified; any host on the path can impersonate it. Development only.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch f.role {
	case "coordinator":
		return runCoordinator(ctx, f, cfg, logger, nil)
	case "standalone":
		return runStandalone(ctx, f, cfg, logger)
	case "api":
		return runAPI(ctx, f, logger)
	case "worker":
		return runWorker(ctx, f, logger)
	default:
		return fmt.Errorf("unknown role %q", f.role)
	}
}

func runCoordinator(ctx context.Context, f *flags, cfg coordinator.Config, log *slog.Logger, ready chan<- struct{}) error {
	if err := coordinator.ValidateInternalBinding(f.internalListen, f.internalToken, f.devInsecure); err != nil {
		return err
	}
	if err := os.MkdirAll(f.dataDir, 0o750); err != nil {
		return err
	}
	dbPath := f.dbPath
	if dbPath == "" {
		dbPath = filepath.Join(f.dataDir, "coordinator.db")
	}
	db, err := coordinator.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := coordinator.Migrate(db); err != nil {
		return err
	}
	store := &storage.Store{Root: f.dataDir, MinFree: f.minFreeGB << 30}
	if err := store.Init(); err != nil {
		return err
	}
	svc := coordinator.NewService(db, store, cfg)
	// Audit lines are structured JSON on stdout (one line per decision); they
	// never contain document text or tokens. Forward them to the process log so
	// a collector can ship them separately by `audit=true`.
	svc.Audit = log
	srv := coordinator.NewServer(svc, f.internalToken, log)
	srv.WorkerToken = f.workerToken
	if f.workerToken == "" {
		log.Warn("no --worker-token: workers share the gateway's internal token, so a compromised worker host " +
			"can act as any owner on /v1. Set PARSEDOCK_WORKER_TOKEN on the coordinator and the workers.")
	}

	cleanupCtx, stopCleanup := context.WithCancel(ctx)
	defer stopCleanup()
	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		svc.StartCleanup(cleanupCtx)
	}()

	mux := http.NewServeMux()
	mux.Handle("/internal/", srv.InternalMux())
	// /v1 must be served here: it is the gateway's upstream. It accepts only the
	// internal (gateway) token, never the worker token.
	mux.Handle("/v1/", srv.PublicMux())
	mux.Handle("/healthz", srv.PublicMux())
	mux.Handle("/readyz", srv.PublicMux())

	httpSrv := &http.Server{
		Addr: f.internalListen,
		// Uploads arrive through the gateway and results from workers (up to 256 MiB):
		// bound how long one body may take.
		Handler:           api.BodyReadDeadline(mux, f.bodyReadTimeout),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: result downloads and SSE are long-lived.
	}
	if f.tlsCert != "" || f.tlsKey != "" || f.tlsClientCA != "" {
		if f.tlsCert == "" || f.tlsKey == "" {
			return errors.New("--tls-client-ca requires --tls-cert and --tls-key: refusing to start a plaintext listener that would silently drop mutual TLS")
		}
		serverTLS, terr := tlsutil.ServerConfig(f.tlsCert, f.tlsKey, f.tlsClientCA)
		if terr != nil {
			return terr
		}
		httpSrv.TLSConfig = serverTLS
	}
	log.Info("coordinator listening", "addr", f.internalListen, "db", dbPath, "config", cfg.String())
	if ready != nil {
		close(ready)
	}
	err = serve(ctx, httpSrv, log)
	// Let the cleaner finish its pass before the deferred db.Close.
	stopCleanup()
	select {
	case <-cleanupDone:
	case <-time.After(10 * time.Second):
		log.Warn("cleanup pass still running at shutdown; closing the database anyway")
	}
	return err
}

func runAPI(ctx context.Context, f *flags, log *slog.Logger) error {
	return runGateway(ctx, f, f.coordinatorBaseURL(), log)
}

// runGateway builds and serves the API gateway (api role and standalone).
func runGateway(ctx context.Context, f *flags, coordinatorURL string, log *slog.Logger) error {
	keys, err := buildAPIKeys(f, log)
	if err != nil {
		return err
	}
	clientTLS, err := tlsutil.ClientConfig(f.tlsCA, f.tlsCert, f.tlsKey, f.tlsServerName, f.tlsInsecure)
	if err != nil {
		return err
	}
	trusted, err := api.ParseTrustedProxies(f.trustedProxies)
	if err != nil {
		return err
	}
	bodyTimeout := f.bodyReadTimeout
	if bodyTimeout == 0 {
		bodyTimeout = -1 // flag 0 = disabled; api.Config 0 = default
	}
	gw, err := api.New(api.Config{
		CoordinatorURL:  coordinatorURL,
		InternalToken:   f.internalToken,
		Keys:            keys,
		DevInsecure:     f.devInsecure,
		RateLimitRPS:    f.rateLimit,
		RateBurst:       f.rateBurst,
		MaxUploadBytes:  f.maxUploadMB << 20,
		TLS:             clientTLS,
		Log:             log,
		BodyReadTimeout: bodyTimeout,
		TrustedProxies:  trusted,
	})
	if err != nil {
		return err
	}
	go gw.FlushThrottle(ctx)
	srv := &http.Server{
		Addr:              f.listen,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: result downloads and SSE are long-lived; uploads are bounded
		// by the per-request body deadline instead.
	}
	log.Info("api gateway listening", "addr", f.listen, "coordinator", coordinatorURL,
		"owners", len(keys), "dev_insecure", f.devInsecure, "trusted_proxies", len(trusted))
	return serve(ctx, srv, log)
}

// coordinatorBaseURL returns the coordinator URL matching the TLS flags. A client
// certificate or a CA implies the coordinator listener is TLS, so an http:// URL is
// upgraded instead of silently failing every proxy request with 502.
func (f *flags) coordinatorBaseURL() string {
	u := f.coordinatorURL
	if f.tlsCA != "" || (f.tlsCert != "" && f.tlsKey != "") {
		if strings.HasPrefix(u, "http://") {
			u = "https://" + strings.TrimPrefix(u, "http://")
		}
	}
	return u
}

// standaloneBaseURL is the coordinator URL inside a single process: the scheme
// follows the server TLS flags and the host/port follows --internal-listen.
func standaloneBaseURL(f *flags) string {
	scheme := "http"
	if f.tlsCert != "" || f.tlsKey != "" {
		scheme = "https"
	}
	return scheme + "://" + f.internalListen
}

func runWorker(ctx context.Context, f *flags, log *slog.Logger) error {
	reg := buildRegistry(f, f.deviceType)
	if len(reg.All()) == 0 {
		return errors.New("no engines enabled; pass --engines")
	}
	for _, e := range reg.All() {
		if err := e.Probe(ctx); err != nil {
			log.Warn("engine probe failed", "engine", e.Name(), "err", err)
		} else {
			log.Info("engine ready", "engine", e.Name(), "version", e.Version(), "device", e.Device())
		}
	}
	// Missing OCR models are fatal, unlike a failed version probe: the worker would claim
	// scanned documents and fail every one of them (or try to download on an offline host).
	if err := probeModels(ctx, reg.All(), func(name string) { log.Info("engine models ready", "engine", name) }); err != nil {
		return err
	}
	gpus, _ := sysinfo.DetectGPU(ctx)
	cfg := worker.Config{
		CoordinatorURL:    f.coordinatorBaseURL(),
		InternalToken:     f.workerCredential(),
		WorkerID:          f.workerID,
		DeviceType:        f.deviceType,
		MaxConcurrency:    f.workerConcurrency,
		WorkDir:           f.workDir,
		HeartbeatInterval: f.heartbeat,
		PollInterval:      f.pollInterval,
		MaxParseTime:      time.Duration(f.maxParseMinutes) * time.Minute,
		MaxInputBytes:     f.maxUploadMB << 20,
		EscalateScans:     f.escalateScans,
		WorkMaxAge:        f.workMaxAge,
		WorkScrubInterval: f.workScrubInterval,
		Engines:           reg.Names(),
		Host:              hostname(),
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = jobs.NewWorkerID()
	}
	// Describe the same card the free-VRAM measurement uses (CUDA_VISIBLE_DEVICES[0], else
	// device 0); gpus[0] is not that card on a multi-GPU host with a pinned device.
	if f.deviceType == "gpu" || f.deviceType == "cuda" || f.deviceType == "mps" {
		if g, ok := sysinfo.ActiveGPU(gpus); ok {
			cfg.GPUModel = g.Name
			cfg.GPUMemoryBytes = g.MemoryMB << 20
		}
	}
	if err := os.MkdirAll(f.workDir, 0o750); err != nil {
		return err
	}
	if (f.deviceType == "gpu" || f.deviceType == "cuda" || f.deviceType == "mps") && f.workerConcurrency > 1 {
		log.Warn("GPU workers run one task at a time: --worker-concurrency is capped at 1 so filled VRAM cannot be overcommitted",
			"requested", f.workerConcurrency)
		cfg.MaxConcurrency = 1
	}
	w := worker.New(cfg, reg, log)
	workerTLS, terr := tlsutil.ClientConfig(f.tlsCA, f.tlsCert, f.tlsKey, f.tlsServerName, f.tlsInsecure)
	if terr != nil {
		return terr
	}
	w.HTTP.Transport = tlsutil.Transport(workerTLS)
	return w.Run(ctx)
}

// ensureStandaloneTokens gives the in-process api->coordinator and worker->coordinator
// hops their own random credentials when none were configured. Without this the
// coordinator listener of a standalone process accepted any local process (and any
// user on the host) as gateway or worker.
func ensureStandaloneTokens(f *flags, log *slog.Logger) error {
	if f.internalToken == "" {
		t, err := randomToken()
		if err != nil {
			return err
		}
		f.internalToken = t
		log.Info("standalone: generated a random in-process internal token; pass --internal-token to call " +
			"/internal/* (metrics, diagnostics) on the internal listener yourself")
	}
	if f.workerToken == "" {
		t, err := randomToken()
		if err != nil {
			return err
		}
		f.workerToken = t
	}
	return nil
}

func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// standaloneStopTimeout bounds each shutdown stage of a standalone process.
const standaloneStopTimeout = 30 * time.Second

func runStandalone(ctx context.Context, f *flags, cfg coordinator.Config, log *slog.Logger) error {
	// The in-process worker and gateway dial the listener this process starts; the
	// scheme follows the server TLS flags.
	f.coordinatorURL = standaloneBaseURL(f)

	// The coordinator is stopped last (it owns SQLite and closes it on return), so it
	// does not inherit the signal context directly.
	coordCtx, stopCoord := context.WithCancel(context.Background())
	defer stopCoord()
	edgeCtx, stopEdge := context.WithCancel(ctx)
	defer stopEdge()

	ready := make(chan struct{})
	coordErr := make(chan error, 1)
	go func() {
		coordErr <- runCoordinator(coordCtx, f, cfg, log, ready)
	}()
	select {
	case <-ready:
	case err := <-coordErr:
		return err
	case <-ctx.Done():
		stopCoord()
		return awaitStop(coordErr, "coordinator", log)
	case <-time.After(15 * time.Second):
		stopCoord()
		_ = awaitStop(coordErr, "coordinator", log)
		return errors.New("coordinator did not start")
	}

	apiErr := make(chan error, 1)
	go func() {
		apiErr <- runGateway(edgeCtx, f, f.coordinatorURL, log)
	}()
	workerErr := make(chan error, 1)
	go func() {
		workerErr <- runWorker(edgeCtx, f, log)
	}()

	var first error
	coordDone, apiDone, workerDone := false, false, false
	select {
	case first = <-coordErr:
		coordDone = true
	case first = <-apiErr:
		apiDone = true
	case first = <-workerErr:
		workerDone = true
	case <-ctx.Done():
	}
	if errors.Is(first, context.Canceled) {
		first = nil
	}

	// Gateway and worker first, so in-flight requests and the worker's final report
	// still reach a live coordinator; then the coordinator, which closes the database.
	stopEdge()
	if !apiDone {
		_ = awaitStop(apiErr, "api gateway", log)
	}
	if !workerDone {
		_ = awaitStop(workerErr, "worker", log)
	}
	stopCoord()
	if !coordDone {
		_ = awaitStop(coordErr, "coordinator", log)
	}
	return first
}

// awaitStop waits (bounded) for a component started by runStandalone to return.
func awaitStop(ch <-chan error, name string, log *slog.Logger) error {
	select {
	case err := <-ch:
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("component stopped with an error", "component", name, "err", err)
			return err
		}
		return nil
	case <-time.After(standaloneStopTimeout):
		log.Warn("component did not stop in time", "component", name, "timeout", standaloneStopTimeout.String())
		return nil
	}
}

// buildRegistry constructs the engines this worker can run.
func buildRegistry(f *flags, device string) *engines.Registry {
	reg := engines.NewRegistry()
	for _, name := range splitList(f.engines) {
		switch name {
		case "native":
			reg.Add(&engines.Native{})
		case "mock":
			reg.Add(&engines.Mock{DeviceStr: device})
		case "xberg":
			if f.xbergBin == "" {
				continue
			}
			reg.Add(&engines.Xberg{
				Bin: f.xbergBin, LibDir: f.xbergLibDir, VersionS: probeVersion(f.xbergBin, f.xbergLibDir),
				OCR: f.xbergOCR, OCRLang: f.xbergOCRLang, OCRBackend: f.xbergOCRBackend,
				Sandbox:  sandboxArg(f.sandbox),
				MaxRSSMB: f.engineMaxRSSMB, Timeout: f.xbergTimeout,
			})
		case "docling-rs":
			if f.doclingRSBin == "" {
				continue
			}
			reg.Add(&engines.DoclingRS{
				Bin: f.doclingRSBin, ModelRoot: f.doclingRSModel, VersionS: probeVersion(f.doclingRSBin, ""),
				DeviceStr: device, TextLayerOnly: f.doclingRSText, OCR: f.xbergOCR, OCRLang: doclingLang(f.xbergOCRLang),
				Sandbox: sandboxArg(f.sandbox), MaxRSSMB: f.engineMaxRSSMB, Timeout: f.doclingRSTimeout,
			})
		case "docling-serve":
			if f.doclingServe == "" {
				continue
			}
			dev := "cpu"
			if f.doclingServeGPU {
				dev = "gpu"
			}
			reg.Add(&engines.DoclingServe{BaseURL: strings.TrimRight(f.doclingServe, "/"), DeviceStr: dev,
				VersionS: f.doclingServe, OCR: f.xbergOCR})
		case "tika":
			if f.tikaURL == "" {
				continue
			}
			reg.Add(&engines.Tika{BaseURL: strings.TrimRight(f.tikaURL, "/"), VersionS: f.tikaURL})
		case "mineru":
			if f.mineruBin == "" {
				continue
			}
			reg.Add(&engines.MinerU{Bin: f.mineruBin, Tier: f.mineruTier, Home: f.mineruHome,
				DeviceStr: device, Sandbox: sandboxArg(f.sandbox), MaxRSSMB: f.engineMaxRSSMB,
				Timeout: f.mineruTimeout})
		}
	}
	return reg
}

func engineSpecs(f *flags) []coordinator.EngineSpec {
	vram := parseVRAMMap(f.engineMinVRAM)
	var out []coordinator.EngineSpec
	for _, name := range splitList(f.engines) {
		c, ok := engines.CapabilityFor(name)
		if !ok {
			continue
		}
		dev := c.Device
		if name == "docling-serve" && f.doclingServeGPU {
			dev = "gpu"
		}
		out = append(out, coordinator.EngineSpec{
			Name: name, Version: name, Device: dev, Formats: c.Formats, Modes: c.Modes,
			Priority: c.Priority, MinVRAMMB: vram[name], Notes: c.Notes,
		})
	}
	return out
}

// parseVRAMMap parses "name:mb,name:mb" into a lookup. Invalid entries are ignored
// with a warning rather than silently changing admission behaviour.
func parseVRAMMap(raw string) map[string]int {
	out := map[string]int{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, val, ok := strings.Cut(part, ":")
		mb, err := strconv.Atoi(strings.TrimSpace(val))
		if !ok || err != nil || mb < 0 {
			continue
		}
		out[strings.TrimSpace(name)] = mb
	}
	return out
}

func printCheck(f *flags, cfg coordinator.Config) error {
	gpus, gerr := sysinfo.DetectGPU(context.Background())
	out := map[string]any{
		"role": f.role,
		"host": map[string]any{
			"cpu_cores": sysinfo.CPU(), "memory_bytes": sysinfo.MemoryBytes(),
			"container": sysinfo.Container(),
		},
		"gpus":      gpus,
		"gpu_error": errString(gerr),
		"config": map[string]any{
			"result_ttl": cfg.ResultTTL.String(), "max_result_ttl": cfg.MaxResultTTL.String(),
			"max_job_lifetime": cfg.MaxJobLifetime.String(), "cleanup_interval": cfg.CleanupInterval.String(),
			"download_lease": cfg.DownloadLeaseTTL.String(), "worker_lease": cfg.WorkerLeaseTTL.String(),
			"tombstone_ttl": cfg.TombstoneTTL.String(), "max_attempts": cfg.MaxAttempts,
			"max_upload_bytes": cfg.MaxUploadBytes,
		},
		"routing_table": cfg.Engines,
	}
	// Model files are checked only where the role runs engines; a failure is printed and
	// also makes --check exit non-zero, so a deployment script can gate on it.
	var modelErr error
	if f.role == "worker" || f.role == "standalone" {
		models := map[string]string{}
		for _, e := range buildRegistry(f, f.deviceType).All() {
			p, ok := e.(engines.ModelProber)
			if !ok {
				continue
			}
			checked, err := p.ProbeModels(context.Background())
			switch {
			case err != nil:
				models[e.Name()] = err.Error()
				modelErr = fmt.Errorf("engine %s: %w", e.Name(), err)
			case checked:
				models[e.Name()] = "ok"
			}
		}
		out["engine_models"] = models
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	return modelErr
}

// probeModels runs ProbeModels on every engine that has model files to check.
func probeModels(ctx context.Context, all []engines.Engine, ready func(name string)) error {
	for _, e := range all {
		p, ok := e.(engines.ModelProber)
		if !ok {
			continue
		}
		checked, err := p.ProbeModels(ctx)
		if err != nil {
			return fmt.Errorf("engine %s: %w", e.Name(), err)
		}
		if checked {
			ready(e.Name())
		}
	}
	return nil
}

func serve(ctx context.Context, srv *http.Server, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		var err error
		if srv.TLSConfig != nil {
			// Certificates are already in TLSConfig; empty file names are valid.
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	log.Info("shutting down", "addr", srv.Addr)
	return srv.Shutdown(shutdownCtx)
}

// buildAPIKeys loads hashed API credentials from a file (preferred) or from the
// --api-keys value. Plaintext entries are hashed immediately and never retained.
// The key policy (length, one token per owner) is enforced here, at startup.
func buildAPIKeys(f *flags, log *slog.Logger) ([]api.Key, error) {
	var (
		keys []api.Key
		err  error
	)
	switch {
	case f.apiKeysFile != "":
		keys, err = api.ParseKeysFile(f.apiKeysFile)
	case f.apiKeys != "":
		keys, err = api.ParseKeys(f.apiKeys)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	warnings, err := api.CheckKeys(keys, f.devInsecure)
	if err != nil {
		return nil, err
	}
	for _, w := range warnings {
		log.Warn(w)
	}
	return keys, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func sandboxArg(v string) string {
	if v == "unshare-net" {
		return "unshare-net"
	}
	return ""
}

func doclingLang(tess string) string {
	switch strings.ToLower(tess) {
	case "chi_sim", "chi", "ch", "zh":
		return "ch"
	case "eng", "en", "":
		return "en"
	}
	return "en"
}

func probeVersion(bin, libDir string) string {
	env := []string{}
	if libDir != "" {
		env = append(env, "LD_LIBRARY_PATH="+libDir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := engines.RunCLI(ctx, engines.CLISpec{Bin: bin, Env: env, MaxOutput: 4096}, os.TempDir(), "--version")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(res.Stdout))
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return h
}

// envErrs collects malformed PARSEDOCK_* values seen while computing flag defaults;
// parseFlags turns them into a startup error.
var envErrs []error

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func intEnv(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			envErrs = append(envErrs, fmt.Errorf("%s=%q is not an integer", k, v))
			return def
		}
		return n
	}
	return def
}

func int64Env(k string, def int64) int64 {
	if v := os.Getenv(k); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			envErrs = append(envErrs, fmt.Errorf("%s=%q is not an integer", k, v))
			return def
		}
		return n
	}
	return def
}

func floatEnv(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			envErrs = append(envErrs, fmt.Errorf("%s=%q is not a number", k, v))
			return def
		}
		return n
	}
	return def
}

func durEnv(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			envErrs = append(envErrs, fmt.Errorf("%s=%q is not a duration (e.g. 30s, 10m)", k, v))
			return def
		}
		return d
	}
	return def
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
