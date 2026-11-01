package engines

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
	Furniture  *doclingNode           `json:"furniture"`
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
	// Run formatting, hyperlink, list numbering and code language, when the converter
	// reports them (docling-rs does for DOCX; PDF text carries none of them).
	Formatting   *doclingFormatting `json:"formatting"`
	Hyperlink    string             `json:"hyperlink"`
	Enumerated   bool               `json:"enumerated"`
	Marker       string             `json:"marker"`
	CodeLanguage string             `json:"code_language"`
}

type doclingFormatting struct {
	Bold          bool   `json:"bold"`
	Italic        bool   `json:"italic"`
	Underline     bool   `json:"underline"`
	Strikethrough bool   `json:"strikethrough"`
	Script        string `json:"script"`
}

type doclingTable struct {
	SelfRef  string        `json:"self_ref"`
	Label    string        `json:"label"`
	Parent   doclingRef    `json:"parent"`
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
	SelfRef  string        `json:"self_ref"`
	Label    string        `json:"label"`
	Prov     []doclingProv `json:"prov"`
	Caption  []doclingRef  `json:"captions"`
	Children []doclingRef  `json:"children"`
}

type doclingGroup struct {
	SelfRef      string       `json:"self_ref"`
	Label        string       `json:"label"`
	Name         string       `json:"name"`
	ContentLayer string       `json:"content_layer"`
	Children     []doclingRef `json:"children"`
}

// mapDocling converts a DoclingDocument into ParseDock's IR, preserving reading
// order, page numbers, bounding boxes (with their coordinate origin) and table
// spans. Fields the engine does not provide stay empty rather than invented.
//
// pageKind is "pdf", "sheet", "slide" or "none" and controls location kind.
//
// Reading order is the body tree, walked depth-first through every kind of child
// (texts, tables, pictures, groups - a section header's table is its child, not its
// sibling). Anything the tree does not reach is never dropped: it is inserted where the
// texts array puts it relative to the reached items, and a table no text references
// either sits at its own header cells (tableAnchors) or after its parent.
func mapDocling(doc *documentir.Document, raw []byte, pageKind string) error {
	var d doclingDoc
	if err := json.Unmarshal(raw, &d); err != nil {
		return fmt.Errorf("decode docling document: %w", err)
	}
	doc.PageKind = pageKind
	if len(d.Pages) > 0 {
		doc.PageCount = len(d.Pages)
	}
	m := newDoclingMapper(&d, pageKind)
	if d.Body != nil {
		m.walk(d.Body.Children, doclingCtx{})
	}
	if d.Furniture != nil {
		m.walk(d.Furniture.Children, doclingCtx{furniture: true})
	}
	m.placeUnreached()
	m.emit(doc)
	return nil
}

// doclingCtx is what a node inherits from its ancestors.
type doclingCtx struct {
	sheet     string
	listDepth int
	furniture bool
	region    string // documentir.ElHeader / ElFooter when inside a page header/footer group
}

// doclingItem is one entry of the reading order before it becomes an element.
type doclingItem struct {
	ref     string
	ctx     doclingCtx
	textIdx int    // index in d.Texts for text items, -1 otherwise
	heading string // sheet name, for a synthetic sheet heading
}

type doclingMapper struct {
	d         *doclingDoc
	pageKind  string
	textIdx   map[string]int
	tableIdx  map[string]*doclingTable
	picIdx    map[string]*doclingPicture
	groupIdx  map[string]*doclingGroup
	pageSizes map[int]doclingPage
	visited   map[string]bool
	order     []doclingItem
	unknown   []string
}

