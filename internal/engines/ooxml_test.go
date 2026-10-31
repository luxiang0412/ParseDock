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
// The table mirrors what an engine produces (every cell, not just the formula
// cells), because origin verification compares it with the sheet content.
func TestEnrichXLSXFormulas(t *testing.T) {
	path := filepath.Join("..", "..", "benchmark", "fixtures", "generated", "xlsx_workbook.xlsx")
	doc := documentir.New()
	doc.Tables = []documentir.Table{
		{ID: "tbl_1", Rows: 9, Cols: 4,
			Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: "Summary"},
			Cells: []documentir.Cell{
				{Row: 0, Col: 0, Text: "Region", DataType: "string"},
				{Row: 0, Col: 1, Text: "Pallets", DataType: "string"},
				{Row: 0, Col: 2, Text: "On-time %", DataType: "string"},
				{Row: 0, Col: 3, Text: "Cost", DataType: "string"},
				{Row: 1, Col: 0, Text: "North", DataType: "string"},
				{Row: 6, Col: 1, Text: "128400", DataType: "number"}, // B7 = SUM(B2:B5)
				{Row: 6, Col: 2, Text: "95.7", DataType: "number"},   // C7 = AVERAGE(C2:C5)
				{Row: 8, Col: 0, Text: "Merged note: figures unaudited", DataType: "string"},
			}},
		{ID: "tbl_2", Rows: 5, Cols: 5,
			Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: "Detail"},
			Cells: []documentir.Cell{
				{Row: 0, Col: 0, Text: "Date", DataType: "string"},
				{Row: 0, Col: 1, Text: "Shipments", DataType: "string"},
				{Row: 4, Col: 1, Text: "5573", DataType: "number"}, // B5 = SUM(B2:B4)
			}},
	}
	applied, warnings, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil {
		t.Fatal(err)
	}
	if applied < 3 {
		t.Fatalf("applied %d formulas, want >= 3 (warnings=%v)", applied, warnings)
	}
	summary := doc.Tables[0]
	if c := cellAt(t, summary, 6, 1); c.Formula != "=SUM(B2:B5)" {
		t.Fatalf("B7 formula = %q", c.Formula)
	}
	// openpyxl (the fixture generator) writes formulas without cached values, so
	// the enrichment must not invent one.
	if c := cellAt(t, summary, 6, 1); c.CachedValue != "" {
		t.Fatalf("cached value invented: %q", c.CachedValue)
	}
	if c := cellAt(t, summary, 6, 2); c.Formula != "=AVERAGE(C2:C5)" {
		t.Fatalf("C7 formula = %q", c.Formula)
	}
	if c := cellAt(t, doc.Tables[1], 4, 1); c.Formula != "=SUM(B2:B4)" {
		t.Fatalf("Detail B5 formula = %q", c.Formula)
	}
	// A merged note without a formula must stay untouched.
	if c := cellAt(t, summary, 8, 0); c.Formula != "" {
		t.Fatal("formula invented for a cell without <f>")
	}
}

