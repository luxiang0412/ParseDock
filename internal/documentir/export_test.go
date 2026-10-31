package documentir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fidelityDoc exercises every Markdown construct the renderer knows.
func fidelityDoc() *Document {
	d := New()
	ordered := map[string]string{MetaListOrdered: "true"}
	d.Elements = []Element{
		{ID: "e1", Type: ElHeader, Text: "Running header"},
		{ID: "e2", Type: ElTitle, Text: "Quarterly *Review*", RichText: "Quarterly *Review*", Level: 1},
		{ID: "e3", Type: ElParagraph, Text: "Plain bold words and a link.",
			RichText: "Plain **bold words** and a [link](https://example.com/a)."},
		{ID: "e4", Type: ElParagraph, Text: "Costs rose 5 *estimated* and on_time_pct is _raw_.\n1. not a list\n# not a heading"},
		{ID: "e5", Type: ElParagraph, Text: "Page 3 of 9", Metadata: map[string]string{MetaContentLayer: ContentLayerFurniture}},
		{ID: "e6", Type: ElListItem, Text: "first", Level: 1, Metadata: ordered},
		{ID: "e7", Type: ElListItem, Text: "second", Level: 1, Metadata: ordered},
		{ID: "e8", Type: ElListItem, Text: "nested bullet", Level: 2},
		{ID: "e9", Type: ElListItem, Text: "nested numbered", Level: 2, Metadata: map[string]string{MetaListOrdered: "true", MetaListMarker: "4."}},
		{ID: "e10", Type: ElListItem, Text: "third", Level: 1, Metadata: ordered},
		{ID: "e11", Type: ElListItem, Text: "lettered", Level: 1, Metadata: map[string]string{MetaListOrdered: "true", MetaListMarker: "a)"}},
		{ID: "e12", Type: ElParagraph, Text: "Quoted line one\nquoted line two", Metadata: map[string]string{MetaMarkdown: MarkdownBlockquote}},
		{ID: "e13", Type: ElListItem, Text: "restarts at one", Level: 1, Metadata: ordered},
		{ID: "e14", Type: ElListItem, Text: "[x] done task", Level: 1},
		{ID: "e15", Type: ElCode, Text: "fmt.Println(\"```\")", Metadata: map[string]string{MetaCodeLanguage: "go"}},
		{ID: "e16", Type: ElTable, TableID: "t1"},
		{ID: "e17", Type: ElTable, TableID: "t2"},
		{ID: "e18", Type: ElFooter, Text: "Confidential footer"},
		{ID: "e19", Type: ElParagraph, Text: "After <b>tables</b> & more."},
	}
	d.Tables = []Table{
		{ID: "t1", Rows: 2, Cols: 3, HeaderRows: 1, Cells: []Cell{
			{Row: 0, Col: 0, RowSpan: 1, ColSpan: 1, Text: "Region"},
			{Row: 0, Col: 1, RowSpan: 1, ColSpan: 1, Text: "Note", RichText: "**Note**"},
			{Row: 0, Col: 2, RowSpan: 1, ColSpan: 1, Text: "a|b"},
			{Row: 1, Col: 0, RowSpan: 1, ColSpan: 1, Text: "North"},
			{Row: 1, Col: 1, RowSpan: 1, ColSpan: 1, Text: "line one\nline two"},
			{Row: 1, Col: 2, RowSpan: 1, ColSpan: 1, Text: "x", RichText: `x\|y`},
		}},
		{ID: "t2", Rows: 3, Cols: 3, HeaderRows: 1, Cells: []Cell{
			{Row: 0, Col: 0, RowSpan: 1, ColSpan: 2, Text: "Merged <head>"},
			{Row: 0, Col: 2, RowSpan: 1, ColSpan: 1, Text: "C"},
			{Row: 1, Col: 0, RowSpan: 2, ColSpan: 1, Text: "tall"},
			{Row: 1, Col: 1, RowSpan: 1, ColSpan: 1, Text: "b1\nb2"},
			{Row: 1, Col: 2, RowSpan: 1, ColSpan: 1, Text: "c1"},
			{Row: 2, Col: 1, RowSpan: 1, ColSpan: 1, Text: "b3"},
			// c3 missing: the renderer must still emit a cell so the grid stays rectangular.
		}},
	}
	d.Finalize()
	return d
}

