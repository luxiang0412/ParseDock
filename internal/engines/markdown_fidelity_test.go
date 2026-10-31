package engines

import (
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

func TestStripInline(t *testing.T) {
	cases := map[string]string{
		"plain words":                        "plain words",
		"a **bold** b":                       "a bold b",
		"a __bold__ b":                       "a bold b",
		"*it* and _it_":                      "it and it",
		"***both***":                         "both",
		"~~gone~~ and ~one~":                 "gone and one",
		"~~~ not strike ~~~":                 "~~~ not strike ~~~",
		"use `a*b*c` here":                   "use a*b*c here",
		"`` a ` b ``":                        "a ` b",
		"see [the docs](https://x.y/z)":      "see the docs",
		"[**bold** link](u \"title (x)\")":   "bold link",
		"![alt text](img.png) after":         "alt text after",
		"<https://example.com> and <a@b.co>": "https://example.com and a@b.co",
		"line<br>break<br/>again":            "line\nbreak\nagain",
		"x<sup>2</sup>":                      "x2",
		`escaped \*star\* and \_u\_`:         "escaped *star* and _u_",
		"5 * 3 and 2*4":                      "5 * 3 and 2*4",
		"snake_case_name":                    "snake_case_name",
		"unmatched *star":                    "unmatched *star",
		"[not a link] (x)":                   "[not a link] (x)",
		"a <div> b":                          "a <div> b",
		"AT&amp;T":                           "AT&T",
		"**unclosed bold":                    "**unclosed bold",
		"*a **b** c*":                        "a b c",
		"price: 1,000 * 2":                   "price: 1,000 * 2",
		"中文**加粗**文字":                         "中文加粗文字",
	}
	for in, want := range cases {
		if got := stripInline(in); got != want {
			t.Errorf("stripInline(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLiftInlineOmitsIdenticalRichText(t *testing.T) {
	if p, r := liftInline("nothing special here"); p != "nothing special here" || r != "" {
		t.Fatalf("plain input got rich_text %q", r)
	}
	if p, r := liftInline("a **b**"); p != "a b" || r != "a **b**" {
		t.Fatalf("got plain=%q rich=%q", p, r)
	}
}

func TestLiftMarkdownFidelity(t *testing.T) {
	md := strings.Join([]string{
		"# Report **2026** ##",
		"",
		"Intro with *emphasis* and a [link](https://example.com).",
		"",
		"1. first",
		"2. second",
		"   - nested under a numbered item",
		"      1) deeper",
		"3. third",
		"",
		"> quoted line one",
		"> quoted **line** two",
		"",
		"```go",
		"fmt.Println(\"hi\")",
		"```",
		"",
		"| Name | Note |",
		"| --- | --- |",
		"| **North** | a \\| b |",
		"|  |  |",
		"| South | line<br>two |",
	}, "\n")
	doc := documentir.New()
	LiftMarkdown(doc, md)
	byType := func(typ string) []documentir.Element {
		var out []documentir.Element
		for _, el := range doc.Elements {
			if el.Type == typ {
				out = append(out, el)
			}
		}
		return out
	}
	title := byType(documentir.ElTitle)
	if len(title) != 1 || title[0].Text != "Report 2026" || title[0].RichText != "Report **2026**" {
		t.Fatalf("heading = %+v", title)
	}
	paras := byType(documentir.ElParagraph)
	if len(paras) != 2 {
		t.Fatalf("paragraphs = %+v", paras)
	}
	if paras[0].Text != "Intro with emphasis and a link." || !strings.Contains(paras[0].RichText, "*emphasis*") {
		t.Fatalf("intro = %+v", paras[0])
	}
	q := paras[1]
	if q.Metadata[documentir.MetaMarkdown] != documentir.MarkdownBlockquote ||
		q.Text != "quoted line one\nquoted line two" || q.RichText != "quoted line one\nquoted **line** two" {
		t.Fatalf("multi-line quote = %+v", q)
	}
	items := byType(documentir.ElListItem)
	type want struct {
		text   string
		level  int
		marker string
	}
	wantItems := []want{{"first", 1, "1."}, {"second", 1, "2."}, {"nested under a numbered item", 2, ""}, {"deeper", 3, "1)"}, {"third", 1, "3."}}
	if len(items) != len(wantItems) {
		t.Fatalf("list items = %+v", items)
	}
	for i, w := range wantItems {
		it := items[i]
		if it.Text != w.text || it.Level != w.level || it.Metadata[documentir.MetaListMarker] != w.marker {
			t.Errorf("item %d = %q level %d marker %q, want %+v", i, it.Text, it.Level, it.Metadata[documentir.MetaListMarker], w)
		}
		if (w.marker != "") != (it.Metadata[documentir.MetaListOrdered] == "true") {
			t.Errorf("item %d ordered flag wrong: %+v", i, it.Metadata)
		}
	}
	code := byType(documentir.ElCode)
	if len(code) != 1 || code[0].Metadata[documentir.MetaCodeLanguage] != "go" || code[0].Text != `fmt.Println("hi")` {
		t.Fatalf("code = %+v", code)
	}
	if len(doc.Tables) != 1 {
		t.Fatalf("tables = %d", len(doc.Tables))
	}
	tbl := doc.Tables[0]
	if tbl.Rows != 4 || tbl.Cols != 2 {
		t.Fatalf("table %dx%d, want 4x2 (the empty row is data, not a delimiter)", tbl.Rows, tbl.Cols)
	}
	cell := func(r, c int) documentir.Cell {
		for _, x := range tbl.Cells {
			if x.Row == r && x.Col == c {
				return x
			}
		}
		return documentir.Cell{}
	}
	if c := cell(1, 0); c.Text != "North" || c.RichText != "**North**" {
		t.Errorf("bold cell = %+v", c)
	}
	if c := cell(1, 1); c.Text != "a | b" {
		t.Errorf("escaped pipe cell = %+v", c)
	}
	if c := cell(3, 1); c.Text != "line\ntwo" {
		t.Errorf("<br> cell = %+v", c)
	}
}

// Lifting canonical Markdown and rendering it again must reproduce it: the IR keeps
// everything the renderer needs.
func TestLiftRenderRoundTrip(t *testing.T) {
	md := strings.Join([]string{
		"# Dock **notes**",
		"",
		"Plain *intro* with `code` and a [link](https://example.com/x).",
		"",
		"1. first",
		"2. second",
		"   - nested",
		"3. third",
		"",
		"> quoted",
		"> twice",
		"",
		"```python",
		"print(1)",
		"```",
		"",
		"| Region | Pallets |",
		"| --- | --- |",
		"| **North** | 48120 |",
		"| South | a\\|b |",
		"",
		"- [ ] firmware rollback",
		"- [x] carrier review",
	}, "\n") + "\n"
	doc := documentir.New()
	LiftMarkdown(doc, md)
	doc.Finalize()
	if got := documentir.RenderMarkdown(doc); got != md {
		t.Fatalf("round trip changed the Markdown\n--- got ---\n%s\n--- want ---\n%s", got, md)
	}
}
