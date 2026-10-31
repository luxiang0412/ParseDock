package engines

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// The docling captures in testdata/ are `docling-rs --to json` (2.53.3) output:
// docling_docx_report.json from benchmark/fixtures/generated/docx_report.docx, and the
// two *_probe.json files from self-authored DOCX probes (formatted runs, a hyperlink,
// numbered and nested lists, a quote, a merged-cell table, a page footer; formatted runs
// inside a list item and a table cell).

func mapCapture(t *testing.T, name, kind string) *documentir.Document {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	if err := mapDocling(doc, raw, kind); err != nil {
		t.Fatal(err)
	}
	doc.DropTableShadows()
	doc.Finalize()
	return doc
}

// docling-rs puts a DOCX table under the section header that introduces it
// (texts/7 -> tables/0), with no provenance and no text anchor. The old mapper only
// followed text children of text nodes and appended the table at the end.
func TestDoclingDocxReportOrder(t *testing.T) {
	doc := mapCapture(t, "docling_docx_report.json", "none")
	if got, want := skeleton(doc), "H1 P H2 LI LI LI LI H2 T P"; got != want {
		t.Fatalf("docx order = %q, want %q", got, want)
	}
	// The page header is kept in JSON, typed and marked as furniture...
	var header *documentir.Element
	for i := range doc.Elements {
		if doc.Elements[i].Text == "ParseDock benchmark fixture" {
			header = &doc.Elements[i]
		}
	}
	if header == nil || header.Type != documentir.ElHeader || header.Metadata[documentir.MetaContentLayer] != documentir.ContentLayerFurniture {
		t.Fatalf("page header not kept as furniture: %+v", header)
	}
	// ...and never reaches the Markdown or text body.
	for _, out := range []string{documentir.RenderMarkdown(doc), documentir.RenderText(doc)} {
		if strings.Contains(out, "ParseDock benchmark fixture") {
			t.Fatalf("page header leaked into the body:\n%s", out)
		}
	}
	md := documentir.RenderMarkdown(doc)
	if !strings.Contains(md, "## Regional table\n\n| Region | Pallets |") {
		t.Fatalf("table is not under its heading:\n%s", md)
	}
}

func TestDoclingRichProbe(t *testing.T) {
	doc := mapCapture(t, "docling_rich_probe.json", "none")
	if got, want := skeleton(doc), "H1 P P LI LI LI LI LI P P T P"; got != want {
		t.Fatalf("rich order = %q, want %q", got, want)
	}
	md := documentir.RenderMarkdown(doc)
	for _, want := range []string{
		// formatted runs are one paragraph, not seven
		"Plain start **bold words** middle *italic words* and ~~struck~~ end.\n",
		"See [the docs](https://example.com/docs) for more.\n",
		"1. First numbered\n2. Second numbered\n3. Third numbered\n- Bullet one\n  - Nested bullet\n",
		"**Whole bold paragraph.**",
		"<th colspan=\"2\">Merged head</th><th>C</th>",
		"<td>line one<br>line two</td><td>x|y</td>",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n%s", want, md)
		}
	}
	if strings.Contains(md, "Footer text") {
		t.Fatal("page footer leaked into the Markdown body")
	}
	txt := documentir.RenderText(doc)
	if !strings.Contains(txt, "Plain start bold words middle italic words and struck end.") || strings.Contains(txt, "**") {
		t.Fatalf("text view must be plain:\n%s", txt)
	}
	for _, el := range doc.Elements {
		if el.Text == "Footer text" && el.Type != documentir.ElFooter {
			t.Fatalf("footer typed %q", el.Type)
		}
	}
}

func TestDoclingRunsInsideListItemAndSpacing(t *testing.T) {
	doc := mapCapture(t, "docling_rich_runs_probe.json", "none")
	var items []documentir.Element
	for _, el := range doc.Elements {
		if el.Type == documentir.ElListItem {
			items = append(items, el)
		}
	}
	if len(items) != 2 {
		t.Fatalf("list items = %+v", items)
	}
	if items[0].Text != "Item with bold part, then comma." || items[0].RichText != "Item with **bold part**, then comma." {
		t.Fatalf("list item runs = %q / %q", items[0].Text, items[0].RichText)
	}
	if items[0].Metadata[documentir.MetaListMarker] != "1." || items[1].Metadata[documentir.MetaListMarker] != "2." {
		t.Fatalf("markers lost: %+v / %+v", items[0].Metadata, items[1].Metadata)
	}
	md := documentir.RenderMarkdown(doc)
	if !strings.Contains(md, "1. Item with **bold part**, then comma.\n2. Plain item\n") {
		t.Fatalf("numbered list not rendered:\n%s", md)
	}
	// The rich-cell runs are rendered by the table, not repeated as paragraphs.
	if strings.Count(md, "cell bold") != 1 {
		t.Fatalf("table cell text repeated:\n%s", md)
	}
}

func TestDoclingRunFormatting(t *testing.T) {
	f := func(b, i, s bool) *doclingFormatting {
		return &doclingFormatting{Bold: b, Italic: i, Strikethrough: s}
	}
	cases := []struct {
		t    doclingText
		rich string
	}{
		{doclingText{Text: "plain"}, "plain"},
		{doclingText{Text: "b", Formatting: f(true, false, false)}, "**b**"},
		{doclingText{Text: "bi", Formatting: f(true, true, false)}, "***bi***"},
		{doclingText{Text: "gone", Formatting: f(false, false, true)}, "~~gone~~"},
		{doclingText{Text: "5 * 3", Formatting: f(true, false, false)}, "**5 * 3**"},
		{doclingText{Text: "a*b", Formatting: f(false, true, false)}, `*a\*b*`},
		{doclingText{Text: "docs", Hyperlink: "https://x.y/a b"}, "[docs](<https://x.y/a b>)"},
		{doclingText{Text: "u", Formatting: &doclingFormatting{Underline: true}}, "u"},
	}
	for _, c := range cases {
		plain, rich := doclingRun(&c.t)
		if plain != c.t.Text || rich != c.rich {
			t.Errorf("doclingRun(%+v) = %q, %q; want %q", c.t, plain, rich, c.rich)
		}
	}
}
