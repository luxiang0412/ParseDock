package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// Scopes a key can hold. A key without an explicit scope list gets all three
// (backwards compatible with the Phase 0 `token:owner` form).
const (
	ScopeJobsWrite = "jobs:write"
	ScopeJobsRead  = "jobs:read"
	ScopeJobsAck   = "jobs:ack"
)

// AllScopes is the default grant.
func AllScopes() []string { return []string{ScopeJobsWrite, ScopeJobsRead, ScopeJobsAck} }

// Key is a hashed API credential. The plaintext token is never stored after
// parsing, never logged and never written to disk by ParseDock.
//
// Accepted forms in --api-keys / --api-keys-file (one entry per line or comma
// separated):
//
//	<token>:<owner>                                 (plaintext, all scopes)
//	<token>:<owner>:jobs:write+jobs:read            (plaintext, explicit scopes)
//	sha256:<hex64>:<owner>:jobs:write+jobs:read     (production: no secret in argv)
type Key struct {
	hash   [32]byte
	Owner  string
	Scopes map[string]bool
	Label  string // short, non-secret identifier for logs (owner + first 8 hex chars)
}

// ParseKeys parses the --api-keys value (comma separated entries).
func ParseKeys(spec string) ([]Key, error) {
	return ParseKeyEntries(strings.Split(spec, ","))
}

// ParseKeyEntries parses individual entries (used for files and env vars).
func ParseKeyEntries(entries []string) ([]Key, error) {
	var keys []Key
	for _, raw := range entries {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, err := parseKey(line)
		if err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, nil
}

// ParseKeysFile reads entries from a file (one per line, `#` comments). The file
// is the recommended production form: process arguments are visible to other
// users on the host through /proc, environment and files are not.
func ParseKeysFile(path string) ([]Key, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read api keys file: %w", err)
	}
	return ParseKeyEntries(strings.Split(string(b), "\n"))
}

func parseKey(entry string) (Key, error) {
	parts := strings.Split(entry, ":")
	hashed := false
	if len(parts) > 0 && strings.EqualFold(parts[0], "sha256") {
		hashed = true
		parts = parts[1:]
	}
	if len(parts) < 2 {
		return Key{}, fmt.Errorf("invalid key entry %q: expected <token|sha256:hash>:<owner>[:scopes]", redact(entry))
	}
	secret, owner := parts[0], parts[1]
	if strings.TrimSpace(secret) == "" {
		return Key{}, fmt.Errorf("invalid key entry: empty token/hash")
	}
	if strings.TrimSpace(owner) == "" {
		return Key{}, fmt.Errorf("invalid key entry: empty owner")
	}
	scopes := AllScopes()
	if len(parts) >= 3 {
		// Scopes contain ':' themselves, so they are the remainder of the entry
		// joined back together; '+' separates multiple scopes.
		scopeSpec := strings.Join(parts[2:], ":")
		if scopeSpec != "" {
			scopes = strings.Split(strings.ToLower(scopeSpec), "+")
		}
	}
	scopeSet := map[string]bool{}
	for _, sc := range scopes {
		sc = strings.TrimSpace(sc)
		switch sc {
		case ScopeJobsWrite, ScopeJobsRead, ScopeJobsAck:
			scopeSet[sc] = true
		default:
			return Key{}, fmt.Errorf("invalid scope %q (allowed: %s)", sc,
				strings.Join(AllScopes(), ", "))
		}
	}

	k := Key{Owner: owner, Scopes: scopeSet}
	if hashed {
		raw, err := hex.DecodeString(secret)
		if err != nil || len(raw) != 32 {
			return Key{}, fmt.Errorf("sha256 key must be 64 hex characters")
		}
		copy(k.hash[:], raw)
	} else {
		k.hash = sha256.Sum256([]byte(secret))
		k.Label = owner + "/" + hex.EncodeToString(k.hash[:4])
	}
	if k.Label == "" {
		k.Label = owner + "/" + hex.EncodeToString(k.hash[:4])
	}
	return k, nil
}

// Lookup finds the key matching a plaintext token. It hashes the presented token
// once and compares in constant time against every configured key, so lookup time
// does not depend on which key matched (or on the owner).
func Lookup(keys []Key, token string) (Key, bool) {
	if token == "" {
		return Key{}, false
	}
	sum := sha256.Sum256([]byte(token))
	var found Key
	ok := false
	for _, k := range keys {
		if subtle.ConstantTimeCompare(k.hash[:], sum[:]) == 1 && !ok {
			found = k
			ok = true
		}
	}
	return found, ok
}

// HasScope reports whether the key grants a scope.
func (k Key) HasScope(scope string) bool { return k.Scopes[scope] }

// NeededScope maps a request to the scope it requires. Unknown paths default to
// jobs:read so that a write-only key cannot probe.
func NeededScope(method, path string) string {
	switch {
	case method == "POST" && (path == "/v1/jobs" || path == "/v1/parse"):
		return ScopeJobsWrite
	case method == "POST" && (strings.HasSuffix(path, "/ack") || strings.HasSuffix(path, "/cancel")):
		return ScopeJobsAck
	default:
		return ScopeJobsRead
	}
}

// redact keeps a key entry safe to put in an error message.
func redact(entry string) string {
	parts := strings.Split(entry, ":")
	for i, p := range parts {
		if i == 0 && strings.EqualFold(p, "sha256") {
			continue
		}
		if len(p) > 4 {
			parts[i] = p[:2] + "***"
		} else if p != "" {
			parts[i] = "***"
		}
	}
	return strings.Join(parts, ":")
}
