package engines

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"parsedock.dev/parsedock/internal/documentir"
)

// OOXML formula enrichment.
//
// Neither xberg nor docling-rs exposes the cell formula text, and the PR-00
// review flagged "formula text" as a capability gap. The formula lives in the
// worksheet XML (`<c><f>…</f><v>cached</v></c>`), which is cheap to read from the
// input zip. Enrichment is best-effort: it only fills cells the engine already
// produced, never invents cells or sheets, and records a warning when a shared
// formula cannot be expanded safely.

type xlsxSheetRef struct {
	name string
	part string
}

type xlsxWorkbook struct {
	Sheets []struct {
		Name string `xml:"name,attr"`
		RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
	} `xml:"sheets>sheet"`
}

type xlsxRels struct {
	Relationships []struct {
		ID     string `xml:"Id,attr"`
		Target string `xml:"Target,attr"`
	} `xml:"Relationship"`
}

type xlsxWorksheet struct {
	SheetData struct {
		Rows []struct {
			Cells []struct {
				Ref string `xml:"r,attr"`
				T   string `xml:"t,attr"`
				F   struct {
					Text string `xml:",chardata"`
					Type string `xml:"t,attr"`
					SI   string `xml:"si,attr"`
				} `xml:"f"`
				V  string `xml:"v"`
				Is struct {
					T string `xml:"t"`
					R []struct {
						T string `xml:"t"`
					} `xml:"r"`
				} `xml:"is"`
			} `xml:"c"`
		} `xml:"row"`
	} `xml:"sheetData"`
}

type xlsxSharedStrings struct {
	Items []struct {
		Text string `xml:",chardata"`
		Runs []struct {
			Text string `xml:"t"`
		} `xml:"r"`
		T struct {
			Text string `xml:",chardata"`
		} `xml:"t"`
	} `xml:"si"`
}

// sheetFormulas holds the formula cells of one sheet plus the origin of the sheet's
// used range. Engine table coordinates are table-local, so enrichment is only safe
// when the used range starts at A1 and the mapping is therefore an identity.
// maxFormulaCells bounds the formula cells retained for one sheet, and
// maxWorkbookFormulaCells for the whole workbook.
const (
	maxFormulaCells         = 500_000
	maxWorkbookFormulaCells = 1_000_000
)

type sheetFormulas struct {
	originRow, originCol int
	maxRow, maxCol       int
	cells                map[[2]int]formulaCell
	// values holds every cell's text at its worksheet address. It is used to
	// *verify* that a table's local (0,0) really is the worksheet's origin before
	// any formula is attached; without positive evidence enrichment is skipped.
	values       map[[2]int]string
	valuesCapped bool
}

type formulaCell struct {
	formula  string
	cached   string
	dataType string
	// authoritative is true when the workbook states the type explicitly
	// (t="b"/"str"/"s"/"inlineStr"/"d"). Numeric results carry no marker and are
	// only a fallback, because Excel stores dates as plain numbers with a number
	// format that the engine may already have resolved.
	authoritative bool
	shared        bool
}

