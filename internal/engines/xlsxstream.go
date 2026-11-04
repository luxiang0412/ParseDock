package engines

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"path"
	"strconv"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// XLSXStream is a pure-Go, streaming XLSX reader for workbooks that are too large for
// the subprocess engines: xberg refuses archives that expand beyond 500 MiB, docling-rs
// sheets beyond 10M cells, and both cap their result at 256 MiB. Real ledgers (one sheet,
// ~800k rows, 550-770 MiB of worksheet XML) hit all three.
//
// The worksheet is decoded token by token, so memory is bounded by the shared-string
// table plus the rows kept for output, not by the sheet size. A sheet small enough to
// render is emitted in full. A larger sheet is emitted as a bounded summary — its size
// and used range, the first rows as a sample table, and per-column statistics — with a
// "truncated" warning: hundreds of megabytes of Markdown are of no use to a reader or a
// model, and the IR caps one table at 10M cells anyway. The original file remains the
// source for the full data.
//
// The coordinator routes an XLSX here only when its expanded size reaches
// --xlsx-stream-min-expanded-mb (see coordinator.Service.route); the engine's priority
// is the lowest, so it never takes ordinary workbooks away from xberg/docling.
type XLSXStream struct{ VersionStr string }

// XLSXStreamName is the engine name used in routing and registration.
const XLSXStreamName = "xlsx-stream"

// Output bounds (variables so tests can shrink them).
var (
	// xlsxStreamFullRows / xlsxStreamFullCells: a sheet within both is emitted in full.
	xlsxStreamFullRows  = 50_000
	xlsxStreamFullCells = 1_000_000
	// xlsxStreamDocCells bounds the full tables of one workbook; sheets after the budget
	// is spent are summarised even when each would fit on its own (IR limit: 20M).
	xlsxStreamDocCells = 5_000_000
	// xlsxStreamSampleRows is the number of leading rows a summarised sheet keeps.
	xlsxStreamSampleRows = 100
	// xlsxStreamDistinctCap bounds the distinct-value set tracked per column.
	xlsxStreamDistinctCap = 10_000
	// xlsxStreamSSTMax bounds the decompressed shared-string part. Strings are held in
	// memory for the whole parse; beyond this the workbook is refused explicitly.
	xlsxStreamSSTMax int64 = 1 << 30
)

// Name implements Engine.
func (x *XLSXStream) Name() string { return XLSXStreamName }

// Version implements Engine.
func (x *XLSXStream) Version() string {
	if x.VersionStr != "" {
		return x.VersionStr
	}
	return "go-xlsx-stream-1"
}

// Device implements Engine.
func (x *XLSXStream) Device() string { return "cpu" }

// Formats implements Engine.
func (x *XLSXStream) Formats() []string { return []string{"xlsx"} }

// Modes implements Engine. The output does not depend on the mode.
func (x *XLSXStream) Modes() []string { return []string{"fast", "accurate", "auto"} }

// Probe implements Engine.
func (x *XLSXStream) Probe(context.Context) error { return nil }

// Parse implements Engine.
func (x *XLSXStream) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	req.Report(CPUEvidence("xlsx-stream:in-process", "the streaming XLSX reader is pure Go and has no GPU path"))
	zr, err := zip.OpenReader(req.Path)
	if err != nil {
		return nil, Errf(false, jobs.ErrMalformed, "not a readable XLSX package: %v", err)
	}
	defer zr.Close()

	sheets, err := xlsxStreamSheets(ctx, zr)
	if err != nil {
		return nil, err
	}
	sst, err := xlsxStreamSharedStrings(ctx, zr)
	if err != nil {
		return nil, err
	}
	dates, err := xlsxStreamDateStyles(ctx, zr)
	if err != nil {
		return nil, err
	}

	// Progress is reported against the uncompressed size of the worksheets, which is
	// where nearly all of the time goes.
	var total int64
	sizes := map[string]int64{}
	for _, f := range zr.File {
		sizes[f.Name] = int64(f.UncompressedSize64)
	}
	for _, sh := range sheets {
		total += sizes[sh.part]
	}

	doc := documentir.New()
	doc.ParserEngine = x.Name()
	doc.ParserVersion = x.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = fileSize(req.Path)
	doc.PageKind = "sheet"

	b := &xlsxStreamBuilder{doc: doc, cellBudget: xlsxStreamDocCells}
	var done int64
	for i, sh := range sheets {
		if err := ctx.Err(); err != nil {
			return nil, Errf(true, jobs.ErrTimeout, "parse cancelled")
		}
		progress := func(read int64) {
			if req.Progress != nil && total > 0 {
				req.Progress(int(5+90*(done+read)/total), "reading_sheet")
			}
		}
		res, err := streamSheet(ctx, zr, sh.part, sst, dates, b.sheetFullBudget(), progress)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, Errf(true, jobs.ErrTimeout, "parse cancelled")
			}
			return nil, Errf(false, jobs.ErrMalformed, "sheet %d: %v", i+1, err)
		}
		done += sizes[sh.part]
		b.addSheet(i+1, sh.name, res)
	}
	if len(doc.Elements) == 0 {
		doc.AddWarning(documentir.WarnPartialContent, "workbook contains no cells", "warn")
	}
	doc.PageCount = len(sheets)
	doc.Metrics.Pages = len(sheets)
	doc.Metrics.InputBytes = doc.Source.SizeBytes
	return doc, nil
}