func TestRenderMarkdownGolden(t *testing.T) {
	got := RenderMarkdown(fidelityDoc())
	path := filepath.Join("testdata", "render_fidelity.golden.md")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (refresh with UPDATE_GOLDEN=1): %v", err)
	}
	if got != string(want) {
		t.Fatalf("markdown differs from %s (refresh with UPDATE_GOLDEN=1)\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
	if RenderMarkdown(fidelityDoc()) != got {
		t.Fatal("rendering is not deterministic")
	}
}

func TestRenderMarkdownFidelityRules(t *testing.T) {
	md := RenderMarkdown(fidelityDoc())
	for _, want := range []string{
		"# Quarterly *Review*\n",
		"Plain **bold words** and a [link](https://example.com/a).",
		// plain text is escaped where CommonMark would reinterpret it, and only there
		`Costs rose 5 \*estimated\* and on_time_pct is \_raw\_.`,
		"1\\. not a list\n\\# not a heading",
		// ordered lists keep their numbers; nested items sit at the parent's content column
		"1. first\n2. second\n   - nested bullet\n   4. nested numbered\n3. third\n- a) lettered\n",
		"> Quoted line one\n> quoted line two",
		// a quote ends the list run, so numbering restarts
		"1. restarts at one\n- [x] done task\n",
		"````go\nfmt.Println(\"```\")\n````",
		"| Region | **Note** | a\\|b |\n| --- | --- | --- |\n| North | line one<br>line two | x\\|y |",
		"<th colspan=\"2\">Merged &lt;head&gt;</th><th>C</th>",
		"<td rowspan=\"2\">tall</td><td>b1<br>b2</td><td>c1</td>",
		"<tr><td>b3</td><td></td></tr>",
		`After \<b>tables\</b> & more.`,
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
	for _, leak := range []string{"Running header", "Page 3 of 9", "Confidential footer"} {
		if strings.Contains(md, leak) {
			t.Errorf("furniture %q leaked into the Markdown body", leak)
		}
	}
}

func TestRenderTextSkipsFurnitureAndMarkup(t *testing.T) {
	txt := RenderText(fidelityDoc())
	for _, leak := range []string{"Running header", "Page 3 of 9", "Confidential footer", "**", "<br>"} {
		if strings.Contains(txt, leak) {
			t.Errorf("text view contains %q:\n%s", leak, txt)
		}
	}
	if !strings.Contains(txt, "Plain bold words and a link.") {
		t.Errorf("text view lost plain text:\n%s", txt)
	}
}

func TestEscapeLine(t *testing.T) {
	cases := map[string]string{
		"plain words":         "plain words",
		"5 * 3 = 15":          "5 * 3 = 15",
		"on_time_pct":         "on_time_pct",
		"- dash start":        `\- dash start`,
		"+ plus":              `\+ plus`,
		"* star":              `\* star`,
		"---":                 `\---`,
		"===":                 `\===`,
		"> quote":             `\> quote`,
		"## h2":               `\## h2`,
		"#hashtag":            "#hashtag",
		"12. twelve":          `12\. twelve`,
		"3) three":            `3\) three`,
		"2024 results":        "2024 results",
		"use `code`":          "use \\`code\\`",
		"[x](y)":              `[x\](y)`,
		"![a](b)":             `!\[a\](b)`,
		"a < b and c<d":       `a < b and c\<d`,
		"AT&T &amp; co":       `AT&T \&amp; co`,
		`C:\path\*`:           `C:\path\\\*`,
		"| --- | --- |":       `\| --- \| --- \|`,
		"~~gone~~":            `\~\~gone\~\~`,
		"    indented":        "indented",
		"price ~ 5":           "price ~ 5",
		"snake_case_and _x_ ": `snake_case_and \_x\_ `,
	}
	for in, want := range cases {
		if got := escapeLine(in); got != want {
			t.Errorf("escapeLine(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHTMLTableClampsAndSkipsOverlaps(t *testing.T) {
	tbl := &Table{ID: "t", Rows: 2, Cols: 2, HeaderRows: 0, Cells: []Cell{
		{Row: 0, Col: 0, RowSpan: 5, ColSpan: 9, Text: "huge"}, // spans clamp to the grid
		{Row: 1, Col: 1, RowSpan: 1, ColSpan: 1, Text: "hidden"},
	}}
	got := renderHTMLTable(tbl)
	want := "<table>\n<tbody>\n<tr><td rowspan=\"2\" colspan=\"2\">huge</td></tr>\n<tr></tr>\n</tbody>\n</table>\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
}

func TestMultiRowHeaderUsesHTML(t *testing.T) {
	tbl := &Table{ID: "t", Rows: 3, Cols: 1, HeaderRows: 2, Cells: []Cell{
		{Row: 0, Col: 0, RowSpan: 1, ColSpan: 1, Text: "h1"},
		{Row: 1, Col: 0, RowSpan: 1, ColSpan: 1, Text: "h2"},
		{Row: 2, Col: 0, RowSpan: 1, ColSpan: 1, Text: "v"},
	}}
	got := renderMarkdownTable(tbl)
	if !strings.Contains(got, "<thead>\n<tr><th>h1</th></tr>\n<tr><th>h2</th></tr>\n</thead>\n<tbody>\n<tr><td>v</td></tr>") {
		t.Fatalf("two header rows not rendered as <th>:\n%s", got)
	}
}