func newDoclingMapper(d *doclingDoc, pageKind string) *doclingMapper {
	m := &doclingMapper{
		d: d, pageKind: pageKind,
		textIdx:   make(map[string]int, len(d.Texts)),
		tableIdx:  make(map[string]*doclingTable, len(d.Tables)),
		picIdx:    make(map[string]*doclingPicture, len(d.Pictures)),
		groupIdx:  make(map[string]*doclingGroup, len(d.Groups)),
		pageSizes: map[int]doclingPage{},
		visited:   map[string]bool{},
	}
	for i := range d.Texts {
		m.textIdx[d.Texts[i].SelfRef] = i
	}
	for i := range d.Tables {
		m.tableIdx[d.Tables[i].SelfRef] = &d.Tables[i]
	}
	for i := range d.Pictures {
		m.picIdx[d.Pictures[i].SelfRef] = &d.Pictures[i]
	}
	for i := range d.Groups {
		m.groupIdx[d.Groups[i].SelfRef] = &d.Groups[i]
	}
	// Page box sizes, used to make bboxes interpretable (F08). Keyed by page_no.
	for _, p := range d.Pages {
		m.pageSizes[p.PageNo] = p
	}
	return m
}

func (m *doclingMapper) walk(refs []doclingRef, ctx doclingCtx) {
	for _, r := range refs {
		if m.visited[r.Ref] {
			continue
		}
		m.visited[r.Ref] = true
		switch {
		case strings.HasPrefix(r.Ref, "#/texts/"):
			i, ok := m.textIdx[r.Ref]
			if !ok {
				continue
			}
			t := &m.d.Texts[i]
			m.order = append(m.order, doclingItem{ref: r.Ref, ctx: ctx, textIdx: i})
			if strings.TrimSpace(t.Text) == "" {
				// A list item or paragraph whose words are formatted runs carries no text
				// itself: the runs are an inline group below it and belong to this item.
				if g := m.inlineChild(t); g != nil {
					m.visited[g.SelfRef] = true
					m.markChildren(g)
				}
			}
			m.walk(t.Children, ctx)
		case strings.HasPrefix(r.Ref, "#/tables/"):
			if t, ok := m.tableIdx[r.Ref]; ok {
				m.order = append(m.order, doclingItem{ref: r.Ref, ctx: ctx, textIdx: -1})
				// Rich cells are groups of runs under the table; the table renders them.
				m.markChildrenDeep(t.Children)
			}
		case strings.HasPrefix(r.Ref, "#/pictures/"):
			if p, ok := m.picIdx[r.Ref]; ok {
				m.order = append(m.order, doclingItem{ref: r.Ref, ctx: ctx, textIdx: -1})
				for _, c := range p.Caption {
					m.visited[c.Ref] = true
				}
				// Text inside the figure (chart labels, OCR of a scanned figure) belongs
				// right after it. Left unreached, placeUnreached put it before the next
				// *text* in the array, i.e. after any picture that follows, so a figure's
				// words could land behind the next page's figure.
				m.walk(p.Children, ctx)
			}
		case strings.HasPrefix(r.Ref, "#/groups/"):
			g, ok := m.groupIdx[r.Ref]
			if !ok {
				continue
			}
			child := ctx
			if g.ContentLayer == "furniture" {
				child.furniture = true
			}
			name := strings.ToLower(g.Name)
			switch {
			case strings.Contains(name, "header") && child.furniture:
				child.region = documentir.ElHeader
			case strings.Contains(name, "footer") && child.furniture:
				child.region = documentir.ElFooter
			}
			switch g.Label {
			case "sheet":
				if g.Name != "" {
					child.sheet = g.Name
					m.order = append(m.order, doclingItem{ref: r.Ref, ctx: child, textIdx: -1, heading: g.Name})
				}
			case "list", "ordered_list":
				child.listDepth++
			case "inline":
				// One paragraph made of formatted runs.
				m.order = append(m.order, doclingItem{ref: r.Ref, ctx: child, textIdx: m.firstTextIndex(g)})
				m.markChildren(g)
				// Non-text children (a formula or picture inside the run) keep their place.
				m.walk(g.Children, child)
				continue
			}
			m.walk(g.Children, child)
		default:
			m.unknown = append(m.unknown, r.Ref)
		}
	}
}

// inlineChild returns the inline group that holds an empty text node's runs.
func (m *doclingMapper) inlineChild(t *doclingText) *doclingGroup {
	for _, c := range t.Children {
		if g, ok := m.groupIdx[c.Ref]; ok && g.Label == "inline" && !m.visited[c.Ref] {
			return g
		}
	}
	return nil
}

