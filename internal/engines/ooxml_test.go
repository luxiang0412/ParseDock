package engines

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

func TestParseA1(t *testing.T) {
	cases := []struct {
		ref      string
		col, row int
		ok       bool
	}{
		{"A1", 0, 0, true},
		{"B7", 1, 6, true},
		{"AA10", 26, 9, true},
		{"ZZ1", 701, 0, true},
		{"A", 0, 0, false},
		{"1", 0, 0, false},
		{"", 0, 0, false},
		{"A0", 0, 0, false},
	}
	for _, c := range cases {
		col, row, ok := parseA1(c.ref)
		if ok != c.ok || (ok && (col != c.col || row != c.row)) {
			t.Errorf("parseA1(%q) = %d,%d,%v want %d,%d,%v", c.ref, col, row, ok, c.col, c.row, c.ok)
		}
	}
}

// PR-05: formulas come from the worksheet XML because no engine exposes them.
func TestEnrichXLSXFormulas(t *testing.T) {
	path := filepath.Join("..", "..", "benchmark", "fixtures", "generated", "xlsx_workbook.xlsx")
	doc := documentir.New()
	doc.Tables = []documentir.Table{
		{ID: "tbl_1", Rows: 9, Cols: 4, Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: "Summary"},
			Cells: []documentir.Cell{
				{Row: 6, Col: 1, Text: "128400", DataType: "number"}, // B7 = SUM(B2:B5)
				{Row: 6, Col: 2, Text: "95.7", DataType: "number"},   // C7 = AVERAGE(C2:C5)
				{Row: 8, Col: 0, Text: "Merged note"},                // A9 merged note, no formula
			}},
		{ID: "tbl_2", Rows: 5, Cols: 5, Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: "Detail"},
			Cells: []documentir.Cell{
				{Row: 4, Col: 1, Text: "5573", DataType: "number"}, // B5 = SUM(B2:B4)
				{Row: 1, Col: 4, Text: "128400.125", DataType: "number"},
			}},
	}
	applied, warnings := EnrichXLSXFormulas(path, doc)
	if applied < 3 {
		t.Fatalf("applied %d formulas, want >= 3; warnings=%v", applied, warnings)
	}
	summaryB7 := doc.Tables[0].Cells[0]
	if summaryB7.Formula != "=SUM(B2:B5)" {
		t.Fatalf("B7 formula = %q", summaryB7.Formula)
	}
	// openpyxl (the fixture generator) writes formulas without cached values, so
	// the enrichment must not invent one.
	if summaryB7.CachedValue != "" {
		t.Fatalf("cached value invented: %q", summaryB7.CachedValue)
	}
	summaryC7 := doc.Tables[0].Cells[1]
	if summaryC7.Formula != "=AVERAGE(C2:C5)" {
		t.Fatalf("C7 formula = %q", summaryC7.Formula)
	}
	detailB5 := doc.Tables[1].Cells[0]
	if detailB5.Formula != "=SUM(B2:B4)" {
		t.Fatalf("Detail B5 formula = %q", detailB5.Formula)
	}
	// A merged note without a formula must stay untouched.
	if doc.Tables[0].Cells[2].Formula != "" {
		t.Fatal("formula invented for a cell without <f>")
	}
	// A date cell keeps its value and gains no fake formula.
	if doc.Tables[1].Cells[1].Formula != "" {
		t.Fatal("formula invented for a value-only cell")
	}
}

// Enrichment must be a no-op for non-xlsx input and must not fabricate cells.
func TestEnrichXLSXFormulasNoOp(t *testing.T) {
	doc := documentir.New()
	applied, warnings := EnrichXLSXFormulas(filepath.Join("..", "..", "benchmark", "fixtures", "generated", "pdf_text_en_3p.pdf"), doc)
	if applied != 0 || len(warnings) == 0 {
		t.Fatalf("applied=%d warnings=%v", applied, warnings)
	}
	if len(doc.Tables) != 0 || len(doc.Elements) != 0 {
		t.Fatal("enrichment modified a document it could not read")
	}
}

// Enrichment is wired into the worker for xlsx only (see worker.execute).
func TestEnrichmentDoesNotTouchOtherFormats(t *testing.T) {
	if ShouldEnrichFormulas("pdf") || ShouldEnrichFormulas("docx") {
		t.Fatal("formula enrichment must be limited to xlsx/xlsm")
	}
	if !ShouldEnrichFormulas("xlsx") || !ShouldEnrichFormulas("xlsm") {
		t.Fatal("formula enrichment missing for xlsx/xlsm")
	}
}

// A cached value that the file does contain must reach the IR with its type.
func TestEnrichXLSXFormulasCachedValue(t *testing.T) {
	path := writeMinimalXLSX(t)
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "tbl_1", Rows: 2, Cols: 2,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "Sheet1"},
		Cells: []documentir.Cell{{Row: 1, Col: 1, Text: "42"}}}}
	applied, _ := EnrichXLSXFormulas(path, doc)
	if applied != 1 {
		t.Fatalf("applied = %d", applied)
	}
	cell := doc.Tables[0].Cells[0]
	if cell.Formula != "=SUM(A1:A2)" {
		t.Fatalf("formula = %q", cell.Formula)
	}
	if cell.CachedValue != "42" || cell.DataType != "number" {
		t.Fatalf("cached = %q type = %q", cell.CachedValue, cell.DataType)
	}
}

// Minimal xlsx with one formula and one cached value.
func writeMinimalXLSX(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "formula.xlsx")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("xl/workbook.xml", `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
          xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets><sheet name="Sheet1" sheetId="1" r:id="rId1"/></sheets>
</workbook>`)
	add("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="worksheet" Target="worksheets/sheet1.xml"/>
</Relationships>`)
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1"><v>1</v></c></row>
    <row r="2"><c r="B2"><f>SUM(A1:A2)</f><v>42</v></c></row>
  </sheetData>
</worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

var _ = context.Background
