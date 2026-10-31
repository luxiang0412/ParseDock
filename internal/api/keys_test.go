package api

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
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

func TestCheckKeysLengthPolicy(t *testing.T) {
	short, err := ParseKeyEntries([]string{"short-key:acme"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckKeys(short, false); err == nil {
		t.Fatal("a plaintext key under 16 characters must be refused")
	} else if strings.Contains(err.Error(), "short-key") {
		t.Fatalf("the error leaks the token: %v", err)
	}
	warn, err := CheckKeys(short, true)
	if err != nil || len(warn) != 1 {
		t.Fatalf("--dev-insecure must downgrade a short key to a warning: %v %v", warn, err)
	}
	mid, _ := ParseKeyEntries([]string{"sixteen-chars-ok:acme"})
	warn, err = CheckKeys(mid, false)
	if err != nil || len(warn) != 1 {
		t.Fatalf("16..31 characters must be accepted with a warning: %v %v", warn, err)
	}
	if strings.Contains(warn[0], "sixteen-chars-ok") {
		t.Fatalf("warning leaks the token: %s", warn[0])
	}
	long, _ := ParseKeyEntries([]string{strings.Repeat("k", 32) + ":acme"})
	if warn, err := CheckKeys(long, false); err != nil || len(warn) != 0 {
		t.Fatalf("a 32-character key needs no warning: %v %v", warn, err)
	}
	// A sha256: entry has no knowable length and is never flagged.
	hashed, _ := ParseKeyEntries([]string{"sha256:" + strings.Repeat("ab", 32) + ":acme"})
	if warn, err := CheckKeys(hashed, false); err != nil || len(warn) != 0 {
		t.Fatalf("hashed entries must pass: %v %v", warn, err)
	}
}

func TestCheckKeysDuplicates(t *testing.T) {
	tok := strings.Repeat("d", 40)
	sum := sha256.Sum256([]byte(tok))
	// Same secret, once plaintext and once hashed, for two different owners.
	keys, err := ParseKeyEntries([]string{tok + ":acme", "sha256:" + hex.EncodeToString(sum[:]) + ":globex"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckKeys(keys, true); err == nil {
		t.Fatal("one token for two owners must be refused, even with --dev-insecure")
	} else if strings.Contains(err.Error(), tok) || strings.Contains(err.Error(), hex.EncodeToString(sum[:])) {
		t.Fatalf("the error leaks the token or hash: %v", err)
	}
	same, _ := ParseKeyEntries([]string{tok + ":acme:jobs:read", tok + ":acme"})
	warn, err := CheckKeys(same, false)
	if err != nil || len(warn) != 1 {
		t.Fatalf("a duplicate for the same owner is a warning: %v %v", warn, err)
	}
	// Rotation (two different tokens for one owner) is the documented pattern: no noise.
	rot, _ := ParseKeyEntries([]string{strings.Repeat("a", 32) + ":acme", strings.Repeat("b", 32) + ":acme"})
	if warn, err := CheckKeys(rot, false); err != nil || len(warn) != 0 {
		t.Fatalf("rotation pair: %v %v", warn, err)
	}
}

// TestNeededScopeMatchesSpec ties the gateway's route->scope mapping to the scope table
// published in api/openapi.yaml (bearerAuth description), and checks the table covers
// every /v1 operation. tests/contract/openapi_contract.py runs it as part of the
// contract phase. The spec is read with line patterns, not a YAML library, to keep the
// module free of a test-only dependency.
func TestNeededScopeMatchesSpec(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")

	// Every documented /v1 operation.
	var ops []string
	pathRe := regexp.MustCompile(`^  (/\S+):\s*$`)
	methodRe := regexp.MustCompile(`^    (get|post|put|patch|delete):`)
	cur := ""
	for _, l := range lines {
		if m := pathRe.FindStringSubmatch(l); m != nil {
			cur = m[1]
			continue
		}
		if m := methodRe.FindStringSubmatch(l); m != nil && strings.HasPrefix(cur, "/v1/") {
			ops = append(ops, strings.ToUpper(m[1])+" "+cur)
		}
	}
	if len(ops) < 7 {
		t.Fatalf("parsed only %d /v1 operations from the spec: %v", len(ops), ops)
	}

	// The scope table: "* `jobs:write` — `POST /v1/jobs`, `POST /v1/parse`". A bare
	// suffix (`/result`) inherits the previous entry's method on /v1/jobs/{id}.
	bulletRe := regexp.MustCompile("^\\s*\\* `(jobs:[a-z]+)` — (.+)$")
	itemRe := regexp.MustCompile("`([^`]+)`")
	table := map[string]string{}
	for _, l := range lines {
		m := bulletRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		method := ""
		for _, it := range itemRe.FindAllStringSubmatch(m[2], -1) {
			item := it[1]
			if mm, p, ok := strings.Cut(item, " "); ok {
				method = mm
				item = p
			} else if !strings.HasPrefix(item, "/v1/") {
				item = "/v1/jobs/{id}" + item
			}
			if method == "" {
				t.Fatalf("scope table item %q has no method", it[1])
			}
			table[method+" "+item] = m[1]
		}
	}
	if len(table) == 0 {
		t.Fatal("no scope table found in the bearerAuth description")
	}
	for _, op := range ops {
		if _, ok := table[op]; !ok {
			t.Errorf("spec operation %s is missing from the scope table", op)
		}
	}
	for op, scope := range table {
		method, path, _ := strings.Cut(op, " ")
		concrete := strings.ReplaceAll(path, "{id}", "job_06GRQJT5HC1DDZMPWRSKSCKM24")
		if got := NeededScope(method, concrete); got != scope {
			t.Errorf("NeededScope(%s) = %s, spec scope table says %s", op, got, scope)
		}
	}
}