func (m *doclingMapper) markChildren(g *doclingGroup) {
	for _, c := range g.Children {
		if _, ok := m.textIdx[c.Ref]; ok {
			m.visited[c.Ref] = true
		}
	}
}

func (m *doclingMapper) markChildrenDeep(refs []doclingRef) {
	for _, c := range refs {
		if m.visited[c.Ref] {
			continue
		}
		if _, ok := m.textIdx[c.Ref]; ok {
			m.visited[c.Ref] = true
			continue
		}
		if g, ok := m.groupIdx[c.Ref]; ok {
			m.visited[c.Ref] = true
			m.markChildrenDeep(g.Children)
		}
	}
}

func (m *doclingMapper) firstTextIndex(g *doclingGroup) int {
	for _, c := range g.Children {
		if i, ok := m.textIdx[c.Ref]; ok {
			return i
		}
	}
	return -1
}

// placeUnreached inserts what the tree does not reach. Earlier docling versions
// referenced only a few inline runs from the body of an Office document and kept the
// paragraphs in d.Texts; those must not be lost or moved to the end. Each unreached text
// goes before the first reached item that comes later in the texts array (the array is
// document order), a table that only its header-cell texts reference goes where those
// texts are, and a table with neither goes after its parent, or last.
func (m *doclingMapper) placeUnreached() {
	anchors, owned := tableAnchors(m.d, m.groupIdx)
	type insert struct {
		pos  int
		item doclingItem
	}
	var inserts []insert
	// before(i) is the position of the first reached text that comes later in the
	// texts array (non-text items are passed over: their array position is unknown).
	// It is monotonic in i, so one cursor over the reached order is enough.
	cursor := 0
	before := func(idx int) int {
		for cursor < len(m.order) && m.order[cursor].textIdx <= idx {
			cursor++
		}
		return cursor
	}
	for i := range m.d.Texts {
		for _, tbl := range anchors[i] {
			if !m.visited[tbl.SelfRef] {
				m.visited[tbl.SelfRef] = true
				inserts = append(inserts, insert{before(i), doclingItem{ref: tbl.SelfRef, textIdx: -1}})
			}
		}
		t := &m.d.Texts[i]
		if m.visited[t.SelfRef] || t.Text == "" || owned[i] {
			continue
		}
		m.visited[t.SelfRef] = true
		ctx := doclingCtx{furniture: t.ContentLayer == "furniture"}
		inserts = append(inserts, insert{before(i), doclingItem{ref: t.SelfRef, ctx: ctx, textIdx: i}})
	}
	pos := map[string]int{}
	for p, it := range m.order {
		pos[it.ref] = p
	}
	for i := range m.d.Tables {
		t := &m.d.Tables[i]
		if m.visited[t.SelfRef] {
			continue
		}
		m.visited[t.SelfRef] = true
		at := len(m.order)
		if p, ok := pos[t.Parent.Ref]; ok {
			at = p + 1
		}
		inserts = append(inserts, insert{at, doclingItem{ref: t.SelfRef, textIdx: -1}})
	}
	if len(inserts) == 0 {
		return
	}
	sort.SliceStable(inserts, func(a, b int) bool { return inserts[a].pos < inserts[b].pos })
	merged := make([]doclingItem, 0, len(m.order)+len(inserts))
	k := 0
	for p := 0; p <= len(m.order); p++ {
		for k < len(inserts) && inserts[k].pos == p {
			merged = append(merged, inserts[k].item)
			k++
		}
		if p < len(m.order) {
			merged = append(merged, m.order[p])
		}
	}
	m.order = merged
}

