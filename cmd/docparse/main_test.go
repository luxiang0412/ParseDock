package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func validFlags() *flags {
	return &flags{
		role: "coordinator", maxUploadMB: 64, maxExpandedMB: 512, maxPages: 2000,
		heartbeat: 10 * time.Second, workerLease: 60 * time.Second, rateLimit: 20,
		bodyReadTimeout: 10 * time.Minute,
	}
}

// Malformed environment values used to fall back to the default silently.
func TestEnvParseErrorsAreCollected(t *testing.T) {
	envErrs = nil
	t.Setenv("PARSEDOCK_T_INT", "12x")
	t.Setenv("PARSEDOCK_T_I64", "1e3")
	t.Setenv("PARSEDOCK_T_FLOAT", "fast")
	t.Setenv("PARSEDOCK_T_DUR", "10")
	t.Setenv("PARSEDOCK_T_OK", "7")
	if intEnv("PARSEDOCK_T_INT", 1) != 1 || int64Env("PARSEDOCK_T_I64", 2) != 2 ||
		floatEnv("PARSEDOCK_T_FLOAT", 3) != 3 || durEnv("PARSEDOCK_T_DUR", time.Second) != time.Second {
		t.Fatal("defaults must still be returned so parsing can continue and report every error")
	}
	if intEnv("PARSEDOCK_T_OK", 0) != 7 || intEnv("PARSEDOCK_T_UNSET", 5) != 5 {
		t.Fatal("valid / unset values")
	}
	if len(envErrs) != 4 {
		t.Fatalf("collected %d env errors, want 4: %v", len(envErrs), envErrs)
	}
	if !strings.Contains(envErrs[3].Error(), "PARSEDOCK_T_DUR") {
		t.Fatalf("error must name the variable: %v", envErrs[3])
	}
	envErrs = nil
}

func TestValidate(t *testing.T) {
	if err := validFlags().validate(); err != nil {
		t.Fatalf("defaults: %v", err)
	}
	for name, mutate := range map[string]func(*flags){
		"upload 0":                  func(f *flags) { f.maxUploadMB = 0 },
		"expanded negative":         func(f *flags) { f.maxExpandedMB = -1 },
		"pages 0":                   func(f *flags) { f.maxPages = 0 },
		"heartbeat 0":               func(f *flags) { f.heartbeat = 0 },
		"heartbeat >= lease":        func(f *flags) { f.heartbeat = time.Minute },
		"negative rate":             func(f *flags) { f.rateLimit = -1 },
		"negative body timeout":     func(f *flags) { f.bodyReadTimeout = -time.Second },
		"bad trusted proxy":         func(f *flags) { f.trustedProxies = "10.0.0.0/33" },
		"worker token, no internal": func(f *flags) { f.workerToken = "w-0123456789abcdef" },
		"same tokens": func(f *flags) {
			f.internalToken = "same-0123456789abcdef"
			f.workerToken = "same-0123456789abcdef"
		},
	} {
		f := validFlags()
		mutate(f)
		if err := f.validate(); err == nil {
			t.Errorf("%s: expected a validation error", name)
		}
	}
	// A worker only sees its own heartbeat and may hold only the worker token.
	w := validFlags()
	w.role, w.heartbeat, w.workerToken = "worker", 2*time.Minute, "w-0123456789abcdef"
	if err := w.validate(); err != nil {
		t.Fatalf("worker with only a worker token: %v", err)
	}
	if w.workerCredential() != "w-0123456789abcdef" {
		t.Fatal("a worker must present the worker token when it has one")
	}
	legacy := validFlags()
	legacy.internalToken = "legacy-0123456789abcdef"
	if legacy.workerCredential() != "legacy-0123456789abcdef" {
		t.Fatal("without a worker token the worker presents the internal token (compatibility)")
	}
}

// Standalone must never run its coordinator listener without credentials.
func TestEnsureStandaloneTokens(t *testing.T) {
	f := validFlags()
	f.role = "standalone"
	if err := ensureStandaloneTokens(f, quiet()); err != nil {
		t.Fatal(err)
	}
	if len(f.internalToken) != 64 || len(f.workerToken) != 64 || f.internalToken == f.workerToken {
		t.Fatalf("generated tokens: %d/%d chars, equal=%v", len(f.internalToken), len(f.workerToken),
			f.internalToken == f.workerToken)
	}
	if err := f.validate(); err != nil {
		t.Fatalf("generated pair must validate: %v", err)
	}
	given := validFlags()
	given.internalToken = "operator-chosen-token-0123"
	if err := ensureStandaloneTokens(given, quiet()); err != nil {
		t.Fatal(err)
	}
	if given.internalToken != "operator-chosen-token-0123" || given.workerToken == "" {
		t.Fatal("an operator token must be kept; only the missing worker token is generated")
	}
}

// The dead assignment in runStandalone used to force http:// regardless of TLS.
func TestStandaloneURLFollowsTLS(t *testing.T) {
	f := validFlags()
	f.internalListen = "127.0.0.1:9081"
	if got := standaloneBaseURL(f); got != "http://127.0.0.1:9081" {
		t.Fatalf("plaintext: %s", got)
	}
	f.tlsCert, f.tlsKey = "c.pem", "k.pem"
	if got := standaloneBaseURL(f); got != "https://127.0.0.1:9081" {
		t.Fatalf("tls: %s", got)
	}
	f.coordinatorURL = standaloneBaseURL(f)
	if got := f.coordinatorBaseURL(); got != "https://127.0.0.1:9081" {
		t.Fatalf("worker/gateway URL in standalone TLS: %s", got)
	}
}

func TestBuildAPIKeysEnforcesPolicy(t *testing.T) {
	f := validFlags()
	f.apiKeys = "short:acme"
	if _, err := buildAPIKeys(f, quiet()); err == nil {
		t.Fatal("a short key must fail startup")
	}
	f.devInsecure = true
	if keys, err := buildAPIKeys(f, quiet()); err != nil || len(keys) != 1 {
		t.Fatalf("--dev-insecure accepts a short key with a warning: %v", err)
	}
	f.devInsecure = false
	f.apiKeys = strings.Repeat("x", 32) + ":acme," + strings.Repeat("x", 32) + ":globex"
	if _, err := buildAPIKeys(f, quiet()); err == nil {
		t.Fatal("one token for two owners must fail startup")
	}
}
