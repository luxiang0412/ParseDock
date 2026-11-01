package engines

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

func recoverDoc(els ...documentir.Element) *documentir.Document {
	d := documentir.New()
	for i := range els {
		if els[i].Loc == nil {
			els[i].Loc = &documentir.SourceLocation{Kind: "pdf", Page: 1}
		}
		if els[i].Type == "" {
			els[i].Type = documentir.ElParagraph
		}
	}
	d.Elements = els
	return d
}

func texts(d *documentir.Document) []string {
	var out []string
	for _, el := range d.Elements {
		out = append(out, el.Text)
	}
	return out
}

func TestRecoverTextLayerPutsLostLinesBackInPlace(t *testing.T) {
	d := recoverDoc(
		documentir.Element{Type: documentir.ElSection, Level: 2, Text: "Quarterly results"},
		documentir.Element{Text: "Revenue grew in every region during the quarter."},
		documentir.Element{Text: "Costs were flat."},
	)
	plain := []xbergPage{{PageNumber: 1, Content: strings.Join([]string{
		"Quarterly results",
		"Revenue grew in every region during the quarter.",
		"Warehouse leases renewed through March",
		"with two optional extensions",
		"Costs were flat.",
	}, "\n")}}
	if n := recoverTextLayer(d, plain, nil); n != 1 {
		t.Fatalf("recovered %d blocks, want 1", n)
	}
	got := texts(d)
	want := []string{"Quarterly results", "Revenue grew in every region during the quarter.",
		"Warehouse leases renewed through March with two optional extensions", "Costs were flat."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("order/text:\n got %q\nwant %q", got, want)
	}
	el := d.Elements[2]
	if el.Metadata[documentir.MetaRecovered] != documentir.RecoveredTextLayer || el.Loc.Page != 1 || el.ID != "el_3" {
		t.Fatalf("recovered element = %+v", el)
	}
}

func TestRecoverTextLayerLeavesWordLevelDifferencesAlone(t *testing.T) {
	// One word of ten differs (the Markdown writer ran two words together): re-inserting
	// the line would repeat the other nine.
	d := recoverDoc(documentir.Element{Text: "The audit covered all twelve branches and the headoffice in full."})
	plain := []xbergPage{{PageNumber: 1, Content: "The audit covered all twelve branches and the head office in full."}}
	if n := recoverTextLayer(d, plain, nil); n != 0 {
		t.Fatalf("recovered %d, want 0: %q", n, texts(d))
	}
}

func TestRecoverTextLayerSkipsRunningHeadersShortLinesAndOCRPages(t *testing.T) {
	d := recoverDoc(
		documentir.Element{Text: "Body text of the first page is here."},
		documentir.Element{Text: "Body text of the second page is here.", Loc: &documentir.SourceLocation{Kind: "pdf", Page: 2}},
		documentir.Element{Text: "Scanned page text.", Loc: &documentir.SourceLocation{Kind: "pdf", Page: 3}},
	)
	plain := []xbergPage{
		{PageNumber: 1, Content: "Confidential internal report draft 1\nBody text of the first page is here.\nPage 1"},
		{PageNumber: 2, Content: "Confidential internal report draft 2\nBody text of the second page is here.\nPage 2"},
		{PageNumber: 3, Content: "Entirely different words appear only here"},
	}
	if n := recoverTextLayer(d, plain, map[int]bool{3: true}); n != 0 {
		t.Fatalf("recovered %d, want 0: %q", n, texts(d))
	}
}

