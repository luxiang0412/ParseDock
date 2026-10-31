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

// Audit (P1 DoS): rendering materialises Rows x Cols, so the declared shape is validated
// before anything allocates.
func TestLimitsRejectGiganticTable(t *testing.T) {
	d := &Document{SchemaVersion: "doc-ir/0.1", Tables: []Table{
		{ID: "t1", Rows: 200_000, Cols: 100_000}, // 20 billion cells
	}}
	if err := DefaultLimits.Validate(d); err == nil {
		t.Fatal("a 20-billion-cell table was accepted")
	}
	// Rendering the same table must not allocate the grid.
	if g := tableGrid(&d.Tables[0]); g != nil {
		t.Fatalf("renderer allocated a %d-row grid", len(g))
	}
	// A realistic document passes.
	ok := &Document{SchemaVersion: "doc-ir/0.1",
		Elements: []Element{{ID: "e1", Type: ElParagraph, Text: "hello"}},
		Tables:   []Table{{ID: "t1", Rows: 40, Cols: 8}}}
	if err := DefaultLimits.Validate(ok); err != nil {
		t.Fatalf("realistic document rejected: %v", err)
	}
}

func TestLimitsRejectNegativeAndOverflow(t *testing.T) {
	for _, tbl := range []Table{
		{ID: "neg", Rows: -1, Cols: 4},
		{ID: "overflow", Rows: 1 << 40, Cols: 1 << 40},
	} {
		d := &Document{Tables: []Table{tbl}}
		if err := DefaultLimits.Validate(d); err == nil {
			t.Fatalf("%s was accepted", tbl.ID)
		}
	}
}

// Audit: duplicate table IDs made the text renderer and the validation disagree, and let a
// document bypass the rendered-area bound (each element re-renders the first table).
func TestLimitsRejectDuplicateTableIDs(t *testing.T) {
	d := &Document{SchemaVersion: "doc-ir/0.1", Tables: []Table{
		{ID: "t", Rows: 100_000, Cols: 100},
		{ID: "t", Rows: 1, Cols: 1},
	}}
	if err := DefaultLimits.Validate(d); err == nil {
		t.Fatal("duplicate table ids were accepted")
	}
}

// Audit follow-up: engines return a table both as text lines and as a Table, so the rendered
// output repeated every cell twice. The redundant lines are the run of paragraphs directly
// before the table element, one per row, in row order.
func TestDropTableShadows(t *testing.T) {
	d := &Document{
		Tables: []Table{{ID: "tbl_1", Rows: 2, Cols: 2, HeaderRows: 1, Cells: []Cell{
			{Row: 0, Col: 0, Text: "Region"}, {Row: 0, Col: 1, Text: "Pallets"},
			{Row: 1, Col: 0, Text: "North"}, {Row: 1, Col: 1, Text: "48,120"},
		}}},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Intro paragraph"},
			{ID: "el_2", Type: ElParagraph, Text: "Region Pallets"},
			{ID: "el_3", Type: ElParagraph, Text: "North 48,120"},
			{ID: "el_4", Type: ElTable, TableID: "tbl_1"},
			{ID: "el_5", Type: ElParagraph, Text: "North 48,120 is also mentioned here, but not as the table."},
		},
	}
	if n := d.DropTableShadows(); n != 2 {
		t.Fatalf("removed %d elements, want 2", n)
	}
	if len(d.Elements) != 3 {
		t.Fatalf("elements = %d, want 3", len(d.Elements))
	}
	if d.Elements[1].Type != ElTable {
		t.Fatalf("table element lost: %+v", d.Elements)
	}
}