// xlsxStreamSheets lists the workbook's worksheets in tab order. Chart sheets and
// dialog sheets have no cell data and are skipped.
func xlsxStreamSheets(ctx context.Context, zr *zip.ReadCloser) ([]xlsxSheetRef, error) {
	workbook, err := readZipXML[xlsxWorkbook](ctx, zr, "xl/workbook.xml")
	if err != nil {
		if ctx.Err() != nil {
			return nil, Errf(true, jobs.ErrTimeout, "parse cancelled")
		}
		if isExpandedTooLarge(err) {
			return nil, err
		}
		return nil, Errf(false, jobs.ErrMalformed, "workbook.xml: %v", err)
	}
	rels, err := readZipXML[xlsxRels](ctx, zr, "xl/_rels/workbook.xml.rels")
	if err != nil {
		if ctx.Err() != nil {
			return nil, Errf(true, jobs.ErrTimeout, "parse cancelled")
		}
		return nil, Errf(false, jobs.ErrMalformed, "workbook relationships: %v", err)
	}
	targets := map[string]string{}
	for _, r := range rels.Relationships {
		targets[r.ID] = r.Target
	}
	var out []xlsxSheetRef
	for _, s := range workbook.Sheets {
		target := targets[s.RID]
		if target == "" {
			continue
		}
		if strings.HasPrefix(target, "/") {
			target = strings.TrimPrefix(target, "/")
		} else {
			target = path.Join("xl", target)
		}
		if !strings.Contains(target, "worksheets/") {
			continue
		}
		out = append(out, xlsxSheetRef{name: s.Name, part: target})
	}
	return out, nil
}

// xlsxStreamSharedStrings decodes the shared-string table token by token. An item's
// text is the concatenation of its <t> elements, excluding phonetic runs (<rPh>).
func xlsxStreamSharedStrings(ctx context.Context, zr *zip.ReadCloser) ([]string, error) {
	f, err := zr.Open("xl/sharedStrings.xml")
	if err != nil {
		return nil, nil // a workbook without strings has no table
	}
	defer f.Close()
	lr := &io.LimitedReader{R: &ctxReader{ctx: ctx, r: f}, N: xlsxStreamSSTMax + 1}
	dec := xml.NewDecoder(bufio.NewReaderSize(lr, 256<<10))
	var (
		out      []string
		cur      strings.Builder
		inSI     bool
		inT      bool
		phonetic int
	)
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, Errf(true, jobs.ErrTimeout, "parse cancelled")
			}
			if lr.N <= 0 {
				return nil, Errf(false, jobs.ErrExpandedTooLarge,
					"shared-string table expands beyond %d MiB", xlsxStreamSSTMax>>20)
			}
			return nil, Errf(false, jobs.ErrMalformed, "sharedStrings.xml: %v", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				cur.Reset()
			case "rPh":
				phonetic++
			case "t":
				inT = inSI && phonetic == 0
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "si":
				out = append(out, cur.String())
				inSI = false
			case "rPh":
				phonetic--
			case "t":
				inT = false
			}
		case xml.CharData:
			if inT {
				cur.Write(t)
			}
		}
	}
	if lr.N <= 0 {
		return nil, Errf(false, jobs.ErrExpandedTooLarge,
			"shared-string table expands beyond %d MiB", xlsxStreamSSTMax>>20)
	}
	return out, nil
}

