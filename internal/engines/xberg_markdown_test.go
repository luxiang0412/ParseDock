package engines

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// The xberg captures in testdata/ are `xberg extract --format json --content-format
// markdown` (1.3.10) on the synthetic fixtures in benchmark/fixtures/generated, with the
// local file path replaced by "engine-input.<ext>". xberg_rich_probe.json comes from a
// self-authored DOCX with bold/italic/strike runs, a hyperlink, numbered and nested
// lists, a quote and a merged-cell table.

// replayXberg runs the real adapter against a stand-in binary that prints a captured
// JSON document, so the whole Parse path (argv, decoding, mapping) is exercised.
func replayXberg(t *testing.T, capture, format string) *documentir.Document {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", capture))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "xberg")
	script := "#!/bin/sh\nexec cat '" + abs + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	in := writeTemp(t, "input."+format, []byte("synthetic"))
	doc, err := (&Xberg{Bin: bin, VersionS: "1.3.10"}).Parse(context.Background(), Request{
		Path: in, Format: format, Mode: "fast", WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("parse %s: %v", capture, err)
	}
	// What the worker does before publishing.
	doc.DropTableShadows()
	doc.Finalize()
	return doc
}

func skeleton(doc *documentir.Document) string {
	var toks []string
	for _, el := range doc.Elements {
		if el.IsFurniture() {
			continue
		}
		switch el.Type {
		case documentir.ElTitle, documentir.ElSection:
			toks = append(toks, "H"+string(rune('0'+maxInt(el.Level, 1))))
		case documentir.ElListItem:
			toks = append(toks, "LI")
		case documentir.ElTable:
			toks = append(toks, "T")
		case documentir.ElParagraph:
			if el.Metadata[documentir.MetaMarkdown] == documentir.MarkdownBlockquote {
				toks = append(toks, "Q")
			} else {
				toks = append(toks, "P")
			}
		default:
			toks = append(toks, el.Type)
		}
	}
	return strings.Join(toks, " ")
}

func TestXbergArgsRequestMarkdownContent(t *testing.T) {
	if got := argsOf(&Xberg{}); !strings.Contains(got, "--format json --content-format markdown") {
		t.Fatalf("xberg must be asked for Markdown content, got %q", got)
	}
}

func TestXbergDocxReportOrder(t *testing.T) {
	doc := replayXberg(t, "xberg_docx_report.json", "docx")
	if got, want := skeleton(doc), "H1 P H2 LI LI LI LI H2 T P"; got != want {
		t.Fatalf("docx order = %q, want %q", got, want)
	}
	if len(doc.Tables) != 1 {
		t.Fatalf("tables = %d, want 1 (the structured copy must not be appended again)", len(doc.Tables))
	}
	md := documentir.RenderMarkdown(doc)
	if strings.Contains(md, "ParseDock benchmark fixture") {
		t.Fatal("page header reached the Markdown body")
	}
	tableAt := strings.Index(md, "| Region |")
	if tableAt < 0 || tableAt < strings.Index(md, "## Regional table") || tableAt > strings.Index(md, "Two risks need attention") {
		t.Fatalf("table not between its heading and the following paragraph:\n%s", md)
	}
}

func TestXbergPDFKeepsHeadingsPagesAndTableBBox(t *testing.T) {
	doc := replayXberg(t, "xberg_pdf_text_en_3p.json", "pdf")
	if got, want := skeleton(doc), "H1 H2 P P T H2 P P H2 P P"; got != want {
		t.Fatalf("pdf order = %q, want %q", got, want)
	}
	if len(doc.Tables) != 1 || doc.Tables[0].Loc == nil || doc.Tables[0].Loc.BBox == nil || doc.Tables[0].Loc.Page != 1 {
		t.Fatalf("table lost its page/bbox: %+v", doc.Tables)
	}
	pages := map[int]int{}
	for _, el := range doc.Elements {
		if el.Loc == nil || el.Loc.Kind != "pdf" || el.Loc.Page == 0 {
			t.Fatalf("element without a page: %+v", el)
		}
		pages[el.Loc.Page]++
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %v, want 3", pages)
	}
	// xberg's Markdown breaks a paragraph at a source line that starts with a number;
	// the repair joins it back, so no paragraph starts mid-sentence.
	for _, el := range doc.Elements {
		if el.Type == documentir.ElParagraph && (strings.HasPrefix(el.Text, "4.2 hours") || strings.HasPrefix(el.Text, "2.4.1, which")) {
			t.Fatalf("split paragraph not repaired: %q", el.Text)
		}
	}
	if txt := documentir.RenderText(doc); !strings.Contains(txt, "fell from 4.2 hours") {
		t.Fatalf("joined sentence missing from text view:\n%s", txt)
	}
}

func TestXbergXLSXSheetsStayStructured(t *testing.T) {
	doc := replayXberg(t, "xberg_xlsx_workbook.json", "xlsx")
	if got, want := skeleton(doc), "H2 T H2 T"; got != want {
		t.Fatalf("xlsx order = %q, want %q", got, want)
	}
	if doc.Elements[0].Text != "Summary" || doc.Elements[2].Text != "Detail" {
		t.Fatalf("sheet headings = %q, %q", doc.Elements[0].Text, doc.Elements[2].Text)
	}
	if len(doc.Tables) != 2 || doc.Tables[0].Loc.Page != 1 || doc.Tables[1].Loc.Page != 2 || doc.Tables[0].Loc.Kind != "sheet" {
		t.Fatalf("tables lost their sheet index: %+v", doc.Tables)
	}
	// Empty spreadsheet rows are data rows, not GFM delimiter rows.
	if doc.Tables[0].Rows != 9 || doc.Tables[0].Cols != 4 {
		t.Fatalf("summary table %dx%d, want 9x4", doc.Tables[0].Rows, doc.Tables[0].Cols)
	}
}

func TestXbergContentPathDoesNotDuplicateTables(t *testing.T) {
	doc := replayXberg(t, "xberg_csv_utf8.json", "csv")
	if got := skeleton(doc); got != "T" {
		t.Fatalf("csv = %q, want a single table", got)
	}
	if len(doc.Tables) != 1 || doc.Tables[0].Rows != 5 {
		t.Fatalf("tables = %+v", doc.Tables)
	}
}

func TestXbergRichProbeKeepsInlineFormatting(t *testing.T) {
	doc := replayXberg(t, "xberg_rich_probe.json", "docx")
	if got, want := skeleton(doc), "H1 P P LI LI LI LI LI Q P T P"; got != want {
		t.Fatalf("rich order = %q, want %q", got, want)
	}
	md := documentir.RenderMarkdown(doc)
	for _, want := range []string{
		"Plain start **bold words** middle *italic words* and ~~struck~~ end.",
		"See [the docs](https://example.com/docs) for more.",
		"1. First numbered\n2. Second numbered\n3. Third numbered\n",
		"> A quoted remark.",
		"**Whole bold paragraph.**",
		// the structured cell restores the line break the Markdown folded into a space
		"| r1a | line one<br>line two | x\\|y |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n%s", want, md)
		}
	}
	txt := documentir.RenderText(doc)
	if strings.Contains(txt, "**") || strings.Contains(txt, "](") || !strings.Contains(txt, "Plain start bold words middle italic words and struck end.") {
		t.Fatalf("text view carries markup or lost words:\n%s", txt)
	}
}

