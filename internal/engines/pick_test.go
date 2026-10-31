package engines

import (
	"context"
	"encoding/json"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// stubEngine is a minimal Engine for routing tests.
type stubEngine struct {
	name    string
	formats []string
	modes   []string
}

func (s stubEngine) Name() string                { return s.name }
func (s stubEngine) Version() string             { return "1" }
func (s stubEngine) Device() string              { return "cpu" }
func (s stubEngine) Formats() []string           { return s.formats }
func (s stubEngine) Modes() []string             { return s.modes }
func (s stubEngine) Probe(context.Context) error { return nil }
func (s stubEngine) Parse(context.Context, Request) (*documentir.Document, error) {
	return documentir.New(), nil
}

// Review finding: `mode == "" || mode == "auto" && Supports(...)` let an unspecified mode
// pick an engine that declares no mode at all; the auto fallback requires a declared mode.
func TestPickFallbackRequiresDeclaredMode(t *testing.T) {
	reg := NewRegistry(stubEngine{name: "nomode", formats: []string{"pdf"}})
	for _, mode := range []string{"", "auto"} {
		if e, err := reg.Pick("", "pdf", mode); err == nil {
			t.Fatalf("mode %q picked %s, which declares no mode", mode, e.Name())
		}
	}
	reg = NewRegistry(
		stubEngine{name: "nomode", formats: []string{"pdf"}},
		stubEngine{name: "custom", formats: []string{"pdf"}, modes: []string{"bespoke"}},
	)
	for _, mode := range []string{"", "auto"} {
		e, err := reg.Pick("", "pdf", mode)
		if err != nil || e.Name() != "custom" {
			t.Fatalf("mode %q: got %v, %v; want custom", mode, e, err)
		}
	}
}

// Review finding: hasWildcard ignored its format, so an engine listing the format
// explicitly *and* "*" lost to a wildcard-only engine on name order.
func TestPickPrefersExplicitFormatOverWildcard(t *testing.T) {
	reg := NewRegistry(
		stubEngine{name: "a-wild", formats: []string{"*"}, modes: []string{"fast"}},
		stubEngine{name: "b-exact", formats: []string{"pdf", "*"}, modes: []string{"fast"}},
	)
	e, err := reg.Pick("", "pdf", "fast")
	if err != nil || e.Name() != "b-exact" {
		t.Fatalf("got %v, %v; want b-exact", e, err)
	}
	if !hasWildcard(stubEngine{formats: []string{"*"}}, "pdf") || hasWildcard(stubEngine{formats: []string{"pdf", "*"}}, "pdf") {
		t.Fatal("hasWildcard does not consider the format")
	}
}

// Review finding: a plain-text paragraph recorded the line where it ended (the blank line
// after it) instead of the line where it starts.
func TestLiftPlainTextRecordsStartLine(t *testing.T) {
	doc := documentir.New()
	liftPlainText(doc, "first\nstill first\n\n\nsecond\n")
	if len(doc.Elements) != 2 {
		t.Fatalf("elements = %+v", doc.Elements)
	}
	if got := doc.Elements[0].Loc.Line; got != 1 {
		t.Fatalf("first paragraph line = %d, want 1", got)
	}
	if got := doc.Elements[1].Loc.Line; got != 5 {
		t.Fatalf("second paragraph line = %d, want 5", got)
	}
}

// Review finding: xberg tables were always tagged kind "pdf"; a pptx table is on a slide.
func TestXbergTableKindFollowsFormat(t *testing.T) {
	var out xbergOutput
	if err := json.Unmarshal([]byte(`{"result":{"tables":[{"cells":[["a","b"],["1","2"]],"page_number":2}]}}`), &out); err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	(&Xberg{}).attachTables(doc, &out, "pptx", 2, false)
	if len(doc.Tables) != 1 || doc.Tables[0].Loc == nil || doc.Tables[0].Loc.Kind != "slide" {
		t.Fatalf("tables = %+v", doc.Tables)
	}
}