func TestDropTableShadowsKeepsProse(t *testing.T) {
	d := &Document{
		Tables: []Table{{ID: "tbl_1", Rows: 1, Cols: 2, Cells: []Cell{
			{Row: 0, Col: 0, Text: "a"}, {Row: 0, Col: 1, Text: "b"},
		}}},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "a b"},
			{ID: "el_2", Type: ElParagraph, Text: "something else"},
			{ID: "el_3", Type: ElTable, TableID: "tbl_1"},
		},
	}
	// Not adjacent: the matching paragraph is separated from the table by another paragraph.
	if n := d.DropTableShadows(); n != 0 {
		t.Fatalf("removed %d elements, want 0", n)
	}
}

// Office engines return the table block as one paragraph with embedded newlines and tabs;
// that is still the same table text and must not be rendered twice.
func TestDropTableShadowBlock(t *testing.T) {
	d := &Document{
		Tables: []Table{{ID: "tbl_1", Rows: 2, Cols: 2, HeaderRows: 1, Cells: []Cell{
			{Row: 0, Col: 0, Text: "Region"}, {Row: 0, Col: 1, Text: "Pallets"},
			{Row: 1, Col: 0, Text: "North"}, {Row: 1, Col: 1, Text: "48,120"},
		}}},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Intro"},
			{ID: "el_2", Type: ElParagraph, Text: "Region\tPallets\nNorth\t48,120"},
			{ID: "el_3", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 1 {
		t.Fatalf("removed %d elements, want 1", n)
	}
	if len(d.Elements) != 2 || d.Elements[1].Type != ElTable {
		t.Fatalf("unexpected elements: %+v", d.Elements)
	}
}

// Office exports attach the table element at the end of the page while the rows stay in the
// text, so the duplicate is not adjacent to the table element. The table must take the place
// of its rows - not simply survive at the end, which is how tables ended up at the bottom of a
// section while their rows vanished from the middle.
func TestDropTableShadowMovesTableIntoPlace(t *testing.T) {
	d := &Document{
		Tables: []Table{{ID: "tbl_1", Rows: 3, Cols: 2, HeaderRows: 1, Cells: []Cell{
			{Row: 0, Col: 0, Text: "Region"}, {Row: 0, Col: 1, Text: "Pallets"},
			{Row: 1, Col: 0, Text: "North"}, {Row: 1, Col: 1, Text: "48,120"},
			{Row: 2, Col: 0, Text: "Total"}, {Row: 2, Col: 1, Text: "48,120"},
		}}},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Region\tPallets"},
			{ID: "el_2", Type: ElParagraph, Text: "North\t48,120"},
			{ID: "el_3", Type: ElParagraph, Text: "Total\t48,120"},
			{ID: "el_4", Type: ElParagraph, Text: "Two risks need attention."},
			{ID: "el_5", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 3 {
		t.Fatalf("removed %d elements, want 3", n)
	}
	if len(d.Elements) != 2 {
		t.Fatalf("elements = %d, want 2 (%+v)", len(d.Elements), d.Elements)
	}
	if d.Elements[0].Type != ElTable || d.Elements[0].TableID != "tbl_1" {
		t.Fatalf("table did not move to where its rows were: %+v", d.Elements)
	}
	if d.Elements[1].Type != ElParagraph || d.Elements[1].Text != "Two risks need attention." {
		t.Fatalf("following paragraph lost or reordered: %+v", d.Elements)
	}
}

// A body paragraph before the table rows must stay before the moved table, and a body
// paragraph after them must stay after it.
func TestDropTableShadowKeepsSurroundingOrder(t *testing.T) {
	d := &Document{
		Tables: []Table{{ID: "tbl_1", Rows: 2, Cols: 2, Cells: []Cell{
			{Row: 0, Col: 0, Text: "Region"}, {Row: 0, Col: 1, Text: "Pallets"},
			{Row: 1, Col: 0, Text: "North"}, {Row: 1, Col: 1, Text: "48,120"},
		}}},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Financial indicators"},
			{ID: "el_2", Type: ElParagraph, Text: "Region Pallets"},
			{ID: "el_3", Type: ElParagraph, Text: "North 48,120"},
			{ID: "el_4", Type: ElParagraph, Text: "Business segments"},
			{ID: "el_5", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 2 {
		t.Fatalf("removed %d elements, want 2", n)
	}
	if len(d.Elements) != 3 {
		t.Fatalf("elements = %d, want 3", len(d.Elements))
	}
	order := []string{d.Elements[0].Type, d.Elements[1].Type, d.Elements[2].Type}
	if order[0] != ElParagraph || order[1] != ElTable || order[2] != ElParagraph {
		t.Fatalf("order = %v, want paragraph, table, paragraph", order)
	}
	if d.Elements[0].Text != "Financial indicators" || d.Elements[2].Text != "Business segments" {
		t.Fatalf("surrounding paragraphs changed: %+v", d.Elements)
	}
}

// A table that is already in the right place (markdown lift interleaves it) is left alone.
func TestDropTableShadowLeavesInterleavedTableAlone(t *testing.T) {
	d := &Document{
		Tables: []Table{{ID: "tbl_1", Rows: 2, Cols: 1, Cells: []Cell{
			{Row: 0, Col: 0, Text: "a"}, {Row: 1, Col: 0, Text: "b"},
		}}},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "intro"},
			{ID: "el_2", Type: ElTable, TableID: "tbl_1", Text: "2 rows x 1 cols"},
			{ID: "el_3", Type: ElParagraph, Text: "after"},
		},
	}
	if n := d.DropTableShadows(); n != 0 {
		t.Fatalf("removed %d elements, want 0", n)
	}
	if len(d.Elements) != 3 || d.Elements[1].Type != ElTable {
		t.Fatalf("interleaved table disturbed: %+v", d.Elements)
	}
}

// Two identical tables in one document must not delete each other's text.
func TestDropTableShadowKeepsSecondTableText(t *testing.T) {
	table := Table{ID: "t1", Rows: 2, Cols: 1, Cells: []Cell{
		{Row: 0, Col: 0, Text: "a"}, {Row: 1, Col: 0, Text: "b"},
	}}
	second := table
	second.ID = "t2"
	d := &Document{
		Tables: []Table{table, second},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "a"},
			{ID: "el_2", Type: ElParagraph, Text: "b"},
			{ID: "el_3", Type: ElTable, TableID: "t1"},
			{ID: "el_4", Type: ElParagraph, Text: "a"},
			{ID: "el_5", Type: ElParagraph, Text: "b"},
			{ID: "el_6", Type: ElTable, TableID: "t2"},
		},
	}
	if n := d.DropTableShadows(); n != 4 {
		t.Fatalf("removed %d elements, want 4", n)
	}
	if len(d.Elements) != 2 {
		t.Fatalf("elements = %+v", d.Elements)
	}
}

// Real-world shape that produced "tables at the bottom of the section": xberg
// returns the page body as one paragraph whose middle block is the table written
// row per line, plus a tables array appended after every body element. The header
// row is reformatted by the engine, so only the body rows match exactly.
func TestDropTableShadowRelocatesBlockWithReformattedHeader(t *testing.T) {
	rows := [][]string{
		{"Metric", "Current", "Prior"},
		{"Revenue", "1,284", "1,150"},
		{"Margin", "18.4%", "17.9%"},
		{"Headcount", "412", "398"},
	}
	cells := []Cell{}
	for r, row := range rows {
		for c, v := range row {
			cells = append(cells, Cell{Row: r, Col: c, Text: v})
		}
	}
	// The engine's own text for the header row differs (e.g. spacing/marks).
	block := "Metric*  Current  Prior\n" +
		"Revenue 1,284 1,150\n" +
		"Margin 18.4% 17.9%\n" +
		"Headcount 412 398"
	d := &Document{
		Tables: []Table{{ID: "tbl_1", Rows: len(rows), Cols: 3, HeaderRows: 1, Cells: cells}},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Financial indicators"},
			{ID: "el_2", Type: ElParagraph, Text: block},
			{ID: "el_3", Type: ElParagraph, Text: "Business segments"},
			{ID: "el_4", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 1 {
		t.Fatalf("removed %d elements, want 1", n)
	}
	if len(d.Elements) != 3 {
		t.Fatalf("elements = %d, want 3", len(d.Elements))
	}
	if d.Elements[0].Text != "Financial indicators" {
		t.Fatalf("preceding paragraph moved: %+v", d.Elements[0])
	}
	if d.Elements[1].Type != ElTable {
		t.Fatalf("table not relocated after its section heading: %+v", d.Elements)
	}
	if d.Elements[2].Text != "Business segments" {
		t.Fatalf("following paragraph moved: %+v", d.Elements[2])
	}
}

// Two tables appended by the engine must each find their own rows; the second
// element sits after every body element, so a single shared window would starve it.
func TestDropTableShadowRelocatesTwoAppendedTables(t *testing.T) {
	mk := func(id string, body ...string) Table {
		tbl := Table{ID: id, Rows: len(body) + 1, Cols: 2}
		tbl.Cells = append(tbl.Cells, Cell{Row: 0, Col: 0, Text: "K"}, Cell{Row: 0, Col: 1, Text: "V"})
		for i, line := range body {
			parts := strings.SplitN(line, " ", 2)
			tbl.Cells = append(tbl.Cells,
				Cell{Row: i + 1, Col: 0, Text: parts[0]}, Cell{Row: i + 1, Col: 1, Text: parts[1]})
		}
		return tbl
	}
	d := &Document{
		Tables: []Table{mk("t1", "a 1", "b 2"), mk("t2", "c 3", "d 4")},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "first section"},
			{ID: "el_2", Type: ElParagraph, Text: "K V\na 1\nb 2"},
			{ID: "el_3", Type: ElParagraph, Text: "second section"},
			{ID: "el_4", Type: ElParagraph, Text: "K V\nc 3\nd 4"},
			{ID: "el_5", Type: ElTable, TableID: "t1"},
			{ID: "el_6", Type: ElTable, TableID: "t2"},
		},
	}
	if n := d.DropTableShadows(); n != 2 {
		t.Fatalf("removed %d elements, want 2", n)
	}
	want := []string{ElParagraph, ElTable, ElParagraph, ElTable}
	if len(d.Elements) != len(want) {
		t.Fatalf("elements = %d, want %d", len(d.Elements), len(want))
	}
	for i, typ := range want {
		if d.Elements[i].Type != typ {
			t.Fatalf("position %d = %s, want %s (%+v)", i, d.Elements[i].Type, typ, d.Elements)
		}
	}
	if d.Elements[1].TableID != "t1" || d.Elements[3].TableID != "t2" {
		t.Fatalf("tables in wrong order: %+v", d.Elements)
	}
}

// rich_text is rendered, so it shares the text budget: a document cannot smuggle an
// unbounded second copy of its content past the limit.
func TestLimitsCountRichText(t *testing.T) {
	l := Limits{MaxElements: 10, MaxTables: 10, MaxTableCells: 100, MaxTextBytes: 10}
	ok := &Document{Elements: []Element{{ID: "e1", Type: ElParagraph, Text: "hello"}}}
	if err := l.Validate(ok); err != nil {
		t.Fatalf("5 bytes rejected: %v", err)
	}
	ok.Elements[0].RichText = "**hello**"
	if err := l.Validate(ok); err == nil {
		t.Fatal("element rich_text was not counted against MaxTextBytes")
	}
	cells := &Document{Tables: []Table{{ID: "t1", Rows: 1, Cols: 1,
		Cells: []Cell{{RowSpan: 1, ColSpan: 1, Text: "abc", RichText: "**abc** and more"}}}}}
	if err := l.Validate(cells); err == nil {
		t.Fatal("cell rich_text was not counted against MaxTextBytes")
	}
}
