package coordinator

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

// Error is a coordinator-level error with a stable code and HTTP mapping.
type Error struct {
	Code    string
	Message string
	Status  int
	// Retryable marks worker/transient failures that may be retried.
	Retryable bool
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// NewError builds a coordinator error.
func NewError(status int, code, msg string) *Error {
	return &Error{Code: code, Message: msg, Status: status}
}

// AsError unwraps a coordinator error, defaulting to internal.
func AsError(err error) *Error {
	var ce *Error
	if errors.As(err, &ce) {
		return ce
	}
	return &Error{Code: jobs.ErrInternal, Message: err.Error(), Status: 500}
}

// EngineSpec is a coordinator-side declaration of an engine's capabilities.
// It is used for routing decisions; workers must independently confirm the
// engine is actually runnable (the claim filters on that).
type EngineSpec struct {
	Name     string   `json:"name"`
	Version  string   `json:"version"`
	Device   string   `json:"device"` // cpu | gpu | both
	Formats  []string `json:"formats"`
	Modes    []string `json:"modes"` // fast | accurate | auto
	Priority int      `json:"priority"`
	Notes    string   `json:"notes,omitempty"`
}

// Supports reports whether the engine can handle format in mode.
//
// Modes lists what the engine explicitly serves. A request with mode=auto is
// compatible with any engine; a request with mode=fast|accurate requires the
// engine to declare that exact mode. This prevents an "auto"-only engine from
// silently satisfying an accurate request.
func (e EngineSpec) Supports(format, mode string) bool {
	okF := false
	for _, f := range e.Formats {
		if f == format || f == "*" {
			okF = true
			break
		}
	}
	if !okF || len(e.Modes) == 0 {
		return okF && len(e.Modes) == 0
	}
	if mode == "" || mode == jobs.ModeAuto {
		return true
	}
	for _, m := range e.Modes {
		if m == mode || m == "*" {
			return true
		}
	}
	return false
}

// GPU reports whether the engine can execute on a GPU.
func (e EngineSpec) GPU() bool { return e.Device == "gpu" || e.Device == "both" }

// CPU reports whether the engine can execute on CPU.
func (e EngineSpec) CPU() bool { return e.Device == "cpu" || e.Device == "both" }

// Config holds lifecycle parameters. Every duration is absolute-UTC persisted.
type Config struct {
	ResultTTL         time.Duration // default result retention
	MaxResultTTL      time.Duration // client-requested TTL ceiling
	MaxJobLifetime    time.Duration // queued+running ceiling
	FailedRetention   time.Duration // keep failed/cancelled artifacts for debugging
	CleanupInterval   time.Duration
	DownloadLeaseTTL  time.Duration
	WorkerLeaseTTL    time.Duration
	HeartbeatInterval time.Duration
	TombstoneTTL      time.Duration
	MaxAttempts       int
	MaxUploadBytes    int64
	DefaultPriority   int
	Engines           []EngineSpec
	Limits            interface{} // storage.Limits, kept as interface to avoid cycle in JSON
}

// DefaultConfig returns the Phase 0 defaults from the task guide.
func DefaultConfig() Config {
	return Config{
		ResultTTL:         1 * time.Hour,
		MaxResultTTL:      24 * time.Hour,
		MaxJobLifetime:    24 * time.Hour,
		FailedRetention:   10 * time.Minute,
		CleanupInterval:   60 * time.Second,
		DownloadLeaseTTL:  60 * time.Second,
		WorkerLeaseTTL:    60 * time.Second,
		HeartbeatInterval: 10 * time.Second,
		TombstoneTTL:      10 * time.Minute,
		MaxAttempts:       3,
		MaxUploadBytes:    64 << 20,
	}
}

// ValidateInternalBinding refuses an internal listener that is reachable from
// outside the host while no shared secret is configured. The coordinator trusts
// X-Parsedock-Owner from whoever reaches it, so a public bind without a token is
// an authentication bypass (F04). Loopback binds stay allowed for development.
func ValidateInternalBinding(addr, token string, devInsecure bool) error {
	if strings.TrimSpace(token) != "" || devInsecure {
		return nil
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return nil
	}
	// An empty host (":8081") binds every interface, which is the dangerous case.
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("internal listener %q is not loopback but PARSEDOCK_INTERNAL_TOKEN is empty: "+
		"set the token (required) or bind 127.0.0.1; --dev-insecure disables this check", addr)
}

func (c Config) String() string {
	return fmt.Sprintf("ttl=%s maxTTL=%s maxLifetime=%s cleanup=%s dlLease=%s workerLease=%s tombstone=%s attempts=%d",
		c.ResultTTL, c.MaxResultTTL, c.MaxJobLifetime, c.CleanupInterval, c.DownloadLeaseTTL,
		c.WorkerLeaseTTL, c.TombstoneTTL, c.MaxAttempts)
}
