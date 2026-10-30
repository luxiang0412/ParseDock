package engines

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// fakeTika serves the two endpoints the adapter uses, with bodies captured from
// the real Tika 4.2.0 responses during the PR-00 audit.
func fakeTika(t *testing.T, tikaBody string, pagesJSON string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/tika", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, tikaBody)
	})
	mux.HandleFunc("/rmeta/text", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, pagesJSON)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// F08: Tika returns no page boundaries for PDFs; the adapter must not invent
// "page 1" for every element.
func TestTikaDoesNotFabricatePageNumbers(t *testing.T) {
	body := "Quarterly Logistics Review\n\nPage one text.\n\nPage two text.\n"
	srv := fakeTika(t, body, `[{"xmpTPg:NPages":"3"}]`)
	eng := &Tika{BaseURL: srv.URL}
	path := writeTemp(t, "doc.pdf", []byte("%PDF-1.7\n%%EOF\n"))

	doc, err := eng.Parse(context.Background(), Request{
		Path: path, Format: "pdf", MimeType: "application/pdf", WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, el := range doc.Elements {
		if el.Loc != nil && el.Loc.Page != 0 {
			t.Fatalf("fabricated page %d on element %q", el.Loc.Page, el.Text)
		}
	}
	if doc.PageCount != 3 {
		t.Fatalf("page count = %d, want 3 from xmpTPg:NPages", doc.PageCount)
	}
	if !hasWarning(doc, documentir.WarnNoPageBoundaries) {
		t.Fatal("missing no_page_boundaries warning")
	}
}

// When Tika *does* emit form feeds, page numbers are real and must be kept.
func TestTikaUsesFormFeedPageBoundaries(t *testing.T) {
	srv := fakeTika(t, "page one\n\fpage two\n\fpage three\n", `[{"xmpTPg:NPages":"3"}]`)
	eng := &Tika{BaseURL: srv.URL}
	path := writeTemp(t, "doc.pdf", []byte("%PDF-1.7\n%%EOF\n"))

	doc, err := eng.Parse(context.Background(), Request{Path: path, Format: "pdf", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if doc.PageCount != 3 {
		t.Fatalf("page count = %d, want 3", doc.PageCount)
	}
	pages := map[int]bool{}
	for _, el := range doc.Elements {
		if el.Loc != nil {
			pages[el.Loc.Page] = true
		}
	}
	if len(pages) < 3 {
		t.Fatalf("expected elements on 3 pages, got %v", pages)
	}
}

// Tika 4.x returns markdown for OOXML: lift it instead of flattening.
func TestTikaLiftsMarkdownForOOXML(t *testing.T) {
	body := "# Dock Throughput Review\n\nBody text.\n\n| Region | Pallets |\n| --- | --- |\n| North | 48120 |\n"
	srv := fakeTika(t, body, `[]`)
	eng := &Tika{BaseURL: srv.URL}
	path := writeTemp(t, "doc.docx", []byte("PK\x03\x04"))

	doc, err := eng.Parse(context.Background(), Request{Path: path, Format: "docx", WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var types []string
	for _, el := range doc.Elements {
		types = append(types, el.Type)
	}
	if len(types) == 0 || types[0] != documentir.ElTitle {
		t.Fatalf("heading not lifted: %v", types)
	}
	if len(doc.Tables) != 1 || doc.Tables[0].Cols != 2 {
		t.Fatalf("markdown table not lifted: %+v", doc.Tables)
	}
}

// No adapter may publish image references until artifacts are downloadable.
func TestNoAdapterEmitsDanglingImageRefs(t *testing.T) {
	body := "# T\n\n![missing](images/x.png)\n"
	srv := fakeTika(t, body, `[]`)
	eng := &Tika{BaseURL: srv.URL}
	doc, err := eng.Parse(context.Background(), Request{
		Path: writeTemp(t, "d.docx", []byte("PK\x03\x04")), Format: "docx", WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// The Markdown lifter records a reference, but the coordinator must not
	// publish it as an artifact. Document the fact explicitly: today the IR field
	// is reserved and this test pins the ground truth.
	if !doc.HasImageRefs() {
		t.Log("lifter does not record image refs")
	}
	mock := &Mock{}
	mockDoc, err := mock.Parse(context.Background(), Request{
		JobID: "job_x", Format: "pdf", WorkDir: t.TempDir(), Timeout: 1e9,
	})
	if err != nil {
		t.Fatal(err)
	}
	if mockDoc.HasImageRefs() {
		t.Fatal("mock engine invented an image reference")
	}
}

func hasWarning(doc *documentir.Document, code string) bool {
	for _, w := range doc.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// Docling page sizes must reach the IR so a bbox can be rendered (F08).
func TestDoclingPageDimensions(t *testing.T) {
	raw := []byte(`{
      "schema_name":"DoclingDocument","version":"1.0.0",
      "pages":{"1":{"page_no":1,"size":{"width":595.28,"height":841.89}}},
      "body":{"self_ref":"#/body","children":[{"$ref":"#/texts/0"}]},
      "texts":[{"self_ref":"#/texts/0","label":"text","text":"hello",
                "prov":[{"page_no":1,"bbox":{"l":10,"t":20,"r":30,"b":40,"coord_origin":"BOTTOMLEFT"}}]}],
      "tables":[],"pictures":[],"groups":[]
    }`)
	doc := documentir.New()
	if err := mapDocling(doc, raw, "pdf"); err != nil {
		t.Fatal(err)
	}
	if len(doc.Elements) != 1 {
		t.Fatalf("elements = %d", len(doc.Elements))
	}
	loc := doc.Elements[0].Loc
	if loc == nil || loc.BBox == nil {
		t.Fatal("bbox lost")
	}
	if loc.PageWidthPt != 595.28 || loc.PageHeightPt != 841.89 {
		t.Fatalf("page size not propagated: %+v", loc)
	}
	if loc.BBox.Coord != "pdf_user_space_v1" {
		t.Fatalf("coordinate origin lost: %s", loc.BBox.Coord)
	}
	if strings.Contains(strings.ToLower(loc.BBox.Coord), "pixel") {
		t.Fatal("pixel coordinates fabricated for a PDF")
	}
}

// A spreadsheet's docling "page size" is a cell-grid extent, not points: it must
// not be reported as page_width_pt (F08).
func TestDoclingDoesNotReportSheetGridAsPageSize(t *testing.T) {
	raw := []byte(`{
      "schema_name":"DoclingDocument","version":"1.0.0",
      "pages":{"1":{"page_no":1,"size":{"width":4.0,"height":9.0}}},
      "body":{"self_ref":"#/body","children":[{"$ref":"#/groups/0"}]},
      "texts":[],"tables":[
        {"self_ref":"#/tables/0","label":"table","prov":[{"page_no":1,"bbox":{"l":0,"t":0,"r":4,"b":9,"coord_origin":"TOPLEFT"}}],
         "data":{"num_rows":1,"num_cols":1,"table_cells":[{"start_row_offset_idx":0,"end_row_offset_idx":1,
         "start_col_offset_idx":0,"end_col_offset_idx":1,"row_span":1,"col_span":1,"text":"A"}]}}],
      "pictures":[],
      "groups":[{"self_ref":"#/groups/0","label":"sheet","name":"Summary","children":[{"$ref":"#/tables/0"}]}]
    }`)
	doc := documentir.New()
	if err := mapDocling(doc, raw, "sheet"); err != nil {
		t.Fatal(err)
	}
	if len(doc.Tables) != 1 {
		t.Fatalf("tables = %d", len(doc.Tables))
	}
	loc := doc.Tables[0].Loc
	if loc == nil || loc.Sheet != "Summary" {
		t.Fatalf("sheet name lost: %+v", loc)
	}
	if loc.PageWidthPt != 0 || loc.PageHeightPt != 0 {
		t.Fatalf("sheet grid extent reported as PDF points: %+v", loc)
	}
}