func (m *doclingMapper) emit(doc *documentir.Document) {
	for _, it := range m.order {
		switch {
		case it.heading != "":
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: fmt.Sprintf("el_%d", len(doc.Elements)+1), Type: documentir.ElSection, Level: 2,
				Text: it.heading, Loc: &documentir.SourceLocation{Kind: "sheet", Sheet: it.heading},
			})
		case strings.HasPrefix(it.ref, "#/texts/"):
			t := &m.d.Texts[it.textIdx]
			el := textToElement(doc, t, m.pageKind, it.ctx.sheet, m.pageSizes)
			plain, rich := doclingRun(t)
			if strings.TrimSpace(t.Text) == "" {
				if g := m.inlineGroupOf(t); g != nil {
					plain, rich = m.joinRuns(g)
					if el.Loc == nil {
						el.Loc = m.groupLocation(g, it.ctx.sheet)
					}
				}
			}
			switch t.Label {
			case "checkbox_selected":
				plain, rich = "[x] "+plain, "[x] "+rich
			case "checkbox_unselected":
				plain, rich = "[ ] "+plain, "[ ] "+rich
			}
			el.Text = plain
			if rich != plain {
				el.RichText = rich
			}
			m.decorate(&el, t, it.ctx)
			if el.Text == "" && el.Type != documentir.ElTable {
				continue
			}
			doc.Elements = append(doc.Elements, el)
		case strings.HasPrefix(it.ref, "#/groups/"):
			g := m.groupIdx[it.ref]
			plain, rich := m.joinRuns(g)
			if strings.TrimSpace(plain) == "" {
				continue
			}
			el := documentir.Element{
				ID: fmt.Sprintf("el_%d", len(doc.Elements)+1), Type: documentir.ElParagraph,
				Text: plain, Loc: m.groupLocation(g, it.ctx.sheet),
			}
			if rich != plain {
				el.RichText = rich
			}
			if it.ctx.furniture {
				m.decorate(&el, nil, it.ctx)
			}
			doc.Elements = append(doc.Elements, el)
		case strings.HasPrefix(it.ref, "#/tables/"):
			addDoclingTable(doc, m.tableIdx[it.ref], m.pageKind, it.ctx.sheet, m.pageSizes)
		case strings.HasPrefix(it.ref, "#/pictures/"):
			p := m.picIdx[it.ref]
			el := documentir.Element{
				ID: fmt.Sprintf("el_%d", len(doc.Elements)+1), Type: documentir.ElImage,
				Loc: provLocation(p.Prov, m.pageKind, it.ctx.sheet, m.pageSizes),
			}
			for _, c := range p.Caption {
				if i, ok := m.textIdx[c.Ref]; ok {
					el.Text = m.d.Texts[i].Text
				}
			}
			doc.Elements = append(doc.Elements, el)
		}
	}
	for _, ref := range m.unknown {
		// Unknown ref family: recorded once, never silently invented.
		doc.AddWarning(documentir.WarnUnsupported, "unsupported docling reference "+ref, "info")
	}
}

// decorate applies what the tree position says about an element: list depth and
// numbering, code language, and the furniture layer.
func (m *doclingMapper) decorate(el *documentir.Element, t *doclingText, ctx doclingCtx) {
	setMeta := func(k, v string) {
		if el.Metadata == nil {
			el.Metadata = map[string]string{}
		}
		el.Metadata[k] = v
	}
	if t != nil {
		if el.Type == documentir.ElListItem {
			if t.Level == 0 && ctx.listDepth > 0 {
				el.Level = ctx.listDepth
			}
			if t.Enumerated {
				setMeta(documentir.MetaListOrdered, "true")
				if mk := strings.TrimSpace(t.Marker); mk != "" {
					setMeta(documentir.MetaListMarker, mk)
				}
			}
		}
		if el.Type == documentir.ElCode && strings.TrimSpace(t.CodeLanguage) != "" {
			setMeta(documentir.MetaCodeLanguage, strings.TrimSpace(t.CodeLanguage))
		}
	}
	furniture := ctx.furniture || (t != nil && t.ContentLayer == "furniture")
	if !furniture {
		return
	}
	if el.Type == documentir.ElParagraph && ctx.region != "" {
		// A plain text inside docling's "page header"/"page footer" group is exactly
		// that; give it the element type consumers already skip.
		el.Type = ctx.region
	}
	setMeta(documentir.MetaContentLayer, documentir.ContentLayerFurniture)
}

