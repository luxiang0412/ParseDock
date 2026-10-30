package api

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePlaintextKeyHashesImmediately(t *testing.T) {
	keys, err := ParseKeyEntries([]string{"super-secret-token:acme"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %d", len(keys))
	}
	k := keys[0]
	if k.Owner != "acme" {
		t.Fatalf("owner = %q", k.Owner)
	}
	for _, scope := range AllScopes() {
		if !k.HasScope(scope) {
			t.Fatalf("default scopes must include %s", scope)
		}
	}
	// The plaintext must not survive anywhere in the struct.
	if strings.Contains(k.Label, "super-secret-token") {
		t.Fatalf("label leaks the token: %q", k.Label)
	}
	want := sha256.Sum256([]byte("super-secret-token"))
	if k.hash != want {
		t.Fatal("token was not hashed with sha256")
	}
	if _, ok := Lookup(keys, "super-secret-token"); !ok {
		t.Fatal("exact token did not match")
	}
	if _, ok := Lookup(keys, "super-secret-toke"); ok {
		t.Fatal("prefix matched")
	}
	if _, ok := Lookup(keys, ""); ok {
		t.Fatal("empty token matched")
	}
}

func TestParseHashedKeyAndScopes(t *testing.T) {
	sum := sha256.Sum256([]byte("t0ken"))
	entry := "sha256:" + hex.EncodeToString(sum[:]) + ":acme:jobs:write+jobs:read"
	keys, err := ParseKeyEntries([]string{entry})
	if err != nil {
		t.Fatal(err)
	}
	k, ok := Lookup(keys, "t0ken")
	if !ok {
		t.Fatal("hashed entry did not match")
	}
	if !k.HasScope(ScopeJobsWrite) || !k.HasScope(ScopeJobsRead) {
		t.Fatalf("scopes = %v", k.Scopes)
	}
	if k.HasScope(ScopeJobsAck) {
		t.Fatal("ack scope granted but not requested")
	}
}

func TestParseKeyErrors(t *testing.T) {
	cases := []string{
		"no-owner",
		":only-owner",
		"token:",
		"sha256:deadbeef:owner",        // too short
		"sha256:zz:owner",              // not hex
		"token:owner:bogus:scope",      // unknown scope
		"token:owner:jobs:write+bogus", // one bad scope
	}
	for _, c := range cases {
		if _, err := ParseKeyEntries([]string{c}); err == nil {
			t.Errorf("%q: expected an error", c)
		} else if strings.Contains(err.Error(), "super-secret") {
			t.Errorf("error leaks the secret: %v", err)
		}
	}
}

func TestParseKeysFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys")
	content := "# production keys\n\nkey-a:acme:jobs:write\nsha256:" +
		hex.EncodeToString(func() []byte { s := sha256.Sum256([]byte("key-b")); return s[:] }()) + ":beta\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	keys, err := ParseKeysFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d", len(keys))
	}
	if _, ok := Lookup(keys, "key-a"); !ok {
		t.Fatal("key-a not found")
	}
	if k, ok := Lookup(keys, "key-b"); !ok || k.Owner != "beta" {
		t.Fatalf("key-b not found or wrong owner: %+v %v", k, ok)
	}
}

func TestNeededScope(t *testing.T) {
	cases := []struct{ method, path, want string }{
		{"POST", "/v1/jobs", ScopeJobsWrite},
		{"POST", "/v1/parse", ScopeJobsWrite},
		{"GET", "/v1/jobs/x", ScopeJobsRead},
		{"GET", "/v1/jobs/x/result", ScopeJobsRead},
		{"GET", "/v1/jobs/x/events", ScopeJobsRead},
		{"POST", "/v1/jobs/x/ack", ScopeJobsAck},
		{"POST", "/v1/jobs/x/cancel", ScopeJobsAck},
	}
	for _, c := range cases {
		if got := NeededScope(c.method, c.path); got != c.want {
			t.Errorf("NeededScope(%s %s) = %s, want %s", c.method, c.path, got, c.want)
		}
	}
}

func TestSanitizeHeaderValue(t *testing.T) {
	got := sanitizeHeaderValue("attachment; filename=\"a\r\nb\x00c\"")
	if strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("control characters survived: %q", got)
	}
	if !strings.Contains(got, "abc") && !strings.Contains(got, "a") {
		t.Fatalf("value lost: %q", got)
	}
}

func TestValidRequestID(t *testing.T) {
	if !validRequestID("req_0123456789abcdef-AB") {
		t.Fatal("valid id rejected")
	}
	for _, bad := range []string{"", "with space", "newline\n", strings.Repeat("x", 200)} {
		if validRequestID(bad) {
			t.Errorf("%q accepted as request id", bad)
		}
	}
}