func cellAt(t *testing.T, table documentir.Table, row, col int) *documentir.Cell {
	t.Helper()
	for i := range table.Cells {
		if table.Cells[i].Row == row && table.Cells[i].Col == col {
			return &table.Cells[i]
		}
	}
	t.Fatalf("no cell at (%d,%d) in table %s", row, col, table.ID)
	return nil
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
			{Row: 0, Col: 0, Text: "1", DataType: "number"},
			{Row: 0, Col: 3, Text: "Shared label", DataType: "string"},
			{Row: 1, Col: 1, Text: "42"},
			// Adapters guess these from text; the explicit OOXML type must win.
			{Row: 1, Col: 2, Text: "ab", DataType: "string"},
			{Row: 1, Col: 3, Text: "1", DataType: "number"},
		}}}
	applied, _, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 3 {
		t.Fatalf("applied = %d, want 3 formulas", applied)
	}
	tbl := doc.Tables[0]
	if c := cellAt(t, tbl, 1, 1); c.Formula != "=SUM(A1:A2)" || c.CachedValue != "42" || c.DataType != "number" {
		t.Fatalf("numeric formula: %+v", *c)
	}
	if c := cellAt(t, tbl, 1, 2); c.Formula != `=CONCAT("a","b")` || c.CachedValue != "ab" || c.DataType != "string" {
		t.Fatalf("string formula must keep string type: %+v", *c)
	}
	if c := cellAt(t, tbl, 1, 3); c.Formula != "=1=1" || c.DataType != "bool" {
		t.Fatalf("boolean formula must keep bool type: %+v", *c)
	}
	// An ordinary shared-string cell keeps the engine's text and gains nothing.
	if c := cellAt(t, tbl, 0, 3); c.Formula != "" || c.CachedValue != "" || c.DataType != "string" {
		t.Fatalf("ordinary cell modified: %+v", *c)
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
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1" s="1"><f>TODAY()</f><v>45123</v></c><c r="B1" t="inlineStr"><is><t>label</t></is></c><c r="C1"><v>5</v></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 1, Cols: 3,
		Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: "S"},
		Cells: []documentir.Cell{
			{Row: 0, Col: 0, Text: "45123", DataType: "date"},
			{Row: 0, Col: 1, Text: "label", DataType: "string"},
			{Row: 0, Col: 2, Text: "5", DataType: "number"},
		}}}
	if _, _, err := EnrichXLSXFormulas(context.Background(), path, doc); err != nil {
		t.Fatal(err)
	}
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
	add("xl/worksheets/sheet1.xml", `<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData><row r="1"><c r="A1"><f t="shared" ref="A1:A2" si="0">A2+1</f><v>2</v></c><c r="B1"><v>2</v></c><c r="C1"><v>9</v></c><c r="A2"><f t="shared" si="0"/></c></row></sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 2, Cols: 3,
		Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: "S"},
		Cells: []documentir.Cell{{Row: 0, Col: 0}, {Row: 1, Col: 0},
			{Row: 0, Col: 1, Text: "2"}, {Row: 0, Col: 2, Text: "9"}}}}
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
		Loc: &documentir.SourceLocation{Kind: "sheet", Page: 1}, // sheet 1, no name
		Cells: []documentir.Cell{{Row: 0, Col: 0, Text: "1"}, {Row: 0, Col: 3, Text: "Shared label"},
			{Row: 1, Col: 1, Text: "42"}}}}
	applied, _, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil || applied != 1 {
		t.Fatalf("applied = %d err = %v", applied, err)
	}
	if c := cellAt(t, doc.Tables[0], 1, 1); c.Formula != "=SUM(A1:A2)" {
		t.Fatalf("formula = %q", c.Formula)
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

// Review finding: a part larger than the read ceiling was cut silently. It must be an
// explicit expanded_too_large, and enrichment must not run on a partial string table.
func TestReadZipXMLRefusesOversizedPart(t *testing.T) {
	path := writeMinimalXLSX(t)
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	old := zipXMLMax
	zipXMLMax = 16
	t.Cleanup(func() { zipXMLMax = old })
	_, err = readZipXML[xlsxWorkbook](context.Background(), zr, "xl/workbook.xml")
	if !isExpandedTooLarge(err) {
		t.Fatalf("err = %v, want expanded_too_large", err)
	}
	doc := documentir.New()
	if _, _, err := EnrichXLSXFormulas(context.Background(), path, doc); !isExpandedTooLarge(err) {
		t.Fatalf("enrichment err = %v, want expanded_too_large", err)
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

// A sub-block of a sheet (table dimensions smaller than the used range) is
// ambiguous: a shifted offset could match repeated labels. Skip it.
func TestEnrichSkipsSubBlockTables(t *testing.T) {
	path := writeMinimalXLSX(t) // used range A1:D2
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 1, Cols: 2,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "Sheet1"},
		Cells: []documentir.Cell{{Row: 0, Col: 0, Text: "1"}, {Row: 0, Col: 1, Text: "x"}}}}
	applied, warnings, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("sub-block table enriched %d cells", applied)
	}
	found := false
	for _, w := range warnings {
		if w.Code == documentir.WarnFormulaMissing {
			found = true
		}
	}
	if !found {
		t.Fatalf("no ambiguity warning: %v", warnings)
	}
}

// A named sheet without formulas must not produce a warning.
func TestEnrichSilentForFormulaFreeSheet(t *testing.T) {
	path := writeMinimalXLSX(t)
	doc := documentir.New()
	doc.Tables = []documentir.Table{{ID: "t", Rows: 2, Cols: 4,
		Loc:   &documentir.SourceLocation{Kind: "sheet", Sheet: "NoFormulas"},
		Cells: []documentir.Cell{{Row: 0, Col: 0, Text: "x"}}}}
	applied, warnings, err := EnrichXLSXFormulas(context.Background(), path, doc)
	if err != nil || applied != 0 {
		t.Fatalf("applied = %d err = %v", applied, err)
	}
	if len(warnings) != 0 {
		t.Fatalf("spurious warning for a formula-free sheet: %v", warnings)
	}
}