func TestJoinSplitParagraphsIsConservative(t *testing.T) {
	long := "The pilot program moved pallets across three regional hubs and the time fell from"
	cases := []struct {
		prev, next string
		join       bool
	}{
		{long, "4.2 hours to 3.1 hours after the scheduling change, while", true},
		{long + ".", "4.2 hours to 3.1 hours after the scheduling change, while", false}, // sentence ended
		{long, "hours to 3.1 hours after the scheduling change", false},                  // not the digit split
		{"Total amount", "18,420.60 EUR due on the thirtieth of November", false},        // label, not prose
		{long, "2026", false}, // a lone figure
	}
	for _, c := range cases {
		d := documentir.New()
		d.Elements = []documentir.Element{
			{ID: "a", Type: documentir.ElParagraph, Text: c.prev},
			{ID: "b", Type: documentir.ElParagraph, Text: c.next},
		}
		if got := joinSplitParagraphs(d) == 1; got != c.join {
			t.Errorf("join(%q, %q) = %v, want %v", c.prev, c.next, got, c.join)
		}
	}
}

func TestJoinSplitParagraphsSeparatorAndEscaping(t *testing.T) {
	zh := "本季度三个区域枢纽共处理托盘十二万八千四百个，同比增长百分之十二点五。调整排程后平均时间下降，并且"
	d := documentir.New()
	d.Elements = []documentir.Element{
		{ID: "el_1", Type: documentir.ElParagraph, Text: zh},
		{ID: "el_2", Type: documentir.ElParagraph, Text: "2026年第三季度的计划已经提交并通过评审，后续每周跟踪进度。"},
	}
	if n := joinSplitParagraphs(d); n != 1 {
		t.Fatalf("joined %d, want 1", n)
	}
	if want := zh + "2026年第三季度的计划已经提交并通过评审，后续每周跟踪进度。"; d.Elements[0].Text != want {
		t.Fatalf("CJK join inserted a separator: %q", d.Elements[0].Text)
	}

	en := "The cold-chain corridor depends on a single carrier and *any* interruption would delay roughly"
	d = documentir.New()
	d.Elements = []documentir.Element{
		{ID: "el_1", Type: documentir.ElParagraph, Text: en},
		{ID: "el_2", Type: documentir.ElParagraph, Text: "900 shipments per week across the network.", RichText: "900 shipments per **week** across the network."},
	}
	if n := joinSplitParagraphs(d); n != 1 {
		t.Fatalf("joined %d, want 1", n)
	}
	got := d.Elements[0].RichText
	if !strings.Contains(got, `\*any\*`) || !strings.Contains(got, "roughly 900") || !strings.Contains(got, "**week**") {
		t.Fatalf("rich text join wrong: %q", got)
	}
}