type xlsxStyles struct {
	NumFmts []struct {
		ID   int    `xml:"numFmtId,attr"`
		Code string `xml:"formatCode,attr"`
	} `xml:"numFmts>numFmt"`
	CellXfs []struct {
		NumFmtID int `xml:"numFmtId,attr"`
	} `xml:"cellXfs>xf"`
}

// xlsxStreamDateStyles returns, per cell style index (the <c s> attribute), whether a
// numeric cell with that style is a date and whether its format shows a time of day.
func xlsxStreamDateStyles(ctx context.Context, zr *zip.ReadCloser) ([]xlsxDateStyle, error) {
	styles, err := readZipXML[xlsxStyles](ctx, zr, "xl/styles.xml")
	if err != nil {
		if ctx.Err() != nil {
			return nil, Errf(true, jobs.ErrTimeout, "parse cancelled")
		}
		return nil, nil // no or unreadable styles: numbers stay numbers
	}
	custom := map[int]string{}
	for _, nf := range styles.NumFmts {
		custom[nf.ID] = nf.Code
	}
	out := make([]xlsxDateStyle, len(styles.CellXfs))
	for i, xf := range styles.CellXfs {
		out[i] = classifyNumFmt(xf.NumFmtID, custom[xf.NumFmtID])
	}
	return out, nil
}

type xlsxDateStyle struct{ date, timeOfDay bool }

// classifyNumFmt recognises the built-in date formats (ECMA-376 §18.8.30) and custom
// codes containing date/time tokens outside quoted literals and [colour] sections.
func classifyNumFmt(id int, code string) xlsxDateStyle {
	switch {
	case id >= 14 && id <= 17:
		return xlsxDateStyle{date: true}
	case id == 22:
		return xlsxDateStyle{date: true, timeOfDay: true}
	case (id >= 18 && id <= 21) || (id >= 45 && id <= 47):
		return xlsxDateStyle{date: true, timeOfDay: true}
	}
	if code == "" {
		return xlsxDateStyle{}
	}
	var (
		inQuote, inBracket bool
		hasDate, hasTime   bool
	)
	lower := strings.ToLower(code)
	for i := 0; i < len(lower); i++ {
		c := lower[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case inQuote:
		case c == '\\':
			i++
		case c == '[':
			inBracket = true
		case c == ']':
			inBracket = false
		case inBracket:
		case c == 'y' || c == 'd':
			hasDate = true
		case c == 'm':
			hasDate = true // "m" is month or minute; either way the value is a date/time
		case c == 'h' || c == 's':
			hasTime = true
		}
	}
	if hasDate || hasTime {
		return xlsxDateStyle{date: true, timeOfDay: hasTime}
	}
	return xlsxDateStyle{}
}

// excelSerialEpoch is day 0 of the 1900 date system as Excel counts it (it treats 1900
// as a leap year, so serials from 61 on are one day ahead of the calendar).
var excelSerialEpoch = time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)

// excelSerialToText renders a date serial as ISO 8601. Serials outside Excel's range
// are returned unchanged.
func excelSerialToText(raw string, timeOfDay bool) (string, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v < 0 || v > 2958465 || math.IsNaN(v) {
		return raw, false
	}
	if v < 61 {
		v++ // 1900-01-01..1900-02-28: compensate the phantom 1900-02-29
	}
	days := math.Floor(v)
	secs := math.Round((v - days) * 86400)
	t := excelSerialEpoch.AddDate(0, 0, int(days)).Add(time.Duration(secs) * time.Second)
	// A time part is rendered when the format asks for one or the value carries one
	// (a date-only format can still hold a timestamp; dropping it would lose data).
	if timeOfDay || secs != 0 {
		return t.Format("2006-01-02 15:04:05"), true
	}
	return t.Format("2006-01-02"), true
}

// xlsxCell is one decoded worksheet cell.
type xlsxCell struct {
	col      int
	text     string
	dataType string // string|number|date|bool ("" for empty)
}

