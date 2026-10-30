package engines

import (
	"archive/zip"
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
				V string `xml:"v"`
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
func EnrichXLSXFormulas(inputPath string, doc *documentir.Document) (int, []documentir.Warning) {
	if doc == nil {
		return 0, nil
	}
	zr, err := zip.OpenReader(inputPath)
	if err != nil {
		return 0, []documentir.Warning{{Code: documentir.WarnUnsupported,
			Message: "formula enrichment skipped: not a readable OOXML package", Severity: "info"}}
	}
	defer zr.Close()

	workbook, err := readZipXML[xlsxWorkbook](zr, "xl/workbook.xml")
	if err != nil {
		return 0, nil // not an xlsx workbook: nothing to add
	}
	rels, _ := readZipXML[xlsxRels](zr, "xl/_rels/workbook.xml.rels")
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
	if sst, err := readZipXML[xlsxSharedStrings](zr, "xl/sharedStrings.xml"); err == nil {
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

	formulas := map[string]map[[2]int]formulaCell{}
	var warnings []documentir.Warning
	for _, sh := range sheets {
		ws, err := readZipXML[xlsxWorksheet](zr, sh.part)
		if err != nil {
			continue
		}
		cells := map[[2]int]formulaCell{}
		for _, row := range ws.SheetData.Rows {
			for _, c := range row.Cells {
				col, r, ok := parseA1(c.Ref)
				if !ok {
					continue
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
		if len(cells) > 0 {
			formulas[sh.name] = cells
		}
	}
	if len(formulas) == 0 {
		return 0, nil
	}

	applied := 0
	sharedSkipped := false
	ordered := make([]string, 0, len(sheets))
	for _, sh := range sheets {
		ordered = append(ordered, sh.name)
	}
	for ti := range doc.Tables {
		table := &doc.Tables[ti]
		sheet := ""
		if table.Loc != nil && table.Loc.Sheet != "" {
			sheet = table.Loc.Sheet
		}
		cells, ok := formulas[sheet]
		if !ok {
			// Positional fallback exists only for engines that expose the sheet
			// *index* and no name (xberg). A table that states its sheet must never
			// inherit another sheet's formulas just because its own sheet has none.
			if sheet == "" && ti < len(ordered) && len(doc.Tables) == len(ordered) {
				cells, ok = formulas[ordered[ti]]
			}
		}
		if !ok {
			continue
		}
		for ci := range table.Cells {
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
	return applied, warnings
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

func readZipXML[T any](zr *zip.ReadCloser, name string) (T, error) {
	var out T
	f, err := zr.Open(name)
	if err != nil {
		return out, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 128<<20))
	if err != nil {
		return out, err
	}
	if err := xml.Unmarshal(b, &out); err != nil {
		return out, fmt.Errorf("parse %s: %w", name, err)
	}
	return out, nil
}
