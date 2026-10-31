package jobs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func TestNewIDShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 10000; i++ {
		id := NewID("job_")
		body, ok := strings.CutPrefix(id, "job_")
		if !ok {
			t.Fatalf("%q lacks its prefix", id)
		}
		// 16 bytes (48-bit millis + 80 random bits) in unpadded base32 = 26 characters.
		if len(body) != 26 {
			t.Fatalf("%q: body length %d, want 26", id, len(body))
		}
		for _, r := range body {
			if !strings.ContainsRune(crockford, r) {
				t.Fatalf("%q contains %q outside the Crockford alphabet", id, r)
			}
		}
		if seen[id] {
			t.Fatalf("duplicate id %q after %d draws", id, i)
		}
		seen[id] = true
	}
}

func TestNewIDTimePrefixOrders(t *testing.T) {
	a := NewID("")
	time.Sleep(3 * time.Millisecond)
	b := NewID("")
	// The first 10 characters encode the millisecond timestamp (big-endian), so IDs minted
	// at different milliseconds sort chronologically.
	if a[:10] >= b[:10] {
		t.Fatalf("time prefix not increasing: %q then %q", a, b)
	}
}

func TestIDHelpersUseDistinctPrefixes(t *testing.T) {
	for prefix, gen := range map[string]func() string{
		"job_": NewJobID, "dl_": NewLeaseID, "lt_": NewToken, "wrk_": NewWorkerID,
	} {
		if v := gen(); !strings.HasPrefix(v, prefix) || len(v) != len(prefix)+26 {
			t.Fatalf("%q: want prefix %q and 26 id characters", v, prefix)
		}
	}
	if a, b := NewToken(), NewToken(); a == b {
		t.Fatal("two lease tokens are equal")
	}
}

func TestValidateModeAndDevice(t *testing.T) {
	for in, want := range map[string]string{"": ModeAuto, " FAST ": ModeFast, "accurate": ModeAccurate, "auto": ModeAuto} {
		if got, err := ValidateMode(in); err != nil || got != want {
			t.Fatalf("ValidateMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ValidateMode("turbo"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	for in, want := range map[string]string{"": DeviceAuto, "CPU": DeviceCPU, "gpu_preferred": DeviceGPUPreferred,
		"gpu_required": DeviceGPURequired} {
		if got, err := ValidateDevice(in); err != nil || got != want {
			t.Fatalf("ValidateDevice(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ValidateDevice("tpu"); err == nil {
		t.Fatal("unknown device accepted")
	}
}

// The known-code set must list exactly the Err* string constants declared in this package.
func TestKnownErrorCodesMatchConstants(t *testing.T) {
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	declared := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, name := range vs.Names {
					if !strings.HasPrefix(name.Name, "Err") || i >= len(vs.Values) {
						continue
					}
					if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
						v, _ := strconv.Unquote(lit.Value)
						declared[v] = true
					}
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("no Err* constants found; the parser walk is broken")
	}
	for code := range declared {
		if !IsKnownErrorCode(code) {
			t.Errorf("error code %q is declared but missing from knownErrorCodes", code)
		}
	}
	for code := range knownErrorCodes {
		if !declared[code] {
			t.Errorf("knownErrorCodes lists %q, which is not a declared constant", code)
		}
	}
	if IsKnownErrorCode("totally_made_up") || IsKnownErrorCode("") {
		t.Error("unknown code accepted")
	}
}

// Review finding 10: keys longer than 200 characters were truncated, so two distinct keys
// sharing a 200-character prefix collided and replayed one operation as the other.
func TestNormalizeIdempotencyKey(t *testing.T) {
	if k, err := NormalizeIdempotencyKey("  op-1 \t"); err != nil || k != "op-1" {
		t.Fatalf("trim: %q, %v", k, err)
	}
	if k, err := NormalizeIdempotencyKey(""); err != nil || k != "" {
		t.Fatalf("empty: %q, %v", k, err)
	}
	exact := strings.Repeat("k", MaxIdempotencyKeyLen)
	if k, err := NormalizeIdempotencyKey(exact); err != nil || k != exact {
		t.Fatalf("200-character key rejected or altered: %v", err)
	}
	// The limit counts characters, not bytes: 200 three-byte runes are allowed.
	wide := strings.Repeat("键", MaxIdempotencyKeyLen)
	if _, err := NormalizeIdempotencyKey(wide); err != nil {
		t.Fatalf("200-character multibyte key rejected: %v", err)
	}
	for _, long := range []string{exact + "a", exact + "b", wide + "键"} {
		if k, err := NormalizeIdempotencyKey(long); err == nil {
			t.Fatalf("over-long key accepted as %q", k)
		}
	}
}