// sheetResult is what streamSheet hands back: either every row (full) or a sample plus
// statistics.
type sheetResult struct {
	rows      [][]xlsxCell // full rows, or the first xlsxStreamSampleRows rows
	rowNums   []int        // 1-based sheet row of each entry in rows
	full      bool
	rowCount  int // rows that contain at least one value
	lastRow   int // 1-based index of the last row with a value
	firstRow  int // 1-based index of the first row with a value
	cols      int // 1 + highest column with a value
	cellCount int // non-empty cells
	stats     []*columnStats
}

type columnStats struct {
	nonEmpty, numbers, dates, texts, bools int
	minNum, maxNum                         float64
	minDate, maxDate                       string
	distinct                               map[string]struct{}
	distinctCapped                         bool
}

func (c *columnStats) add(cell xlsxCell) {
	if cell.text == "" {
		return
	}
	c.nonEmpty++
	switch cell.dataType {
	case "number":
		if v, err := strconv.ParseFloat(cell.text, 64); err == nil {
			if c.numbers == 0 || v < c.minNum {
				c.minNum = v
			}
			if c.numbers == 0 || v > c.maxNum {
				c.maxNum = v
			}
		}
		c.numbers++
	case "date":
		if c.dates == 0 || cell.text < c.minDate {
			c.minDate = cell.text
		}
		if c.dates == 0 || cell.text > c.maxDate {
			c.maxDate = cell.text
		}
		c.dates++
	case "bool":
		c.bools++
	default:
		c.texts++
	}
	if !c.distinctCapped {
		if c.distinct == nil {
			c.distinct = map[string]struct{}{}
		}
		c.distinct[cell.text] = struct{}{}
		if len(c.distinct) > xlsxStreamDistinctCap {
			c.distinct = nil
			c.distinctCapped = true
		}
	}
}

// streamSheet decodes one worksheet part. fullCells is the cell budget left for full
// output; when the sheet exceeds it (or xlsxStreamFullRows) the result degrades to a
// sample plus statistics, and the rows beyond the sample are not retained.
func streamSheet(ctx context.Context, zr *zip.ReadCloser, part string, sst []string,
	dates []xlsxDateStyle, fullCells int, progress func(read int64)) (*sheetResult, error) {
	f, err := zr.Open(part)
	if err != nil {
		return nil, fmt.Errorf("open worksheet: %w", err)
	}
	defer f.Close()
	cr := &countingReader{r: &ctxReader{ctx: ctx, r: f}}
	dec := xml.NewDecoder(bufio.NewReaderSize(cr, 256<<10))

	res := &sheetResult{full: true}
	var (
		row         []xlsxCell
		rowIdx      int // 1-based, from <row r> when present
		inSheetData bool
		inCell      bool
		inV, inIsT  bool
		phonetic    int
		cellCol     int
		cellType    string
		cellStyle   int
		val         strings.Builder
		inline      strings.Builder
		nextCol     int
		lastReport  int64
	)
	flushRow := func() {
		if len(row) == 0 {
			return
		}
		if res.firstRow == 0 {
			res.firstRow = rowIdx
		}
		res.lastRow = rowIdx
		res.rowCount++
		for _, c := range row {
			res.cellCount++
			for len(res.stats) <= c.col {
				res.stats = append(res.stats, &columnStats{})
			}
			res.stats[c.col].add(c)
			if c.col+1 > res.cols {
				res.cols = c.col + 1
			}
		}
		if res.full && (res.rowCount > xlsxStreamFullRows || res.cellCount > fullCells) {
			res.full = false
			if len(res.rows) > xlsxStreamSampleRows {
				res.rows = res.rows[:xlsxStreamSampleRows:xlsxStreamSampleRows]
				res.rowNums = res.rowNums[:xlsxStreamSampleRows:xlsxStreamSampleRows]
			}
		}
		if res.full || len(res.rows) < xlsxStreamSampleRows {
			res.rows = append(res.rows, row)
			res.rowNums = append(res.rowNums, rowIdx)
		}
		row = nil
	}
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return nil, err
		}
		if progress != nil && cr.n-lastReport > 32<<20 {
			lastReport = cr.n
			progress(cr.n)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "sheetData":
				inSheetData = true
			case "row":
				if !inSheetData {
					continue
				}
				rowIdx++
				if r := attr(t, "r"); r != "" {
					if n, err := strconv.Atoi(r); err == nil && n > 0 {
						rowIdx = n
					}
				}
				row, nextCol = nil, 0
			case "c":
				if !inSheetData {
					continue
				}
				inCell = true
				cellType = attr(t, "t")
				cellStyle = -1
				if s := attr(t, "s"); s != "" {
					if n, err := strconv.Atoi(s); err == nil {
						cellStyle = n
					}
				}
				cellCol = nextCol
				if ref := attr(t, "r"); ref != "" {
					if col, _, ok := parseA1(ref); ok {
						cellCol = col
					}
				}
				val.Reset()
				inline.Reset()
			case "v":
				inV = inCell
			case "rPh":
				phonetic++
			case "t":
				inIsT = inCell && phonetic == 0
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "sheetData":
				inSheetData = false
			case "row":
				if inSheetData {
					flushRow()
				}
			case "c":
				if !inCell {
					continue
				}
				inCell = false
				nextCol = cellCol + 1
				cell := decodeCell(cellType, cellStyle, val.String(), inline.String(), sst, dates)
				if cell.text != "" {
					cell.col = cellCol
					row = append(row, cell)
				}
			case "v":
				inV = false
			case "rPh":
				phonetic--
			case "t":
				inIsT = false
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			} else if inIsT {
				inline.Write(t)
			}
		}
	}
	if progress != nil {
		progress(cr.n)
	}
	return res, nil
}