func (m *doclingMapper) inlineGroupOf(t *doclingText) *doclingGroup {
	for _, c := range t.Children {
		if g, ok := m.groupIdx[c.Ref]; ok && g.Label == "inline" {
			return g
		}
	}
	return nil
}

// joinRuns concatenates an inline group's runs into plain and rich text. docling trims
// each run, so the space between runs is restored, except before closing punctuation,
// after opening punctuation and between CJK characters.
func (m *doclingMapper) joinRuns(g *doclingGroup) (string, string) {
	var plain, rich strings.Builder
	prevPlain := ""
	for _, c := range g.Children {
		i, ok := m.textIdx[c.Ref]
		if !ok {
			continue
		}
		p, r := doclingRun(&m.d.Texts[i])
		if p == "" {
			continue
		}
		if prevPlain != "" && needsSpace(prevPlain, p) {
			plain.WriteByte(' ')
			rich.WriteByte(' ')
		}
		plain.WriteString(p)
		rich.WriteString(r)
		prevPlain = p
	}
	return plain.String(), rich.String()
}

func (m *doclingMapper) groupLocation(g *doclingGroup, sheet string) *documentir.SourceLocation {
	for _, c := range g.Children {
		if i, ok := m.textIdx[c.Ref]; ok && len(m.d.Texts[i].Prov) > 0 {
			return provLocation(m.d.Texts[i].Prov, m.pageKind, sheet, m.pageSizes)
		}
	}
	return provLocation(nil, m.pageKind, sheet, m.pageSizes)
}

func needsSpace(prev, next string) bool {
	a, _ := utf8.DecodeLastRuneInString(prev)
	b, _ := utf8.DecodeRuneInString(next)
	if unicode.IsSpace(a) || unicode.IsSpace(b) {
		return false
	}
	if strings.ContainsRune(".,;:!?)]}%»”’、。，；：！？）」』", b) || strings.ContainsRune("([{«“‘（「『", a) {
		return false
	}
	if isCJK(a) && isCJK(b) {
		return false
	}
	return true
}

func isCJK(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) ||
		strings.ContainsRune("，。；：！？、（）「」『』", r)
}

// doclingRun returns a text node's plain text and the same text with its run
// formatting as CommonMark. Underline and sub/superscript have no CommonMark form and
// are dropped from the markup (the words stay).
func doclingRun(t *doclingText) (string, string) {
	plain := t.Text
	if plain == "" {
		plain = t.Orig
	}
	if strings.TrimSpace(plain) == "" {
		return plain, plain
	}
	f := t.Formatting
	bold := f != nil && f.Bold
	italic := f != nil && f.Italic
	strike := f != nil && f.Strikethrough
	link := strings.TrimSpace(t.Hyperlink)
	if !bold && !italic && !strike && link == "" {
		return plain, plain
	}
	// Delimiters must hug non-space characters to open/close, so whitespace stays outside.
	lead := plain[:len(plain)-len(strings.TrimLeft(plain, " \t\n"))]
	trail := plain[len(strings.TrimRight(plain, " \t\n")):]
	core := documentir.EscapeMarkdownInline(strings.TrimSpace(plain))
	if strike {
		core = "~~" + core + "~~"
	}
	switch {
	case bold && italic:
		core = "***" + core + "***"
	case bold:
		core = "**" + core + "**"
	case italic:
		core = "*" + core + "*"
	}
	if link != "" {
		core = "[" + core + "](" + markdownDestination(link) + ")"
	}
	return plain, lead + core + trail
}

// markdownDestination makes a URL safe as a CommonMark link destination.
func markdownDestination(u string) string {
	if strings.ContainsAny(u, " ()<>") {
		return "<" + strings.NewReplacer("<", "%3C", ">", "%3E", "\n", "").Replace(u) + ">"
	}
	return u
}

// tableAnchors maps "index of the first text node that belongs to this table" to
// the tables that start there, and reports which text indices belong to a table
// at all.
//
// A table with no reachable text node (no anchor) is not anchored here; the caller
// places it after its parent.
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
