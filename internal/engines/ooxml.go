package engines

import (
	"archive/zip"
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
type sheetFormulas struct {
	originRow, originCol int
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
		return 0, nil, nil // not an xlsx workbook: nothing to add
	}
	rels, _ := readZipXML[xlsxRels](ctx, zr, "xl/_rels/workbook.xml.rels")
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
	if sst, err := readZipXML[xlsxSharedStrings](ctx, zr, "xl/sharedStrings.xml"); err == nil {
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
	var warnings []documentir.Warning
	for _, sh := range sheets {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		ws, err := readZipXML[xlsxWorksheet](ctx, zr, sh.part)
		if err != nil {
			continue
		}
		cells := map[[2]int]formulaCell{}
		values := map[[2]int]string{}
		capped := false
		originRow, originCol := -1, -1
		for _, row := range ws.SheetData.Rows {
			for _, c := range row.Cells {
				col, r, ok := parseA1(c.Ref)
				if !ok {
					continue
				}
				if originRow < 0 || r < originRow {
					originRow = r
				}
				if originCol < 0 || col < originCol {
					originCol = col
				}
				// Record every cell's text first: origin verification compares the
				// engine's table against the sheet, so ordinary cells matter.
				if !capped {
					val := cachedText(c.T, c.V, sharedStrings)
					if val == "" && c.T == "inlineStr" {
						val = c.Is.T
						for _, r := range c.Is.R {
							val += r.T
						}
					}
					if val != "" {
						values[[2]int{r, col}] = val
					}
					if len(values) > 200000 {
						capped = true
						values = map[[2]int]string{}
					}
				}
				hasFormula := strings.TrimSpace(c.F.Text) != "" || c.F.Type == "shared"
				if !hasFormula {
					// Ordinary cells are already populated by the engine; touching
					// them would replace real text with a shared-string index or
					// overwrite a date/boolean type.
					continue
				}
				fc := formulaCell{cached: cachedText(c.T, c.V, sharedStrings)}
				switch c.T {
				case "s", "str", "inlineStr":
					fc.dataType, fc.authoritative = "string", true
				case "b":
					fc.dataType, fc.authoritative = "bool", true
				case "d":
					fc.dataType, fc.authoritative = "date", true
				default:
					fc.dataType = "number"
				}
				if text := strings.TrimSpace(c.F.Text); text != "" {
					fc.formula = text
				} else {
					// A shared-formula slave carries no text of its own. Copying the
					// master verbatim would be wrong (relative references), so only
					// the cached value is used and the gap is reported once.
					fc.shared = true
				}
				// Keep shared-formula slaves even when they have no cache: the warning
				// that the formula text is unavailable depends on seeing them.
				if fc.formula != "" || fc.cached != "" || fc.shared {
					cells[[2]int{r, col}] = fc
				}

			}
		}
		if len(cells) > 0 && originRow >= 0 {
			formulas[sh.name] = sheetFormulas{originRow: originRow, originCol: originCol,
				cells: cells, values: values, valuesCapped: capped}
		}
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
		case loc != nil && loc.Sheet != "" && hasSheet(formulas, ordered, loc.Sheet):
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

// verifyTableOrigin reports whether the table's local coordinates line up with the
// worksheet's addresses. Evidence comes from non-formula cells only: a formula
// cell's engine text may be a formatted rendering (a date, a currency) that does
// not equal the raw cached value. At least one comparable cell must agree and at
// least half of them must agree; otherwise the table is treated as unmappable
// rather than guessed.
func verifyTableOrigin(table *documentir.Table, values map[[2]int]string,
	formulaCells map[[2]int]formulaCell) bool {
	if len(values) == 0 {
		return false
	}
	compared, matched := 0, 0
	for _, c := range table.Cells {
		key := [2]int{c.Row, c.Col}
		if _, isFormula := formulaCells[key]; isFormula {
			continue
		}
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		got := strings.TrimSpace(values[key])
		if got == "" {
			continue
		}
		compared++
		if got == text {
			matched++
		}
		if compared >= 50 {
			break
		}
	}
	return matched >= 1 && matched*2 >= compared
}

func hasSheet(formulas map[string]sheetFormulas, ordered []string, name string) bool {
	if _, ok := formulas[name]; ok {
		return true
	}
	for _, n := range ordered {
		if n == name {
			return true
		}
	}
	return false
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
