package engines

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// xlsxPart is one zip entry; body is streamed so tests can build large sheets without
// holding their XML in memory.
type xlsxPart struct {
	name string
	body func(w io.Writer) error
}

func literal(s string) func(io.Writer) error {
	return func(w io.Writer) error { _, err := io.WriteString(w, s); return err }
}

func writeXLSX(t *testing.T, parts ...xlsxPart) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "book.xlsx")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, part := range parts {
		w, err := zw.Create(part.name)
		if err != nil {
			t.Fatal(err)
		}
		if err := part.body(w); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

const testWorkbookRels = `<?xml version="1.0"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="chartsheet" Target="chartsheets/sheet1.xml"/>
  <Relationship Id="rId3" Type="worksheet" Target="/xl/worksheets/sheet2.xml"/>
</Relationships>`

const testWorkbook = `<?xml version="1.0"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"
          xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Ledger" sheetId="1" r:id="rId1"/>
    <sheet name="Chart" sheetId="2" r:id="rId2"/>
    <sheet name="Notes" sheetId="3" r:id="rId3"/>
  </sheets>
</workbook>`

const testStyles = `<?xml version="1.0"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <numFmts count="2">
    <numFmt numFmtId="164" formatCode="yyyy\-mm\-dd hh:mm"/>
    <numFmt numFmtId="165" formatCode="&quot;day&quot;0.00"/>
  </numFmts>
  <cellXfs count="4">
    <xf numFmtId="0"/>
    <xf numFmtId="14"/>
    <xf numFmtId="164"/>
    <xf numFmtId="165"/>
  </cellXfs>
</styleSheet>`

func parseStream(t *testing.T, p string) *documentir.Document {
	t.Helper()
	x := &XLSXStream{}
	doc, err := x.Parse(context.Background(), Request{Path: p, Format: "xlsx"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := documentir.DefaultLimits.Validate(doc); err != nil {
		t.Fatalf("IR limits: %v", err)
	}
	return doc
}

func tableCell(t *testing.T, tbl documentir.Table, row, col int) documentir.Cell {
	t.Helper()
	for _, c := range tbl.Cells {
		if c.Row == row && c.Col == col {
			return c
		}
	}
	t.Fatalf("no cell (%d,%d) in %s", row, col, tbl.ID)
	return documentir.Cell{}
}

func TestXLSXStreamFullWorkbook(t *testing.T) {
	p := writeXLSX(t,
		xlsxPart{"xl/workbook.xml", literal(testWorkbook)},
		xlsxPart{"xl/_rels/workbook.xml.rels", literal(testWorkbookRels)},
		xlsxPart{"xl/styles.xml", literal(testStyles)},
		xlsxPart{"xl/sharedStrings.xml", literal(`<?xml version="1.0"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <si><t>Account</t></si>
  <si><r><t>Rich </t></r><r><t>text</t></r><rPh><t>PHONETIC</t></rPh></si>
  <si><t xml:space="preserve"> padded &amp; escaped </t></si>
</sst>`)},
		xlsxPart{"xl/worksheets/sheet1.xml", literal(`<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1"><c r="A1" t="s"><v>0</v></c><c r="C1" t="s"><v>1</v></c></row>
    <row r="3">
      <c r="A3"><v>58674.53</v></c>
      <c r="B3" s="1"><v>44774</v></c>
      <c r="C3" s="2"><v>44774.5</v></c>
      <c r="D3" t="b"><v>1</v></c>
      <c r="E3" t="inlineStr"><is><t>inline</t></is></c>
      <c r="F3" t="str"><f>A3*2</f><v>117349.06</v></c>
      <c r="G3" s="3"><v>2.5</v></c>
      <c r="H3" t="s"><v>2</v></c>
    </row>
    <row r="4"><c><v>7</v></c><c><v>8</v></c><c r="Z4" t="e"><v>#N/A</v></c></row>
  </sheetData>
</worksheet>`)},
		xlsxPart{"xl/worksheets/sheet2.xml", literal(`<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData/></worksheet>`)},
	)
	doc := parseStream(t, p)
	if doc.PageKind != "sheet" || doc.PageCount != 2 {
		t.Fatalf("page kind/count = %s/%d, want sheet/2 (chart sheet skipped)", doc.PageKind, doc.PageCount)
	}
	if len(doc.Tables) != 1 {
		t.Fatalf("tables = %d, want 1 (empty sheet has none)", len(doc.Tables))
	}
	tbl := doc.Tables[0]
	if tbl.Loc == nil || tbl.Loc.Sheet != "Ledger" || tbl.Loc.Range != "A1:Z4" {
		t.Fatalf("loc = %+v", tbl.Loc)
	}
	// Rows are the rows that hold values: sheet rows 1, 3, 4.
	if tbl.Rows != 3 || tbl.Cols != 26 {
		t.Fatalf("shape = %dx%d, want 3x26", tbl.Rows, tbl.Cols)
	}
	want := []struct {
		row, col    int
		text, dtype string
	}{
		{0, 0, "Account", "string"},
		{0, 2, "Rich text", "string"}, // runs joined, phonetic run dropped
		{1, 0, "58674.53", "number"},
		{1, 1, "2022-08-01", "date"},
		{1, 2, "2022-08-01 12:00:00", "date"},
		{1, 3, "TRUE", "bool"},
		{1, 4, "inline", "string"},
		{1, 5, "117349.06", "string"},
		{1, 6, "2.5", "number"}, // quoted "day" literal is not a date token
		{1, 7, " padded & escaped ", "string"},
		{2, 0, "7", "number"}, // cells without r= continue left to right
		{2, 1, "8", "number"},
		{2, 25, "#N/A", "string"},
	}
	for _, w := range want {
		c := tableCell(t, tbl, w.row, w.col)
		if c.Text != w.text || c.DataType != w.dtype {
			t.Errorf("(%d,%d) = %q/%s, want %q/%s", w.row, w.col, c.Text, c.DataType, w.text, w.dtype)
		}
	}
	var sections []string
	for _, e := range doc.Elements {
		if e.Type == documentir.ElSection {
			sections = append(sections, e.Text)
		}
	}
	if strings.Join(sections, ",") != "Ledger,Notes" {
		t.Fatalf("sections = %v", sections)
	}
	for _, w := range doc.Warnings {
		if w.Code == documentir.WarnTruncated {
			t.Fatalf("small workbook must not be truncated: %+v", w)
		}
	}
	md := documentir.RenderMarkdown(doc)
	if !strings.Contains(md, "## Ledger") || !strings.Contains(md, "2022-08-01") {
		t.Fatalf("markdown missing content:\n%s", md)
	}
}

// ledger streams a sheet of n data rows under two header rows, like the bank ledgers
// that motivated the engine (company code, amount, date, free text).
func ledger(n int) func(io.Writer) error {
	return func(w io.Writer) error {
		if _, err := io.WriteString(w, `<?xml version="1.0"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>
<row r="1"><c r="A1" t="inlineStr"><is><t>Company Code</t></is></c><c r="B1" t="inlineStr"><is><t>Amount</t></is></c><c r="C1" t="inlineStr"><is><t>Value Date</t></is></c><c r="D1" t="inlineStr"><is><t>Text</t></is></c></row>
<row r="2"><c r="A2" t="inlineStr"><is><t>Empr</t></is></c><c r="B2" t="inlineStr"><is><t>Montante</t></is></c></row>
`); err != nil {
			return err
		}
		for i := 0; i < n; i++ {
			r := i + 3
			if _, err := fmt.Fprintf(w,
				`<row r="%d"><c r="A%d" t="s"><v>%d</v></c><c r="B%d"><v>%d.25</v></c><c r="C%d" s="1"><v>%d</v></c><c r="D%d" t="inlineStr"><is><t>item %d</t></is></c></row>`+"\n",
				r, r, i%3, r, i-5, r, 44774+i%30, r, i); err != nil {
				return err
			}
		}
		_, err := io.WriteString(w, `</sheetData></worksheet>`)
		return err
	}
}

func ledgerBook(t *testing.T, rows ...int) string {
	t.Helper()
	var sheets, rels strings.Builder
	parts := []xlsxPart{
		{"xl/styles.xml", literal(testStyles)},
		{"xl/sharedStrings.xml", literal(`<sst><si><t>C001</t></si><si><t>D001</t></si><si><t>E001</t></si></sst>`)},
	}
	for i, n := range rows {
		fmt.Fprintf(&sheets, `<sheet name="S%d" sheetId="%d" r:id="rId%d"/>`, i+1, i+1, i+1)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Target="worksheets/sheet%d.xml"/>`, i+1, i+1)
		parts = append(parts, xlsxPart{fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1), ledger(n)})
	}
	parts = append(parts,
		xlsxPart{"xl/workbook.xml", literal(`<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>` +
			sheets.String() + `</sheets></workbook>`)},
		xlsxPart{"xl/_rels/workbook.xml.rels", literal(`<Relationships>` + rels.String() + `</Relationships>`)})
	return writeXLSX(t, parts...)
}

func shrinkStreamBounds(t *testing.T, fullRows, fullCells, docCells, sample int) {
	t.Helper()
	oldRows, oldCells, oldDoc, oldSample := xlsxStreamFullRows, xlsxStreamFullCells, xlsxStreamDocCells, xlsxStreamSampleRows
	xlsxStreamFullRows, xlsxStreamFullCells, xlsxStreamDocCells, xlsxStreamSampleRows = fullRows, fullCells, docCells, sample
	t.Cleanup(func() {
		xlsxStreamFullRows, xlsxStreamFullCells, xlsxStreamDocCells, xlsxStreamSampleRows = oldRows, oldCells, oldDoc, oldSample
	})
}

func TestXLSXStreamSummarisesHugeSheet(t *testing.T) {
	shrinkStreamBounds(t, 100, 1_000_000, 5_000_000, 5)
	doc := parseStream(t, ledgerBook(t, 1000))

	if len(doc.Tables) != 2 {
		t.Fatalf("tables = %d, want sample + statistics", len(doc.Tables))
	}
	sample, stats := doc.Tables[0], doc.Tables[1]
	if sample.Rows != 5 || sample.Cols != 4 || sample.Loc.Range != "A1:D5" { // rows 1..5, no gaps
		t.Fatalf("sample = %dx%d %s", sample.Rows, sample.Cols, sample.Loc.Range)
	}
	if c := tableCell(t, sample, 0, 0); c.Text != "Company Code" {
		t.Fatalf("sample keeps the header rows, got %q", c.Text)
	}
	if stats.Rows != 5 || stats.Cols != 9 { // header + one row per column
		t.Fatalf("stats shape = %dx%d", stats.Rows, stats.Cols)
	}
	// Column B: 1000 numbers from -5.25 (written "-5.25") to 994.25 plus two header texts.
	got := map[int]string{}
	for _, c := range stats.Cells {
		if c.Row == 2 {
			got[c.Col] = c.Text
		}
	}
	wantB := map[int]string{0: "B", 1: "Amount", 2: "1002", 3: "1000", 4: "0", 5: "2", 6: "-5.25", 7: "994.25", 8: "1002"}
	for col, w := range wantB {
		if got[col] != w {
			t.Errorf("stats column B[%d] = %q, want %q (row %v)", col, got[col], w, got)
		}
	}
	// Column C: dates between 2022-08-01 and 2022-08-30.
	for _, c := range stats.Cells {
		if c.Row == 3 && (c.Col == 6 || c.Col == 7) {
			if c.Text != map[int]string{6: "2022-08-01", 7: "2022-08-30"}[c.Col] {
				t.Errorf("date range %d = %q", c.Col, c.Text)
			}
		}
	}
	var truncated, para bool
	for _, w := range doc.Warnings {
		truncated = truncated || w.Code == documentir.WarnTruncated
	}
	for _, e := range doc.Elements {
		para = para || (e.Type == documentir.ElParagraph && strings.Contains(e.Text, "1,002 rows x 4 columns"))
	}
	if !truncated || !para {
		t.Fatalf("summary must say it is truncated (warning %v, paragraph %v)", truncated, para)
	}
}

func TestXLSXStreamWorkbookCellBudget(t *testing.T) {
	// Each sheet alone fits; together they exceed the workbook budget, so the second one
	// is summarised.
	shrinkStreamBounds(t, 1000, 1_000_000, 500, 3)
	doc := parseStream(t, ledgerBook(t, 100, 100))
	if len(doc.Tables) != 3 {
		t.Fatalf("tables = %d, want full + sample + stats", len(doc.Tables))
	}
	if doc.Tables[0].Rows != 102 || doc.Tables[1].Rows != 3 {
		t.Fatalf("rows = %d/%d", doc.Tables[0].Rows, doc.Tables[1].Rows)
	}
}

func TestXLSXStreamCancelled(t *testing.T) {
	p := ledgerBook(t, 50_000)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := (&XLSXStream{}).Parse(ctx, Request{Path: p, Format: "xlsx"})
	var ee *Error
	if !errors.As(err, &ee) || ee.Code != jobs.ErrTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
}

func TestXLSXStreamRejectsNonZip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.xlsx")
	if err := os.WriteFile(p, []byte("not a zip"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := (&XLSXStream{}).Parse(context.Background(), Request{Path: p, Format: "xlsx"})
	var ee *Error
	if !errors.As(err, &ee) || ee.Code != jobs.ErrMalformed {
		t.Fatalf("err = %v, want malformed_file", err)
	}
}

func TestClassifyNumFmt(t *testing.T) {
	cases := []struct {
		id         int
		code       string
		date, time bool
	}{
		{0, "", false, false},
		{14, "", true, false},
		{22, "", true, true},
		{21, "", true, true},
		{4, "#,##0.00", false, false},
		{164, `yyyy\-mm\-dd`, true, false},
		{165, "[$-409]d-mmm-yy;@", true, false},
		{166, "hh:mm:ss", true, true},
		{167, `"Day "0`, false, false},
		{168, "[Red]0.00", false, false},
	}
	for _, c := range cases {
		got := classifyNumFmt(c.id, c.code)
		if got.date != c.date || got.timeOfDay != c.time {
			t.Errorf("classifyNumFmt(%d,%q) = %+v, want date=%v time=%v", c.id, c.code, got, c.date, c.time)
		}
	}
}

func TestExcelSerialToText(t *testing.T) {
	cases := map[string]string{
		"44774":     "2022-08-01",
		"44774.75":  "2022-08-01 18:00:00",
		"1":         "1900-01-01",
		"60":        "1900-03-01", // Excel's phantom 1900-02-29 has no calendar date
		"61":        "1900-03-01",
		"2958465":   "9999-12-31",
		"abc":       "abc",
		"-1":        "-1",
		"999999999": "999999999",
	}
	for in, want := range cases {
		got, _ := excelSerialToText(in, false)
		if got != want {
			t.Errorf("excelSerialToText(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestColumnName(t *testing.T) {
	for in, want := range map[int]string{0: "A", 25: "Z", 26: "AA", 701: "ZZ", 702: "AAA", 18: "S"} {
		if got := columnName(in); got != want {
			t.Errorf("columnName(%d) = %s, want %s", in, got, want)
		}
	}
	if groupThousands(781913) != "781,913" || groupThousands(999) != "999" || groupThousands(1000) != "1,000" {
		t.Fatal("groupThousands")
	}
}

func TestXLSXStreamSampleRangeFollowsSheetRows(t *testing.T) {
	// Empty rows are not table rows, so the sample's range must come from the sheet's own
	// row numbers, not from counting.
	shrinkStreamBounds(t, 2, 1_000_000, 5_000_000, 2)
	var rows strings.Builder
	for _, r := range []int{1, 10, 20, 30} {
		fmt.Fprintf(&rows, `<row r="%d"><c r="A%d"><v>%d</v></c></row>`, r, r, r)
	}
	p := writeXLSX(t,
		xlsxPart{"xl/workbook.xml", literal(`<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="G" sheetId="1" r:id="rId1"/></sheets></workbook>`)},
		xlsxPart{"xl/_rels/workbook.xml.rels", literal(`<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`)},
		xlsxPart{"xl/worksheets/sheet1.xml", literal(`<worksheet><sheetData>` + rows.String() + `</sheetData></worksheet>`)},
	)
	doc := parseStream(t, p)
	if got := doc.Tables[0].Loc.Range; got != "A1:A10" {
		t.Fatalf("sample range = %s, want A1:A10", got)
	}
	if got := doc.Tables[1].Loc.Range; got != "A1:A30" {
		t.Fatalf("used range = %s, want A1:A30", got)
	}
}