// EnrichXLSXFormulas adds formula text and cached values to the IR tables of an
// xlsx/xlsm document. It returns the number of cells enriched and a warning list
// (which the caller should add to the document).
func EnrichXLSXFormulas(ctx context.Context, inputPath string, doc *documentir.Document) (int, []documentir.Warning, error) {
	if doc == nil {
		return 0, nil, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	zr, err := zip.OpenReader(inputPath)
	if err != nil {
		return 0, []documentir.Warning{{Code: documentir.WarnUnsupported,
			Message: "formula enrichment skipped: not a readable OOXML package", Severity: "info"}}, nil
	}
	defer zr.Close()

	workbook, err := readZipXML[xlsxWorkbook](ctx, zr, "xl/workbook.xml")
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return 0, nil, cerr // deadline/cancel must reach the worker
		}
		return 0, nil, nil // not an xlsx workbook: nothing to add
	}
	rels, rerr := readZipXML[xlsxRels](ctx, zr, "xl/_rels/workbook.xml.rels")
	if rerr != nil && ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}
	targets := map[string]string{}
	for _, r := range rels.Relationships {
		targets[r.ID] = r.Target
	}

	var sheets []xlsxSheetRef
	for _, s := range workbook.Sheets {
		target := targets[s.RID]
		if target == "" {
			continue
		}
		target = strings.TrimPrefix(target, "/")
		if !strings.HasPrefix(target, "xl/") {
			target = path.Join("xl", target)
		}
		sheets = append(sheets, xlsxSheetRef{name: s.Name, part: target})
	}

	// Shared-string table: a cached formula result of type "s" is an index into it.
	var sharedStrings []string
	sst, sstErr := readZipXML[xlsxSharedStrings](ctx, zr, "xl/sharedStrings.xml")
	if sstErr != nil && ctx.Err() != nil {
		return 0, nil, ctx.Err()
	}
	if sstErr == nil {
		for _, si := range sst.Items {
			text := si.T.Text
			if text == "" {
				for _, r := range si.Runs {
					text += r.Text
				}
			}
			sharedStrings = append(sharedStrings, text)
		}
	}

	formulas := map[string]sheetFormulas{}
	retained := 0
	var warnings []documentir.Warning
	for _, sh := range sheets {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		// A large sheet expands to tens of megabytes of XML (a real 10 MB workbook has one
		// 59 MB worksheet). Unmarshalling that into structs cost several times the parse
		// itself, so: (1) skip sheets with no <f> tag at all using a byte scan of the
		// decompressed stream, and (2) stream the rest instead of building a DOM.
		hasF, err := sheetHasFormulaTag(ctx, zr, sh.part)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return 0, warnings, cerr
			}
			continue
		}
		if !hasF {
			continue
		}
		scan, err := scanSheet(ctx, zr, sh.part, sharedStrings)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return 0, warnings, cerr
			}
			continue // malformed worksheet: best effort
		}
		if len(scan.cells) == 0 {
			continue
		}
		// Per-sheet caps do not bound the workbook: many sheets would still add up. Past the
		// workbook budget the remaining sheets are skipped without enrichment.
		if retained+len(scan.cells) > maxWorkbookFormulaCells {
			continue
		}
		retained += len(scan.cells)
		formulas[sh.name] = *scan
	}
	if len(formulas) == 0 {
		return 0, nil, nil
	}

	applied := 0
	sharedSkipped := false
	originUnknown := false
	unverified := false
	ordered := make([]string, 0, len(sheets))
	for _, sh := range sheets {
		ordered = append(ordered, sh.name)
	}
	// Resolve which worksheet each IR table belongs to, then count tables per
	// sheet: with two tables on one sheet the sheet's A1 origin says nothing about
	// where the second table starts, so enrichment is only safe when a sheet is
	// represented by exactly one table.
	resolved := make([]string, len(doc.Tables))
	perSheet := map[string]int{}
	for ti := range doc.Tables {
		table := &doc.Tables[ti]
		name := ""
		loc := table.Loc
		switch {
		case loc != nil && loc.Sheet != "" && hasFormulaData(formulas, loc.Sheet):
			name = loc.Sheet
		case (loc == nil || loc.Sheet == "") && loc != nil && loc.Page > 0 && loc.Page-1 < len(ordered):
			// xberg exposes the 1-based sheet index in Loc.Page.
			candidate := ordered[loc.Page-1]
			if _, ok := formulas[candidate]; ok {
				name = candidate
			}
		}
		resolved[ti] = name
		if name != "" {
			perSheet[name]++
		}
	}

	ambiguous := false
	for ti := range doc.Tables {
		if err := ctx.Err(); err != nil {
			return applied, warnings, err
		}
		table := &doc.Tables[ti]
		name := resolved[ti]
		if name == "" {
			continue
		}
		sheetData := formulas[name]
		// Engine table cells are table-local (0,0 = first cell of the table), while
		// OOXML references are worksheet-absolute. Without a per-table origin the
		// mapping is only the identity when the sheet starts at A1 *and* holds a
		// single table; otherwise guessing would attach another table's formula.
		// A sub-block of the sheet could also match a shifted offset when labels
		// repeat. Requiring the table to span the sheet's entire used range removes
		// that ambiguity: only then is table-local (0,0) provably the sheet origin.
		spansSheet := table.Rows > 0 && table.Cols > 0 &&
			table.Rows == sheetData.maxRow+1 && table.Cols == sheetData.maxCol+1
		if !spansSheet {
			ambiguous = true
			originUnknown = true
			continue
		}
		if sheetData.originRow != 0 || sheetData.originCol != 0 || perSheet[name] > 1 {
			originUnknown = true
			if perSheet[name] > 1 {
				ambiguous = true
			}
			continue
		}
		// The sheet's used range starts at A1, but the engine's table may still
		// start elsewhere (a title row at A1, table at A2). Verify the identity
		// mapping against the sheet's own cell text; no evidence -> no enrichment.
		if sheetData.valuesCapped || !verifyTableOrigin(table, sheetData.values, sheetData.cells) {
			originUnknown = true
			unverified = true
			continue
		}
		cells := sheetData.cells
		for ci := range table.Cells {
			if ci%512 == 0 {
				if err := ctx.Err(); err != nil {
					return applied, warnings, err
				}
			}
			cell := &table.Cells[ci]
			fc, ok := cells[[2]int{cell.Row, cell.Col}]
			if !ok {
				continue
			}
			if fc.formula != "" {
				cell.Formula = "=" + fc.formula
				applied++
			} else if fc.shared {
				sharedSkipped = true
			}
			if fc.cached != "" && cell.CachedValue == "" {
				cell.CachedValue = fc.cached
			}
			// Type precedence: an explicit OOXML marker beats the adapters'
			// text-based guess; a plain numeric cached value only fills a gap,
			// because the engine may already have resolved a date serial.
			if fc.authoritative && fc.dataType != "" {
				cell.DataType = fc.dataType
			} else if cell.DataType == "" && fc.dataType != "" {
				cell.DataType = fc.dataType
			}
		}
	}
	if sharedSkipped {
		warnings = append(warnings, documentir.Warning{Code: documentir.WarnFormulaMissing,
			Message:  "shared formulas are not expanded; only their cached values are available for non-master cells",
			Severity: "info"})
	}
	if originUnknown {
		msg := "formula text unavailable: the sheet's used range does not start at A1, so table-local cells cannot be mapped to worksheet addresses without an origin"
		if ambiguous {
			msg = "formula text unavailable: the sheet contains several tables and no per-table origin, so table-local cells cannot be mapped to worksheet addresses"
		}
		if unverified {
			msg = "formula text unavailable: the table's origin could not be verified against the worksheet content, so table-local cells were not mapped to worksheet addresses"
		}
		warnings = append(warnings, documentir.Warning{Code: documentir.WarnFormulaMissing,
			Message: msg, Severity: "info"})
	}
	return applied, warnings, nil
}

