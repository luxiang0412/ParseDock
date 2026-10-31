package documentir

import (
	"strings"
	"testing"
)

func TestEscapeRichHTML(t *testing.T) {
	cases := map[string]string{
		"Hello **bold** <img src=x onerror=alert(1)>": `Hello **bold** \<img src=x onerror=alert(1)>`,
		"<script>x</script>":                          `\<script>x\</script>`,
		"<!-- c --> <?php ?>":                         `\<!-- c --> \<?php ?>`,
		"see <https://example.com/a?b=1> or <a@b.co>": "see <https://example.com/a?b=1> or <a@b.co>",
		"line<br>two<br/>three":                       "line<br>two<br/>three",
		"code `<b>` stays and ``a <i> b`` too":        "code `<b>` stays and ``a <i> b`` too",
		"already \\<b> escaped":                       "already \\<b> escaped",
		"1 < 2 and a<3":                               "1 < 2 and a<3",
		"no html at all":                              "no html at all",
	}
	for in, want := range cases {
		if got := escapeRichHTML(in); got != want {
			t.Errorf("escapeRichHTML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRichTextRawHTMLIsNotEmitted(t *testing.T) {
	d := New()
	d.Elements = []Element{
		{ID: "el_1", Type: ElParagraph, Text: "x <script>alert(1)</script>", RichText: "**x** <script>alert(1)</script>"},
		{ID: "el_2", Type: ElListItem, Level: 1, Text: "y <img src=x onerror=1>", RichText: "*y* <img src=x onerror=1>"},
		{ID: "el_3", Type: ElTable, TableID: "t1"},
	}
	d.Tables = []Table{{ID: "t1", Rows: 2, Cols: 1, HeaderRows: 1, Cells: []Cell{
		{Row: 0, Col: 0, RowSpan: 1, ColSpan: 1, Text: "h"},
		{Row: 1, Col: 0, RowSpan: 1, ColSpan: 1, Text: "c <b onclick=1>", RichText: "**c** <b onclick=1>"},
	}}}
	md := RenderMarkdown(d)
	for _, bad := range []string{"<script", "<img", "<b onclick"} {
		if strings.Contains(strings.ReplaceAll(md, `\<`, "&lt;"), bad) {
			t.Fatalf("raw HTML %q reached the Markdown view:\n%s", bad, md)
		}
	}
	if !strings.Contains(md, "**x**") || !strings.Contains(md, "*y*") || !strings.Contains(md, "**c**") {
		t.Fatalf("inline markup lost:\n%s", md)
	}
}
