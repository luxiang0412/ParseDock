package documentir

import (
	"encoding/json"
	"strings"
	"testing"
)

func sample() *Document {
	d := New()
	d.ParserEngine = "test"
	d.ParserVersion = "1"
	d.PageKind = "pdf"
	d.PageCount = 1
	d.Elements = []Element{
		{ID: "el_1", Type: ElTitle, Text: "Report", Level: 1},
		{ID: "el_2", Type: ElParagraph, Text: "Body text."},
		{ID: "el_3", Type: ElListItem, Text: "first", Level: 1},
		{ID: "el_4", Type: ElListItem, Text: "nested", Level: 2},
		{ID: "el_5", Type: ElTable, TableID: "tbl_1"},
		{ID: "el_6", Type: ElPageBreak},
		{ID: "el_7", Type: ElParagraph, Text: "After the table."},
	}
	conf := 0.9
	d.Tables = []Table{{
		ID: "tbl_1", Rows: 2, Cols: 2, HeaderRows: 1,
		Cells: []Cell{
			{Row: 0, Col: 0, RowSpan: 1, ColSpan: 1, Text: "A"},
			{Row: 0, Col: 1, RowSpan: 1, ColSpan: 1, Text: "B"},
			{Row: 1, Col: 0, RowSpan: 1, ColSpan: 1, Text: "1"},
			{Row: 1, Col: 1, RowSpan: 1, ColSpan: 1, Text: "2"},
		},
	}}
	d.Elements[0].Confidence = &conf
	d.Finalize()
	return d
}

func TestRenderMarkdown(t *testing.T) {
	md := RenderMarkdown(sample())
	for _, want := range []string{
		"# Report\n\n",
		"Body text.\n\n",
		"- first\n  - nested\n",
		"| A | B |\n| --- | --- |\n| 1 | 2 |",
		"\n---\n",
		"After the table.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
	if strings.Contains(md, "header") {
		t.Error("page_break rendered as text")
	}
}

func TestRenderText(t *testing.T) {
	txt := RenderText(sample())
	if !strings.Contains(txt, "A\tB") || !strings.Contains(txt, "1\t2") {
		t.Errorf("table rows not tab separated:\n%s", txt)
	}
	if !strings.Contains(txt, "  - nested") {
		t.Errorf("nested list indentation lost:\n%s", txt)
	}
}

func TestFinalizeRenumbersAndCounts(t *testing.T) {
	d := sample()
	for i, el := range d.Elements {
		if el.ReadingOrder != i {
			t.Fatalf("element %d reading_order=%d", i, el.ReadingOrder)
		}
	}
	if d.Metrics.OutputChars == 0 {
		t.Fatal("output_chars not computed")
	}
	if d.Metrics.Pages != d.PageCount {
		t.Fatalf("metrics.pages=%d page_count=%d", d.Metrics.Pages, d.PageCount)
	}
}

func TestMissingProvenanceIsAbsent(t *testing.T) {
	d := sample()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	// A document without engine coordinates must not carry a synthesised bbox.
	if strings.Contains(string(b), `"bbox"`) {
		t.Fatal("bbox invented for elements without geometry")
	}
	if !strings.Contains(string(b), `"schema_version":"`+SchemaVersion+`"`) {
		t.Fatal("schema version missing")
	}
}

func TestParseFormat(t *testing.T) {
	for in, want := range map[string]Format{
		"": FormatMarkdown, "markdown": FormatMarkdown, "MD": FormatMarkdown,
		"text": FormatText, "json": FormatJSON, " JSON ": FormatJSON,
	} {
		got, ok := ParseFormat(in)
		if !ok || got != want {
			t.Errorf("ParseFormat(%q) = %q,%v want %q", in, got, ok, want)
		}
	}
	if _, ok := ParseFormat("html"); ok {
		t.Error("html should not be a valid result format")
	}
}

func TestTableGridSkipsOutOfRangeCells(t *testing.T) {
	d := sample()
	d.Tables[0].Cells = append(d.Tables[0].Cells, Cell{Row: 99, Col: 99, Text: "ghost"})
	md := RenderMarkdown(d)
	if strings.Contains(md, "ghost") {
		t.Fatal("out-of-range cell leaked into the grid")
	}
}

// PR-04 codex follow-up: the escalation comparison must include table cells,
// otherwise a retry could drop extracted tables and still look "larger".
func TestTextLengthIncludesTables(t *testing.T) {
	d := sample()
	without := d.TextLength()
	d.Tables[0].Cells = append(d.Tables[0].Cells,
		Cell{Row: 1, Col: 1, Text: strings.Repeat("x", 50)})
	if got := d.TextLength(); got != without+50 {
		t.Fatalf("TextLength = %d, want %d", got, without+50)
	}
}