// sheetHasFormulaTag reports whether the worksheet part contains a formula element.
//
// It is a byte scan of the decompressed stream, not an XML parse: text content escapes "<"
// as "&lt;", so a raw "<f" can only start a tag. This lets a large sheet with no formulas —
// the common case — cost a memchr-style pass instead of a full tokenise.
func sheetHasFormulaTag(ctx context.Context, zr *zip.ReadCloser, name string) (bool, error) {
	rc, err := zr.Open(name)
	if err != nil {
		return false, err
	}
	defer rc.Close()
	return containsFormulaTag(ctx, rc)
}

// containsFormulaTag is the chunk scan used by sheetHasFormulaTag, split out so the boundary
// handling is testable without a zip archive.
func containsFormulaTag(ctx context.Context, r io.Reader) (bool, error) {
	buf := make([]byte, 1<<20)
	prevF := false // last byte of the previous chunk was '<' or ':'
	// OOXML in the wild prefixes elements (<x:f>), and the decoder matches on Name.Local,
	// so both spellings must be detected.
	needles := [][]byte{[]byte("<f"), []byte(":f")}
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, rerr := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			// "<f" and ":f" are matched inside a chunk; a tag split across the boundary
			// leaves the prefix as the last byte, so remember both possibilities.
			if prevF && len(chunk) > 0 && chunk[0] == 'f' {
				return true, nil
			}
			for _, needle := range needles {
				if bytes.Contains(chunk, needle) {
					return true, nil
				}
			}
			prevF = chunk[len(chunk)-1] == '<'
		}
		if rerr == io.EOF {
			return false, nil
		}
		if rerr != nil {
			return false, rerr
		}
	}
}

