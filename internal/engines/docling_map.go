package engines

import (
	"encoding/json"
	"fmt"
	"strings"

	"parsedock.dev/parsedock/internal/documentir"
)

// doclingDoc mirrors the DoclingDocument JSON emitted by docling-rs and
// docling-serve. Only the fields ParseDock consumes are decoded.
type doclingDoc struct {
	SchemaName string                 `json:"schema_name"`
	Version    string                 `json:"version"`
	Name       string                 `json:"name"`
	Pages      map[string]doclingPage `json:"pages"`
	Body       *doclingNode           `json:"body"`
	Texts      []doclingText          `json:"texts"`
	Tables     []doclingTable         `json:"tables"`
	Pictures   []doclingPicture       `json:"pictures"`
	Groups     []doclingGroup         `json:"groups"`
}

type doclingPage struct {
	PageNo int `json:"page_no"`
	Size   struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"size"`
}

type doclingRef struct {
	Ref string `json:"$ref"`
}

type doclingNode struct {
	SelfRef  string       `json:"self_ref"`
	Children []doclingRef `json:"children"`
	Label    string       `json:"label"`
	Name     string       `json:"name"`
}

type doclingProv struct {
	PageNo int `json:"page_no"`
	BBox   *struct {
		L           float64 `json:"l"`
		T           float64 `json:"t"`
		R           float64 `json:"r"`
		B           float64 `json:"b"`
		CoordOrigin string  `json:"coord_origin"`
	} `json:"bbox"`
}

type doclingText struct {
	SelfRef      string        `json:"self_ref"`
	Label        string        `json:"label"`
	Text         string        `json:"text"`
	Orig         string        `json:"orig"`
	Level        int           `json:"level"`
	Prov         []doclingProv `json:"prov"`
	ContentLayer string        `json:"content_layer"`
	Children     []doclingRef  `json:"children"`
}

type doclingTable struct {
	SelfRef  string        `json:"self_ref"`
	Label    string        `json:"label"`
	Prov     []doclingProv `json:"prov"`
	Children []doclingRef  `json:"children"`
	Data     struct {
		NumRows    int                `json:"num_rows"`
		NumCols    int                `json:"num_cols"`
		TableCells []doclingTableCell `json:"table_cells"`
	} `json:"data"`
}

type doclingTableCell struct {
	RowSpan    int    `json:"row_span"`
	ColSpan    int    `json:"col_span"`
	StartRow   int    `json:"start_row_offset_idx"`
	EndRow     int    `json:"end_row_offset_idx"`
	StartCol   int    `json:"start_col_offset_idx"`
	EndCol     int    `json:"end_col_offset_idx"`
	Text       string `json:"text"`
	ColumnHdr  bool   `json:"column_header"`
	RowHdr     bool   `json:"row_header"`
	RowSection bool   `json:"row_section"`
	BBox       *struct {
		L float64 `json:"l"`
		T float64 `json:"t"`
		R float64 `json:"r"`
		B float64 `json:"b"`
	} `json:"bbox"`
}

type doclingPicture struct {
	SelfRef string        `json:"self_ref"`
	Label   string        `json:"label"`
	Prov    []doclingProv `json:"prov"`
	Caption []doclingRef  `json:"captions"`
}

type doclingGroup struct {
	SelfRef  string       `json:"self_ref"`
	Label    string       `json:"label"`
	Name     string       `json:"name"`
	Children []doclingRef `json:"children"`
}

// mapDocling converts a DoclingDocument into ParseDock's IR, preserving reading
// order, page numbers, bounding boxes (with their coordinate origin) and table
// spans. Fields the engine does not provide stay empty rather than invented.
//
// pageKind is "pdf", "sheet", "slide" or "none" and controls location kind.
func mapDocling(doc *documentir.Document, raw []byte, pageKind string) error {
	var d doclingDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("decode docling document: %w", err)
	}
	doc.PageKind = pageKind
	if len(d.Pages) > 0 {
		doc.PageCount = len(d.Pages)
	}

	textIdx := map[string]*doclingText{}
	for i := range d.Texts {
		textIdx[d.Texts[i].SelfRef] = &d.Texts[i]
	}
	tableIdx := map[string]*doclingTable{}
	for i := range d.Tables {
		tableIdx[d.Tables[i].SelfRef] = &d.Tables[i]
	}
	picIdx := map[string]*doclingPicture{}
	for i := range d.Pictures {
		picIdx[d.Pictures[i].SelfRef] = &d.Pictures[i]
	}
	groupIdx := map[string]*doclingGroup{}
	for i := range d.Groups {
		groupIdx[d.Groups[i].SelfRef] = &d.Groups[i]
	}

	// Page box sizes, used to make bboxes interpretable (F08). Keyed by page_no.
	pageSizes := map[int]doclingPage{}
	for _, p := range d.Pages {
		pageSizes[p.PageNo] = p
	}

	sheet := ""
	visited := map[string]bool{}

	var walk func(refs []doclingRef, depth int)
	walk = func(refs []doclingRef, depth int) {
		for _, r := range refs {
			if visited[r.Ref] {
				continue
			}
			visited[r.Ref] = true
			switch {
			case strings.HasPrefix(r.Ref, "#/texts/"):
				if t, ok := textIdx[r.Ref]; ok {
					doc.Elements = append(doc.Elements, textToElement(doc, t, pageKind, sheet, pageSizes))
					for _, c := range t.Children {
						if strings.HasPrefix(c.Ref, "#/texts/") {
							walk([]doclingRef{c}, depth+1)
						}
					}
				}
			case strings.HasPrefix(r.Ref, "#/tables/"):
				if t, ok := tableIdx[r.Ref]; ok {
					addDoclingTable(doc, t, pageKind, sheet, pageSizes)
				}
			case strings.HasPrefix(r.Ref, "#/pictures/"):
				if p, ok := picIdx[r.Ref]; ok {
					el := documentir.Element{
						ID: fmt.Sprintf("el_%d", len(doc.Elements)+1), Type: documentir.ElImage,
						Loc: provLocation(p.Prov, pageKind, sheet, pageSizes),
					}
					for _, c := range p.Caption {
						if t, ok := textIdx[c.Ref]; ok {
							el.Text = t.Text
						}
					}
					doc.Elements = append(doc.Elements, el)
				}
			case strings.HasPrefix(r.Ref, "#/groups/"):
				if g, ok := groupIdx[r.Ref]; ok {
					prev := sheet
					if g.Label == "sheet" && g.Name != "" {
						sheet = g.Name
					}
					walk(g.Children, depth+1)
					sheet = prev
				}
			default:
				// Unknown ref family: recorded once, never silently invented.
				doc.AddWarning(documentir.WarnUnsupported, "unsupported docling reference "+r.Ref, "info")
			}
		}
	}

	// Reading order comes from one of two places. With provenance (PDF and image
	// inputs) the body tree is the reading order and the texts array may be
	// column-interleaved, so the tree wins. Without provenance (Office inputs)
	// docling lists the paragraphs in d.Texts order but the body tree only
	// references a handful of inline runs; walking the tree there puts a caption
	// dozens of elements away from the table it introduces.
	if hasProvenance(&d) && d.Body != nil {
		// Reading order is the body tree; the texts array may be column-interleaved.
		walk(d.Body.Children, 0)
	}
	// Without provenance the tree is not a reading order at all: it references a
	// handful of inline runs and would drop everything it does not mention. The
	// texts array is iterated below and owns the order for those inputs.
	// DOCX (and other Office inputs) arrive with most content outside the body
	// tree: the body references one text node and a couple of groups, while the
	// paragraphs live in d.Texts in document order. Tables carry no provenance, so
	// the only reading-order anchor is the set of text nodes their own header
	// cells live in - those are contiguous and sit exactly where the table was.
	anchors, owned := tableAnchors(&d, groupIdx)
	// Anything not reachable from the body would be lost; append it in document
	// array order so content is never silently dropped.
	for i := range d.Texts {
		// Anchor first: the text node a table starts at may itself have been
		// reached from the body, and the table must still be inserted at that
		// position rather than after everything else.
		for _, tbl := range anchors[i] {
			if !visited[tbl.SelfRef] {
				visited[tbl.SelfRef] = true
				addDoclingTable(doc, tbl, pageKind, "", pageSizes)
			}
		}
		t := &d.Texts[i]
		if visited[t.SelfRef] || t.Text == "" {
			continue
		}
		// A table's own header cells are rendered by the table element itself;
		// keeping them as paragraphs would print the header twice.
		if owned[i] {
			continue
		}
		doc.Elements = append(doc.Elements, textToElement(doc, t, pageKind, "", pageSizes))
	}
	for i := range d.Tables {
		if !visited[d.Tables[i].SelfRef] {
			addDoclingTable(doc, &d.Tables[i], pageKind, "", pageSizes)
		}
	}
	return nil
}

// hasProvenance reports whether any text carries a source location. Office
// converters emit none, which is what tells the two ordering strategies apart.
func hasProvenance(d *doclingDoc) bool {
	for i := range d.Texts {
		if len(d.Texts[i].Prov) > 0 {
			return true
		}
	}
	for i := range d.Tables {
		if len(d.Tables[i].Prov) > 0 {
			return true
		}
	}
	return false
}

// tableAnchors maps "index of the first text node that belongs to this table" to
// the tables that start there, and reports which text indices belong to a table
// at all. Only tables the body never references are anchored; a table the body
// does reference keeps the engine's own reading order.
//
// A table with no reachable text node (no anchor) is not anchored here and keeps
// the fallback of being appended, which is what happened to every table before.
func tableAnchors(d *doclingDoc, groupIdx map[string]*doclingGroup) (map[int][]*doclingTable, map[int]bool) {
	textIndex := map[string]int{}
	for i := range d.Texts {
		textIndex[d.Texts[i].SelfRef] = i
	}
	anchors := map[int][]*doclingTable{}
	owned := map[int]bool{}
	for i := range d.Tables {
		t := &d.Tables[i]
		var refs []int
		seenGroups := map[string]bool{}
		stack := append([]doclingRef{}, t.Children...)
		for len(stack) > 0 {
			r := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if idx, ok := textIndex[r.Ref]; ok {
				refs = append(refs, idx)
				continue
			}
			if g, ok := groupIdx[r.Ref]; ok && !seenGroups[r.Ref] {
				seenGroups[r.Ref] = true
				stack = append(stack, g.Children...)
			}
		}
		if len(refs) == 0 {
			continue
		}
		first := refs[0]
		for _, r := range refs[1:] {
			if r < first {
				first = r
			}
		}
		anchors[first] = append(anchors[first], t)
		for _, r := range refs {
			owned[r] = true
		}
	}
	return anchors, owned
}

func textToElement(doc *documentir.Document, t *doclingText, pageKind, sheet string,
	pageSizes map[int]doclingPage) documentir.Element {
	el := documentir.Element{
		ID:   fmt.Sprintf("el_%d", len(doc.Elements)+1),
		Text: t.Text,
		Loc:  provLocation(t.Prov, pageKind, sheet, pageSizes),
	}
	if el.Text == "" {
		el.Text = t.Orig
	}
	switch t.Label {
	case "title":
		el.Type = documentir.ElTitle
		el.Level = 1
	case "section_header", "subtitle":
		el.Type = documentir.ElSection
		el.Level = t.Level
		if el.Level == 0 {
			el.Level = 2
		}
	case "list_item":
		el.Type = documentir.ElListItem
		el.Level = maxInt(t.Level, 1)
	case "caption":
		el.Type = documentir.ElCaption
	case "footnote":
		el.Type = documentir.ElFootnote
	case "page_header":
		el.Type = documentir.ElHeader
	case "page_footer":
		el.Type = documentir.ElFooter
	case "code":
		el.Type = documentir.ElCode
	case "formula":
		el.Type = documentir.ElFormula
	case "checkbox_selected":
		el.Type = documentir.ElListItem
		el.Text = "[x] " + el.Text
	case "checkbox_unselected":
		el.Type = documentir.ElListItem
		el.Text = "[ ] " + el.Text
	case "reference":
		el.Type = documentir.ElFootnote
	default:
		el.Type = documentir.ElParagraph
	}
	if t.ContentLayer == "furniture" && el.Type == documentir.ElParagraph {
		el.Metadata = map[string]string{"content_layer": "furniture"}
	}
	return el
}

func addDoclingTable(doc *documentir.Document, t *doclingTable, pageKind, sheet string,
	pageSizes map[int]doclingPage) {
	rows := t.Data.NumRows
	cols := t.Data.NumCols
	if rows == 0 || cols == 0 {
		for _, c := range t.Data.TableCells {
			if c.EndRow > rows {
				rows = c.EndRow
			}
			if c.EndCol > cols {
				cols = c.EndCol
			}
		}
	}
	headerRows := 0
	for _, c := range t.Data.TableCells {
		if c.ColumnHdr && c.EndRow > headerRows {
			headerRows = c.EndRow
		}
	}
	tbl := documentir.Table{
		ID:         fmt.Sprintf("tbl_%d", len(doc.Tables)+1),
		Rows:       rows,
		Cols:       cols,
		HeaderRows: headerRows,
		Loc:        provLocation(t.Prov, pageKind, sheet, pageSizes),
	}
	for _, c := range t.Data.TableCells {
		rowSpan := c.RowSpan
		if rowSpan == 0 {
			rowSpan = c.EndRow - c.StartRow
		}
		colSpan := c.ColSpan
		if colSpan == 0 {
			colSpan = c.EndCol - c.StartCol
		}
		if rowSpan < 1 {
			rowSpan = 1
		}
		if colSpan < 1 {
			colSpan = 1
		}
		tbl.Cells = append(tbl.Cells, documentir.Cell{
			Row: c.StartRow, Col: c.StartCol, RowSpan: rowSpan, ColSpan: colSpan,
			Text: c.Text, DataType: guessType(c.Text),
		})
	}
	doc.Tables = append(doc.Tables, tbl)
	doc.Elements = append(doc.Elements, documentir.Element{
		ID: fmt.Sprintf("el_%d", len(doc.Elements)+1), Type: documentir.ElTable, TableID: tbl.ID,
		Text: fmt.Sprintf("%d rows x %d cols", tbl.Rows, tbl.Cols), Loc: tbl.Loc,
	})
}

func provLocation(provs []doclingProv, pageKind, sheet string,
	pageSizes map[int]doclingPage) *documentir.SourceLocation {
	if len(provs) == 0 {
		if sheet != "" {
			return &documentir.SourceLocation{Kind: "sheet", Sheet: sheet}
		}
		return nil
	}
	p := provs[0]
	loc := &documentir.SourceLocation{Kind: pageKind, Page: p.PageNo}
	if pageKind == "sheet" && sheet != "" {
		loc.Sheet = sheet
	}
	// Page dimensions are PDF points (1/72"). For spreadsheets Docling reports
	// the sheet grid extent, not points, so those fields stay empty there.
	if pageKind == "pdf" || pageKind == "slide" {
		if size, ok := pageSizes[p.PageNo]; ok && size.Size.Width > 0 && size.Size.Height > 0 {
			loc.PageWidthPt = size.Size.Width
			loc.PageHeightPt = size.Size.Height
		}
	}
	if p.BBox != nil {
		coord := "pdf_top_left_v1"
		if strings.EqualFold(p.BBox.CoordOrigin, "BOTTOMLEFT") {
			coord = "pdf_user_space_v1"
		}
		loc.BBox = &documentir.BBox{Coord: coord, X0: p.BBox.L, Y0: p.BBox.T, X1: p.BBox.R, Y1: p.BBox.B}
	}
	if loc.Kind == "" {
		loc.Kind = "none"
	}
	return loc
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
