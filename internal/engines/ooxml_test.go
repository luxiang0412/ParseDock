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
	doc.Tables = []documentir.Table{{ID: "tbl_1", Rows: 2, Cols: 4,
		Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: "Sheet1"},
		Cells: []documentir.Cell{
			{Row: 0, Col: 3, Text: "Shared label", DataType: "string"}, // ordinary cell
			{Row: 1, Col: 1, Text: "42"},
			{Row: 1, Col: 2, Text: "ab"},
			{Row: 1, Col: 3, Text: "1"},
		}}}
	applied, _ := EnrichXLSXFormulas(path, doc)
	if applied != 3 {
		t.Fatalf("applied = %d, want 3 formulas", applied)
	}
	cells := doc.Tables[0].Cells
	if cells[1].Formula != "=SUM(A1:A2)" || cells[1].CachedValue != "42" || cells[1].DataType != "number" {
		t.Fatalf("numeric formula: %+v", cells[1])
	}
	if cells[2].Formula != `=CONCAT("a","b")` || cells[2].CachedValue != "ab" || cells[2].DataType != "string" {
		t.Fatalf("string formula must keep string type: %+v", cells[2])
	}
	if cells[3].Formula != "=1=1" || cells[3].DataType != "bool" {
		t.Fatalf("boolean formula must keep bool type: %+v", cells[3])
	}
	// An ordinary shared-string cell keeps the engine's text and gains nothing.
	if cells[0].Formula != "" || cells[0].CachedValue != "" || cells[0].DataType != "string" {
		t.Fatalf("ordinary cell modified: %+v", cells[0])
	}
}

// Shared-string formula results are indexes into sharedStrings.xml.
func TestEnrichXLSXSharedStringResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sst.xlsx")
	f, err := os.Create(path)
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
	add("xl/workbook.xml", `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`)
	add("xl/sharedStrings.xml", `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><si><t>first</t></si><si><r><t>ri</t></r><r><t>ch</t></r></si></sst>`)
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" t="s"><f>IF(1,"first","x")</f><v>0</v></c><c r="B1" t="s"><f>IF(1,"rich","x")</f><v>1</v></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 1, Cols: 2,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "S"},
		Cells: []documentir.Cell{{Row: 0, Col: 0}, {Row: 0, Col: 1}}}}
	EnrichXLSXFormulas(path, doc)
	if got := doc.Tables[0].Cells[0].CachedValue; got != "first" {
		t.Fatalf("shared-string index not resolved: %q", got)
	}
	if got := doc.Tables[0].Cells[1].CachedValue; got != "rich" {
		t.Fatalf("rich shared string not concatenated: %q", got)
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
	add("xl/sharedStrings.xml", `<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>Shared label</t></si>
</sst>`)
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1"><v>1</v></c>
      <c r="D1" t="s"><v>0</v></c>
    </row>
    <row r="2">
      <c r="B2"><f>SUM(A1:A2)</f><v>42</v></c>
      <c r="C2" t="str"><f>CONCAT("a","b")</f><v>ab</v></c>
      <c r="D2" t="b"><f>1=1</f><v>1</v></c>
    </row>
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

// A formula whose cached value is a date serial: the engine's date type wins.
func TestEnrichKeepsEngineDateType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "date.xlsx")
	f, err := os.Create(path)
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
	add("xl/workbook.xml", `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`)
	// Numeric serial 45123 with a date style, no t="d".
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" s="1"><f>TODAY()</f><v>45123</v></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 1, Cols: 1,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "S"},
		Cells: []documentir.Cell{{Row: 0, Col: 0, Text: "2023-07-15", DataType: "date"}}}}
	EnrichXLSXFormulas(path, doc)
	cell := doc.Tables[0].Cells[0]
	if cell.DataType != "date" {
		t.Fatalf("engine date type overwritten: %q", cell.DataType)
	}
	if cell.Formula != "=TODAY()" {
		t.Fatalf("formula = %q", cell.Formula)
	}
}

// An uncached shared-formula slave must still trigger the warning.
func TestEnrichWarnsOnUncachedSharedFormula(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.xlsx")
	f, err := os.Create(path)
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
	add("xl/workbook.xml", `<?xml version="1.0"?><workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`)
	add("xl/_rels/workbook.xml.rels", `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`)
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><f t="shared" ref="A1:A2" si="0">A2+1</f><v>2</v></c><c r="A2"><f t="shared" si="0"/></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 2, Cols: 1,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "S"},
		Cells: []documentir.Cell{{Row: 0, Col: 0}, {Row: 1, Col: 0}}}}
	applied, warnings := EnrichXLSXFormulas(path, doc)
	if applied != 1 {
		t.Fatalf("applied = %d, want the master only", applied)
	}
	found := false
	for _, w := range warnings {
		if w.Code == documentir.WarnFormulaMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("no shared-formula warning: %v", warnings)
	}
}