func decodeCell(cellType string, style int, raw, inline string, sst []string,
	dates []xlsxDateStyle) xlsxCell {
	switch cellType {
	case "s":
		idx, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || idx < 0 || idx >= len(sst) {
			return xlsxCell{}
		}
		return xlsxCell{text: sst[idx], dataType: "string"}
	case "inlineStr":
		return xlsxCell{text: inline, dataType: "string"}
	case "str", "e":
		return xlsxCell{text: raw, dataType: "string"}
	case "b":
		switch strings.TrimSpace(raw) {
		case "1":
			return xlsxCell{text: "TRUE", dataType: "bool"}
		case "0":
			return xlsxCell{text: "FALSE", dataType: "bool"}
		}
		return xlsxCell{text: raw, dataType: "string"}
	case "d":
		return xlsxCell{text: raw, dataType: "date"} // ISO 8601 already
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return xlsxCell{}
	}
	if style >= 0 && style < len(dates) && dates[style].date {
		if text, ok := excelSerialToText(raw, dates[style].timeOfDay); ok {
			return xlsxCell{text: text, dataType: "date"}
		}
	}
	return xlsxCell{text: raw, dataType: "number"}
}

func attr(t xml.StartElement, local string) string {
	for _, a := range t.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// xlsxStreamBuilder turns sheet results into IR elements and tables.
type xlsxStreamBuilder struct {
	doc        *documentir.Document
	cellBudget int
}

func (b *xlsxStreamBuilder) sheetFullBudget() int {
	if b.cellBudget < 0 {
		return 0
	}
	if b.cellBudget < xlsxStreamFullCells {
		return b.cellBudget
	}
	return xlsxStreamFullCells
}

func (b *xlsxStreamBuilder) nextElementID() string {
	return fmt.Sprintf("el_%d", len(b.doc.Elements)+1)
}

func (b *xlsxStreamBuilder) addElement(e documentir.Element) {
	e.ID = b.nextElementID()
	e.ReadingOrder = len(b.doc.Elements)
	b.doc.Elements = append(b.doc.Elements, e)
}

func (b *xlsxStreamBuilder) addTable(t documentir.Table, summary string) {
	t.ID = fmt.Sprintf("tbl_%d", len(b.doc.Tables)+1)
	b.doc.Tables = append(b.doc.Tables, t)
	b.addElement(documentir.Element{Type: documentir.ElTable, TableID: t.ID, Text: summary, Loc: t.Loc})
}

func (b *xlsxStreamBuilder) addSheet(index int, name string, res *sheetResult) {
	loc := func(rng string) *documentir.SourceLocation {
		return &documentir.SourceLocation{Kind: "sheet", Page: index, Sheet: name, Range: rng}
	}
	b.addElement(documentir.Element{Type: documentir.ElSection, Level: 2, Text: name, Loc: loc("")})
	if res.rowCount == 0 {
		return
	}
	used := fmt.Sprintf("%s%d:%s%d", columnName(0), res.firstRow, columnName(res.cols-1), res.lastRow)
	if res.full {
		b.cellBudget -= res.cellCount
		t := rowsTable(res.rows, res.cols, loc(used))
		b.addTable(t, fmt.Sprintf("%d rows x %d cols", t.Rows, t.Cols))
		return
	}

	shown := len(res.rows)
	b.addElement(documentir.Element{Type: documentir.ElParagraph, Loc: loc(used), Text: fmt.Sprintf(
		"Sheet %q has %s rows x %d columns (used range %s), too many to render in full. "+
			"The first %d rows are shown, followed by per-column statistics over all rows; "+
			"the full data is in the original workbook.",
		name, groupThousands(res.rowCount), res.cols, used, shown)})
	sample := rowsTable(res.rows, res.cols, loc(fmt.Sprintf("%s%d:%s%d",
		columnName(0), res.firstRow, columnName(res.cols-1), res.rowNums[shown-1])))
	sample.Caption = fmt.Sprintf("First %d of %s rows", shown, groupThousands(res.rowCount))
	b.addTable(sample, fmt.Sprintf("first %d of %d rows x %d cols", shown, res.rowCount, res.cols))
	stats := statsTable(res, loc(used))
	b.addTable(stats, fmt.Sprintf("column statistics for %d columns", res.cols))
	b.doc.AddWarning(documentir.WarnTruncated, fmt.Sprintf(
		"sheet %q: %d rows exceed the full-output bound; emitted the first %d rows and per-column statistics",
		name, res.rowCount, shown), "warn")
}

// rowsTable lays rows out densely; empty cells are omitted (the renderers fill gaps).
func rowsTable(rows [][]xlsxCell, cols int, loc *documentir.SourceLocation) documentir.Table {
	t := documentir.Table{Rows: len(rows), Cols: cols, HeaderRows: 1, Loc: loc}
	for ri, row := range rows {
		for _, c := range row {
			t.Cells = append(t.Cells, documentir.Cell{Row: ri, Col: c.col, RowSpan: 1, ColSpan: 1,
				Text: c.text, DataType: c.dataType})
		}
	}
	return t
}

func statsTable(res *sheetResult, loc *documentir.SourceLocation) documentir.Table {
	header := []string{"Column", "First value", "Non-empty", "Numbers", "Dates", "Text", "Min", "Max", "Distinct"}
	t := documentir.Table{Rows: 1, Cols: len(header), HeaderRows: 1, Loc: loc, Caption: "Per-column statistics"}
	for ci, h := range header {
		t.Cells = append(t.Cells, documentir.Cell{Row: 0, Col: ci, RowSpan: 1, ColSpan: 1, Text: h, DataType: "string"})
	}
	first := map[int]string{}
	if len(res.rows) > 0 {
		for _, c := range res.rows[0] {
			first[c.col] = c.text
		}
	}
	for col := 0; col < res.cols; col++ {
		s := &columnStats{}
		if col < len(res.stats) {
			s = res.stats[col]
		}
		var minV, maxV string
		switch {
		case s.numbers > 0 && s.numbers >= s.dates:
			minV, maxV = formatNumber(s.minNum), formatNumber(s.maxNum)
		case s.dates > 0:
			minV, maxV = s.minDate, s.maxDate
		}
		distinct := strconv.Itoa(len(s.distinct))
		if s.distinctCapped {
			distinct = fmt.Sprintf(">%d", xlsxStreamDistinctCap)
		}
		vals := []string{columnName(col), first[col], strconv.Itoa(s.nonEmpty), strconv.Itoa(s.numbers),
			strconv.Itoa(s.dates), strconv.Itoa(s.texts + s.bools), minV, maxV, distinct}
		r := t.Rows
		for ci, v := range vals {
			if v == "" {
				continue
			}
			dt := "string"
			if ci >= 2 && ci <= 5 {
				dt = "number"
			}
			t.Cells = append(t.Cells, documentir.Cell{Row: r, Col: ci, RowSpan: 1, ColSpan: 1, Text: v, DataType: dt})
		}
		t.Rows++
	}
	return t
}

func formatNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// columnName converts a 0-based column index to its letters (0 -> A, 26 -> AA).
func columnName(col int) string {
	var b []byte
	for col >= 0 {
		b = append(b, byte('A'+col%26))
		col = col/26 - 1
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}

func groupThousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}
