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
	"parsedock.dev/parsedock/internal/worker"
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
	apiKeys          string
	apiKeysFile      string
	quotaConcurrent  int
	quotaJobsPerDay  int
	quotaBytesPerDay int64
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
	xbergTimeout     time.Duration
	doclingRSTimeout time.Duration
	mineruTimeout    time.Duration
	engineMaxRSSMB   int64
	escalateScans    bool
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
	f := parseFlags()
	if err := run(f); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func parseFlags() *flags {
	f := &flags{}
	fs := flag.NewFlagSet("docparse", flag.ExitOnError)
	fs.StringVar(&f.role, "role", env("PARSEDOCK_ROLE", "standalone"), "standalone|coordinator|api|worker")
	fs.StringVar(&f.listen, "listen", env("PARSEDOCK_LISTEN", "127.0.0.1:8080"), "public API listen address")
	fs.StringVar(&f.internalListen, "internal-listen", env("PARSEDOCK_INTERNAL_LISTEN", "127.0.0.1:8081"), "coordinator internal listen address")
	fs.StringVar(&f.coordinatorURL, "coordinator-url", env("PARSEDOCK_COORDINATOR_URL", "http://127.0.0.1:8081"), "coordinator base URL")
	fs.StringVar(&f.dataDir, "data-dir", env("PARSEDOCK_DATA_DIR", "./runtime/data"), "data directory (coordinator only)")
	fs.StringVar(&f.dbPath, "db", env("PARSEDOCK_DB", ""), "sqlite path (default <data-dir>/coordinator.db)")
	fs.StringVar(&f.workDir, "work-dir", env("PARSEDOCK_WORK_DIR", "./runtime/work"), "worker scratch directory")
	fs.StringVar(&f.internalToken, "internal-token", env("PARSEDOCK_INTERNAL_TOKEN", ""), "shared internal token")
	fs.StringVar(&f.apiKeys, "api-keys", env("PARSEDOCK_API_KEYS", ""), "comma list <token:owner[:scopes]> or sha256:<hex>:owner[:scopes]")
	fs.StringVar(&f.apiKeysFile, "api-keys-file", env("PARSEDOCK_API_KEYS_FILE", ""), "file with one key entry per line (preferred: argv is world-readable)")
	fs.IntVar(&f.quotaConcurrent, "quota-concurrent", intEnv("PARSEDOCK_QUOTA_CONCURRENT", 0), "max queued+running jobs per owner (0 = unlimited)")
	fs.IntVar(&f.quotaJobsPerDay, "quota-jobs-per-day", intEnv("PARSEDOCK_QUOTA_JOBS_PER_DAY", 0), "max jobs per owner per rolling 24h (0 = unlimited)")
	fs.Int64Var(&f.quotaBytesPerDay, "quota-bytes-per-day", int64Env("PARSEDOCK_QUOTA_BYTES_PER_DAY", 0), "max input bytes per owner per rolling 24h (0 = unlimited)")
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
	fs.IntVar(&f.maxPages, "max-pages", intEnv("PARSEDOCK_MAX_PAGES", 500), "PDF page limit")
	fs.Int64Var(&f.minFreeGB, "min-free-gb", int64Env("PARSEDOCK_MIN_FREE_GB", 2), "disk safety watermark (GiB)")
	fs.Float64Var(&f.rateLimit, "rate-limit", floatEnv("PARSEDOCK_RATE_LIMIT", 20), "per-owner requests/second")
	fs.IntVar(&f.rateBurst, "rate-burst", intEnv("PARSEDOCK_RATE_BURST", 40), "per-owner burst")

	fs.StringVar(&f.workerID, "worker-id", env("PARSEDOCK_WORKER_ID", ""), "worker id (generated when empty)")
	fs.IntVar(&f.workerConcurrency, "worker-concurrency", intEnv("PARSEDOCK_WORKER_CONCURRENCY", 1), "concurrent tasks per worker")
	fs.StringVar(&f.deviceType, "device-type", env("PARSEDOCK_DEVICE_TYPE", "cpu"), "cpu|gpu")
	fs.IntVar(&f.maxParseMinutes, "max-parse-minutes", intEnv("PARSEDOCK_MAX_PARSE_MINUTES", 30), "engine time budget")
	fs.DurationVar(&f.workMaxAge, "work-max-age", durEnv("PARSEDOCK_WORK_MAX_AGE", time.Hour), "remove crashed attempt dirs older than this")
	fs.DurationVar(&f.workScrubInterval, "work-scrub-interval", durEnv("PARSEDOCK_WORK_SCRUB_INTERVAL", 10*time.Minute), "periodic scratch sweep interval")

	fs.StringVar(&f.xbergBin, "xberg-bin", env("PARSEDOCK_XBERG_BIN", ""), "path to xberg CLI")
	fs.StringVar(&f.xbergLibDir, "xberg-lib-dir", env("PARSEDOCK_XBERG_LIB_DIR", ""), "dir with xberg shared libs")
	fs.BoolVar(&f.xbergOCR, "xberg-ocr", env("PARSEDOCK_XBERG_OCR", "1") == "1", "enable OCR for xberg")
	fs.StringVar(&f.xbergOCRLang, "xberg-ocr-lang", env("PARSEDOCK_XBERG_OCR_LANG", "eng"), "tesseract language")
	fs.DurationVar(&f.xbergTimeout, "xberg-timeout", durEnv("PARSEDOCK_XBERG_TIMEOUT", 10*time.Minute), "per-document xberg ceiling (capped by --max-parse-minutes)")
	fs.DurationVar(&f.doclingRSTimeout, "docling-rs-timeout", durEnv("PARSEDOCK_DOCLING_RS_TIMEOUT", 15*time.Minute), "per-document docling-rs ceiling (capped by --max-parse-minutes)")
	fs.DurationVar(&f.mineruTimeout, "mineru-timeout", durEnv("PARSEDOCK_MINERU_TIMEOUT", 30*time.Minute), "per-document MinerU ceiling (capped by --max-parse-minutes)")
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
	_ = fs.Parse(os.Args[1:])
	return f
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
		MaxZipUncompressed: 512 << 20,
		MaxZipRatio:        200,
		MaxZipEntries:      5000,
	}
	cfg.Engines = engineSpecs(f)
	cfg.Quota = coordinator.Quota{
		MaxConcurrent:  f.quotaConcurrent,
		MaxJobsPerDay:  f.quotaJobsPerDay,
		MaxBytesPerDay: f.quotaBytesPerDay,
	}

	if f.check {
		return printCheck(f, cfg)
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

	go svc.StartCleanup(ctx)

	mux := http.NewServeMux()
	mux.Handle("/internal/", srv.InternalMux())
	mux.Handle("/v1/", srv.PublicMux())
	mux.Handle("/healthz", srv.PublicMux())
	mux.Handle("/readyz", srv.PublicMux())

	httpSrv := &http.Server{
		Addr:              f.internalListen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: result downloads and SSE are long-lived.
	}
	log.Info("coordinator listening", "addr", f.internalListen, "db", dbPath, "config", cfg.String())
	if ready != nil {
		close(ready)
	}
	return serve(ctx, httpSrv, log)
}