func TestRecoverTextLayerHandlesPageTopTablesAndEmptyPages(t *testing.T) {
	d := recoverDoc(
		documentir.Element{Type: documentir.ElTable, TableID: "tbl_1", Text: "1 rows x 2 cols"},
		documentir.Element{Text: "Closing remarks for page one.", Loc: &documentir.SourceLocation{Kind: "pdf", Page: 1}},
		documentir.Element{Text: "Page three starts here.", Loc: &documentir.SourceLocation{Kind: "pdf", Page: 3}},
	)
	d.Tables = []documentir.Table{{ID: "tbl_1", Rows: 1, Cols: 2, Cells: []documentir.Cell{
		{Row: 0, Col: 0, Text: "Region"}, {Row: 0, Col: 1, Text: "North"}}}}
	plain := []xbergPage{
		{PageNumber: 1, Content: "Opening statement nobody kept\nRegion North\nNote below the table was lost too\nClosing remarks for page one."},
		{PageNumber: 2, Content: "A page whose words all vanished"},
		{PageNumber: 3, Content: "Page three starts here."},
	}
	if n := recoverTextLayer(d, plain, nil); n != 3 {
		t.Fatalf("recovered %d, want 3: %q", n, texts(d))
	}
	want := []string{"Opening statement nobody kept", "1 rows x 2 cols", "Note below the table was lost too",
		"Closing remarks for page one.", "A page whose words all vanished", "Page three starts here."}
	if got := texts(d); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if d.Elements[4].Loc.Page != 2 {
		t.Fatalf("empty-page block page = %d", d.Elements[4].Loc.Page)
	}
}

func TestRecoverTextLayerJoinsCJKLinesWithoutSpaces(t *testing.T) {
	d := recoverDoc(documentir.Element{Text: "前言部分已经保留。"})
	plain := []xbergPage{{PageNumber: 1, Content: "前言部分已经保留。\n仓库租约续签至三月\n并附带两次续租选择权"}}
	if n := recoverTextLayer(d, plain, nil); n != 1 {
		t.Fatalf("recovered %d, want 1", n)
	}
	if got := d.Elements[1].Text; got != "仓库租约续签至三月并附带两次续租选择权" {
		t.Fatalf("joined = %q", got)
	}
}

func TestForTokens(t *testing.T) {
	var got []string
	forTokens("Q3 revenue: 1,234.5 – 仓库B区 ﬁnal", func(s string) { got = append(got, s) })
	want := "q|3|revenue|1|234|5|仓|库|b|区|ﬁnal"
	if strings.Join(got, "|") != want {
		t.Fatalf("tokens = %q, want %q", strings.Join(got, "|"), want)
	}
}

// The full Parse path: a stand-in binary answers the main (Markdown) run with a capture and
// the text-layer run with a plain variant that has one extra line.
func TestXbergParseRecoversTextLayerLines(t *testing.T) {
	capture, err := os.ReadFile(filepath.Join("testdata", "xberg_pdf_text_en_3p.json"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(capture, &raw); err != nil {
		t.Fatal(err)
	}
	pages := raw["result"].(map[string]any)["pages"].([]any)
	first := pages[0].(map[string]any)
	first["content"] = first["content"].(string) + "\nAppendix reference kept only by the text layer"
	plainJSON, _ := json.Marshal(raw)

	dir := t.TempDir()
	md, pl := filepath.Join(dir, "md.json"), filepath.Join(dir, "plain.json")
	if err := os.WriteFile(md, capture, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pl, plainJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "xberg")
	script := "#!/bin/sh\ncase \"$*\" in *'--content-format plain'*) exec cat '" + pl + "';; esac\nexec cat '" + md + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	in := writeTemp(t, "input.pdf", []byte("synthetic"))
	doc, err := (&Xberg{Bin: bin, VersionS: "1.3.10"}).Parse(context.Background(), Request{
		Path: in, Format: "pdf", Mode: "fast", WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var found *documentir.Element
	for i := range doc.Elements {
		if doc.Elements[i].Metadata[documentir.MetaRecovered] != "" {
			found = &doc.Elements[i]
		}
	}
	if found == nil || found.Text != "Appendix reference kept only by the text layer" || found.Loc.Page != 1 {
		t.Fatalf("recovered element = %+v", found)
	}
	warned := false
	for _, w := range doc.Warnings {
		warned = warned || w.Code == documentir.WarnTextRecovered
	}
	if !warned {
		t.Fatalf("no %s warning: %+v", documentir.WarnTextRecovered, doc.Warnings)
	}
	// Without the extra line the same capture recovers nothing (the replay tests run both
	// calls against one capture).
	if d := replayXberg(t, "xberg_pdf_text_en_3p.json", "pdf"); hasRecovered(d) {
		t.Fatal("identical text layer must not recover anything")
	}
}

func hasRecovered(d *documentir.Document) bool {
	for _, el := range d.Elements {
		if el.Metadata[documentir.MetaRecovered] != "" {
			return true
		}
	}
	return false
}