// scanSheet streams one worksheet part.
//
// It replaces a full xml.Unmarshal of the sheet into structs: the decoder below walks the
// token stream, keeps only the cells that matter, and never materialises the whole document.
// keepValues additionally records every cell's text at its worksheet address, which origin
// verification needs (and which is the expensive part, so the caller asks for it only when
// the sheet actually contains formulas).
func scanSheet(ctx context.Context, zr *zip.ReadCloser, name string,
	shared []string) (*sheetFormulas, error) {
	rc, err := zr.Open(name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	out := &sheetFormulas{
		originRow: -1, originCol: -1, maxRow: -1, maxCol: -1,
		cells:  map[[2]int]formulaCell{},
		values: map[[2]int]string{},
	}

	dec := xml.NewDecoder(&ctxReader{ctx: ctx, r: bufio.NewReaderSize(rc, 256<<10)})
	var (
		inCell                                bool
		ref, typ, value, fText, fType, inline string
	)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				inCell = true
				ref, typ, value, fText, fType, inline = "", "", "", "", "", ""
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "r":
						ref = a.Value
					case "t":
						typ = a.Value
					}
				}
			case "v":
				if inCell {
					var v string
					if err := dec.DecodeElement(&v, &t); err != nil {
						return nil, err
					}
					value = v
				}
			case "f":
				if inCell {
					for _, a := range t.Attr {
						if a.Name.Local == "t" {
							fType = a.Value
						}
					}
					var f string
					if err := dec.DecodeElement(&f, &t); err != nil {
						return nil, err
					}
					fText = f
				}
			case "is":
				if inCell {
					var is struct {
						T string `xml:"t"`
						R []struct {
							T string `xml:"t"`
						} `xml:"r"`
					}
					if err := dec.DecodeElement(&is, &t); err != nil {
						return nil, err
					}
					inline = is.T
					for _, r := range is.R {
						inline += r.T
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local != "c" || !inCell {
				continue
			}
			inCell = false
			col, row, ok := parseA1(ref)
			if !ok {
				continue
			}
			if out.originRow < 0 || row < out.originRow {
				out.originRow = row
			}
			if out.originCol < 0 || col < out.originCol {
				out.originCol = col
			}
			if row > out.maxRow {
				out.maxRow = row
			}
			if col > out.maxCol {
				out.maxCol = col
			}
			cellValue := cachedText(typ, value, shared)
			if cellValue == "" && typ == "inlineStr" {
				cellValue = inline
			}
			if !out.valuesCapped {
				if cellValue != "" {
					out.values[[2]int{row, col}] = cellValue
				}
				if len(out.values) > 200000 {
					out.valuesCapped = true
					out.values = nil
				}
			}
			hasFormula := strings.TrimSpace(fText) != "" || fType == "shared"
			if !hasFormula {
				// Ordinary cells are already populated by the engine; touching them would
				// replace real text with a shared-string index or overwrite a date/boolean.
				continue
			}
			fc := formulaCell{cached: cellValue}
			switch typ {
			case "s", "str", "inlineStr":
				fc.dataType, fc.authoritative = "string", true
			case "b":
				fc.dataType, fc.authoritative = "bool", true
			case "d":
				fc.dataType, fc.authoritative = "date", true
			default:
				fc.dataType = "number"
			}
			if text := strings.TrimSpace(fText); text != "" {
				fc.formula = text
			} else {
				// A shared-formula slave carries no text of its own; copying the master
				// verbatim would be wrong (relative references), so only the cached value is
				// used and the gap is reported once.
				fc.shared = true
			}
			if fc.formula != "" || fc.cached != "" || fc.shared {
				// Bound what a hostile sheet can make us retain: past this many formula
				// cells the sheet is not enrichable anyway (verification needs the values,
				// which are capped separately), and the subprocess RSS ceiling does not
				// cover this in-process work.
				if len(out.cells) >= maxFormulaCells {
					out.valuesCapped = true
					out.cells = map[[2]int]formulaCell{}
					return out, nil
				}
				out.cells[[2]int{row, col}] = fc
			}
		}
	}
	return out, nil
}