func runAPI(ctx context.Context, f *flags, log *slog.Logger) error {
	keys, err := buildAPIKeys(f)
	if err != nil {
		return err
	}
	gw, err := api.New(api.Config{
		CoordinatorURL: f.coordinatorURL,
		InternalToken:  f.internalToken,
		Keys:           keys,
		DevInsecure:    f.devInsecure,
		RateLimitRPS:   f.rateLimit,
		RateBurst:      f.rateBurst,
		MaxUploadBytes: f.maxUploadMB << 20,
		Log:            log,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              f.listen,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	log.Info("api gateway listening", "addr", f.listen, "coordinator", f.coordinatorURL,
		"owners", len(keys), "dev_insecure", f.devInsecure)
	return serve(ctx, srv, log)
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
	gpus, _ := sysinfo.DetectGPU(ctx)
	cfg := worker.Config{
		CoordinatorURL:    f.coordinatorURL,
		InternalToken:     f.internalToken,
		WorkerID:          f.workerID,
		DeviceType:        f.deviceType,
		MaxConcurrency:    f.workerConcurrency,
		WorkDir:           f.workDir,
		HeartbeatInterval: f.heartbeat,
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
	if f.deviceType == "gpu" && len(gpus) > 0 {
		cfg.GPUModel = gpus[0].Name
		cfg.GPUMemoryBytes = gpus[0].MemoryMB << 20
	}
	if err := os.MkdirAll(f.workDir, 0o750); err != nil {
		return err
	}
	w := worker.New(cfg, reg, log)
	return w.Run(ctx)
}

func runStandalone(ctx context.Context, f *flags, cfg coordinator.Config, log *slog.Logger) error {
	// In standalone the worker and gateway talk to the local coordinator listener.
	f.coordinatorURL = "http://" + f.internalListen
	ready := make(chan struct{})
	errCh := make(chan error, 2)
	go func() {
		errCh <- runCoordinator(ctx, f, cfg, log, ready)
	}()
	select {
	case <-ready:
	case err := <-errCh:
		return err
	case <-time.After(15 * time.Second):
		return errors.New("coordinator did not start")
	}

	apiErr := make(chan error, 1)
	go func() {
		keys, kerr := buildAPIKeys(f)
		if kerr != nil {
			apiErr <- kerr
			return
		}
		gw, err := api.New(api.Config{
			CoordinatorURL: "http://" + f.internalListen,
			InternalToken:  f.internalToken,
			Keys:           keys,
			DevInsecure:    f.devInsecure,
			RateLimitRPS:   f.rateLimit,
			RateBurst:      f.rateBurst,
			MaxUploadBytes: f.maxUploadMB << 20,
			Log:            log,
		})
		if err != nil {
			apiErr <- err
			return
		}
		srv := &http.Server{
			Addr:              f.listen,
			Handler:           gw.Handler(),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
		log.Info("api gateway listening", "addr", f.listen, "coordinator", f.internalListen)
		apiErr <- serve(ctx, srv, log)
	}()

	workerErr := make(chan error, 1)
	go func() {
		workerErr <- runWorker(ctx, f, log)
	}()

	select {
	case err := <-errCh:
		return err
	case err := <-apiErr:
		return err
	case err := <-workerErr:
		return err
	case <-ctx.Done():
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
				OCR: f.xbergOCR, OCRLang: f.xbergOCRLang, Sandbox: sandboxArg(f.sandbox),
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
			Priority: c.Priority, Notes: c.Notes,
		})
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
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func serve(ctx context.Context, srv *http.Server, log *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
func buildAPIKeys(f *flags) ([]api.Key, error) {
	if f.apiKeysFile != "" {
		return api.ParseKeysFile(f.apiKeysFile)
	}
	if f.apiKeys == "" {
		return nil, nil
	}
	return api.ParseKeys(f.apiKeys)
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

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func intEnv(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func int64Env(k string, def int64) int64 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func floatEnv(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return def
}

func durEnv(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
