package engines

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
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
	applied, warnings, _ := EnrichXLSXFormulas(context.Background(), path, doc)
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
	applied, warnings, err := EnrichXLSXFormulas(context.Background(),
		filepath.Join("..", "..", "benchmark", "fixtures", "generated", "pdf_text_en_3p.pdf"), doc)
	if err != nil || applied != 0 || len(warnings) == 0 {
		t.Fatalf("applied=%d warnings=%v err=%v", applied, warnings, err)
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
			// The adapters fill these from text (guessType): the explicit OOXML
			// type must win for string/boolean results.
			{Row: 1, Col: 2, Text: "ab", DataType: "string"},
			{Row: 1, Col: 3, Text: "1", DataType: "number"},
		}}}
	applied, _, gerr := EnrichXLSXFormulas(context.Background(), path, doc)
	if gerr != nil {
		t.Fatal(gerr)
	}
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
	_, _, _ = EnrichXLSXFormulas(context.Background(), path, doc)
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
	EnrichXLSXFormulas(context.Background(), path, doc)
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
	applied, warnings, gerr := EnrichXLSXFormulas(context.Background(), path, doc)
	if gerr != nil || applied != 1 {
		t.Fatalf("applied = %d err = %v, want the master only", applied, gerr)
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

// A table that names its sheet must not inherit another sheet's formulas.
func TestEnrichDoesNotCrossSheets(t *testing.T) {
	path := writeMinimalXLSX(t) // one sheet "Sheet1" with formulas
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 2, Cols: 4,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "OtherSheet"},
		Cells: []documentir.Cell{{Row: 1, Col: 1, Text: "42"}}}}
	applied, _, _ := EnrichXLSXFormulas(context.Background(), path, doc)
	if applied != 0 {
		t.Fatalf("formulas from another sheet were applied: %d", applied)
	}
	if doc.Tables[0].Cells[0].Formula != "" || doc.Tables[0].Cells[0].CachedValue != "" {
		t.Fatalf("cross-sheet enrichment: %+v", doc.Tables[0].Cells[0])
	}
}

// Engines that expose only the sheet index (xberg) still get enrichment, but only
// via the reported index - never via table order.
func TestEnrichUsesReportedSheetIndex(t *testing.T) {
	path := writeMinimalXLSX(t)
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 2, Cols: 4,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Page: 1}, // sheet 1, no name
		Cells: []documentir.Cell{{Row: 1, Col: 1, Text: "42"}}}}
	applied, _, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil || applied != 1 {
		t.Fatalf("applied = %d err = %v", applied, err)
	}
	if doc.Tables[0].Cells[0].Formula != "=SUM(A1:A2)" {
		t.Fatalf("formula = %q", doc.Tables[0].Cells[0].Formula)
	}

	// An out-of-range index is skipped rather than guessed.
	doc2 := documentir.New()
	doc2.Tables = []documentir.Table{{ID: "t", Rows: 2, Cols: 4,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Page: 99},
		Cells: []documentir.Cell{{Row: 1, Col: 1, Text: "42"}}}}
	if applied, _, _ := EnrichXLSXFormulas(context.Background(), path, doc2); applied != 0 {
		t.Fatalf("out-of-range sheet index enriched %d cells", applied)
	}
}

// Enrichment must stop when the parse context is cancelled.
func TestEnrichRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	doc := documentir.New()
	if _, _, err := EnrichXLSXFormulas(ctx, writeMinimalXLSX(t), doc); err == nil {
		t.Fatal("cancelled context ignored")
	}
}

// Decoding itself must observe cancellation (not just the per-sheet loop).
func TestReadZipXMLRespectsCancellation(t *testing.T) {
	path := writeMinimalXLSX(t)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := readZipXML[xlsxWorksheet](ctx, zr, "xl/worksheets/sheet1.xml"); err == nil {
		t.Fatal("cancelled context ignored during decoding")
	}
	if _, err := readZipXML[xlsxWorksheet](context.Background(), zr, "xl/worksheets/sheet1.xml"); err != nil {
		t.Fatalf("live context failed to decode: %v", err)
	}
}

// When the used range does not start at A1 the table-local coordinates cannot be
// mapped to worksheet addresses; enrichment must skip and say so.
func TestEnrichSkipsWhenOriginIsNotA1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "offset.xlsx")
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
	// Data starts at C3, so table-local (0,0) is worksheet C3 - unknown offset.
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="3"><c r="C3"><f>SUM(C4:C5)</f><v>7</v></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 1, Cols: 1,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "S"},
		Cells: []documentir.Cell{{Row: 0, Col: 0, Text: "7"}}}}
	applied, warnings, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("applied %d formulas with an unknown origin", applied)
	}
	if doc.Tables[0].Cells[0].Formula != "" {
		t.Fatalf("formula invented: %q", doc.Tables[0].Cells[0].Formula)
	}
	found := false
	for _, w := range warnings {
		if w.Code == documentir.WarnFormulaMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning for the unmappable origin: %v", warnings)
	}
}

// Two tables on one sheet: the sheet's A1 origin does not locate the second table,
// so neither is enriched and the IR says why.
func TestEnrichSkipsMultiTableSheets(t *testing.T) {
	path := writeMinimalXLSX(t)
	doc := documentir.New()
	loc := &documentir.SourceLocation{Kind: "sheet", Sheet: "Sheet1"}
	doc.Tables = []documentir.Table{
		{ID: "t1", Rows: 2, Cols: 4, Loc: loc, Cells: []documentir.Cell{{Row: 1, Col: 1, Text: "42"}}},
		{ID: "t2", Rows: 2, Cols: 4, Loc: loc, Cells: []documentir.Cell{{Row: 0, Col: 0, Text: "x"}}},
	}
	applied, warnings, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("multi-table sheet enriched %d cells", applied)
	}
	for _, t2 := range doc.Tables {
		for _, c := range t2.Cells {
			if c.Formula != "" || c.CachedValue != "" {
				t.Fatalf("cross-table enrichment: %+v", c)
			}
		}
	}
	found := false
	for _, w := range warnings {
		if w.Code == documentir.WarnFormulaMissing && strings.Contains(w.Message, "several tables") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no multi-table warning: %v", warnings)
	}
}