// cachedText converts a cached OOXML value to text, resolving shared strings.
func cachedText(cellType, raw string, sharedStrings []string) string {
	switch cellType {
	case "s":
		if idx, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && idx >= 0 && idx < len(sharedStrings) {
			return sharedStrings[idx]
		}
		return ""
	case "b":
		return raw // "0"/"1": the caller receives the raw boolean marker as text
	default:
		return raw
	}
}

// verifyTableOrigin proves that the engine's table-local coordinates are the
// worksheet's addresses. The evidence must be unambiguous:
//
//   - only non-formula cells are compared (a formula cell's engine text may be a
//     formatted rendering such as a date or currency);
//   - every comparable cell must match — a majority is not enough, because
//     repeated headers/labels can otherwise mask a shifted table;
//   - a cell the sheet leaves blank while the table carries text counts as a
//     mismatch;
//   - at least two cells must be comparable.
//
// Numbers are compared by value so "48,120" and "48120" agree. Anything short of
// this returns false and the caller skips enrichment with a warning.
func verifyTableOrigin(table *documentir.Table, values map[[2]int]string,
	formulaCells map[[2]int]formulaCell) bool {
	if len(values) == 0 {
		return false
	}
	compared := 0
	for _, c := range table.Cells {
		key := [2]int{c.Row, c.Col}
		if _, isFormula := formulaCells[key]; isFormula {
			continue
		}
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		compared++
		got := strings.TrimSpace(values[key])
		if got == "" || !sameCellValue(text, got) {
			return false
		}
	}
	return compared >= 2
}

// sameCellValue compares cell text, treating numeric renderings as equal when
// their numeric value is equal after removing thousands separators and spaces.
func sameCellValue(a, b string) bool {
	if a == b {
		return true
	}
	norm := func(v string) string {
		return strings.NewReplacer(",", "", " ", "", "\u00a0", "").Replace(v)
	}
	na, nb := norm(a), norm(b)
	if na == nb {
		return true
	}
	fa, ea := strconv.ParseFloat(na, 64)
	fb, eb := strconv.ParseFloat(nb, 64)
	return ea == nil && eb == nil && fa == fb
}

// hasFormulaData reports whether a sheet has any formula cells. A named sheet
// without formulas must not be resolved: otherwise it would produce a spurious
// "formula text unavailable" warning.
func hasFormulaData(formulas map[string]sheetFormulas, name string) bool {
	_, ok := formulas[name]
	return ok
}

// ShouldEnrichFormulas reports whether formula extraction applies to a format.
func ShouldEnrichFormulas(format string) bool {
	return format == "xlsx" || format == "xlsm"
}

// parseA1 converts a cell reference (e.g. "AB12") to 0-based column and row.
func parseA1(ref string) (col, row int, ok bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, 0, false
	}
	letters := 0
	for letters < len(ref) && ref[letters] >= 'A' && ref[letters] <= 'Z' {
		col = col*26 + int(ref[letters]-'A'+1)
		letters++
	}
	if letters == 0 || letters == len(ref) {
		return 0, 0, false
	}
	r, err := strconv.Atoi(ref[letters:])
	if err != nil || r < 1 {
		return 0, 0, false
	}
	return col - 1, r - 1, true
}

// ctxReader aborts a zip-part read as soon as the parse context is done, so a
// timed-out job cannot keep a worker busy decompressing a large worksheet.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

func readZipXML[T any](ctx context.Context, zr *zip.ReadCloser, name string) (T, error) {
	var out T
	if err := ctx.Err(); err != nil {
		return out, err
	}
	f, err := zr.Open(name)
	if err != nil {
		return out, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(&ctxReader{ctx: ctx, r: f}, 128<<20))
	if err != nil {
		return out, err
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// Decode through a ctx-aware reader: unmarshalling a large worksheet is not
	// interruptible once started, but decoding token by token is.
	dec := xml.NewDecoder(&ctxReader{ctx: ctx, r: bytes.NewReader(b)})
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("parse %s: %w", name, err)
	}
	return out, nil
}
