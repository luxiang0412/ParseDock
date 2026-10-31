package documentir

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// Format is a supported derivative representation.
type Format string

const (
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
	FormatText     Format = "text"
)

// ParseFormat validates an API format query value.
func ParseFormat(s string) (Format, bool) {
	switch Format(strings.ToLower(strings.TrimSpace(s))) {
	case "", FormatMarkdown, "md":
		return FormatMarkdown, true
	case FormatJSON:
		return FormatJSON, true
	case FormatText:
		return FormatText, true
	}
	return "", false
}

// RenderMarkdown converts the IR to CommonMark + GFM. Tables are rendered from the
// structured grid, never re-parsed from text.
//
// Inline formatting comes from rich_text, which is already Markdown; plain text is
// escaped so that characters the source used literally (an asterisk, a leading
// "1." or "#") are not reinterpreted as markup. Page furniture (header/footer
// elements and content_layer=furniture) is not part of the body and is skipped;
// it stays in the JSON. The output is a pure function of the IR.
func RenderMarkdown(d *Document) string {
	var b strings.Builder
	tables := map[string]*Table{}
	for i := range d.Tables {
		tables[d.Tables[i].ID] = &d.Tables[i]
	}
	var list listState
	for i := range d.Elements {
		el := &d.Elements[i]
		if el.IsFurniture() {
			continue
		}
		if list.open && el.Type != ElListItem {
			b.WriteString("\n")
			list = listState{}
		}
		switch el.Type {
		case ElTitle, ElSection:
			lvl := el.Level
			if lvl < 1 {
				lvl = 1
			}
			if lvl > 6 {
				lvl = 6
			}
			b.WriteString(strings.Repeat("#", lvl) + " " + headingText(el) + "\n\n")
		case ElParagraph:
			if el.Metadata[MetaMarkdown] == MarkdownBlockquote {
				b.WriteString(blockquote(el) + "\n\n")
				continue
			}
			if body := blockText(el); body != "" {
				b.WriteString(body + "\n\n")
			}
		case ElList:
			// container only
		case ElListItem:
			b.WriteString(list.item(el))
		case ElTable:
			if t := tables[el.TableID]; t != nil {
				b.WriteString(renderMarkdownTable(t) + "\n")
			}
		case ElImage:
			alt := oneLine(el.Text)
			if alt == "" {
				alt = "image"
			}
			b.WriteString("![" + escapeLinkText(alt) + "](" + el.ImageRef + ")\n\n")
		case ElFormula:
			b.WriteString("$$\n" + strings.TrimSpace(el.Text) + "\n$$\n\n")
		case ElCaption, ElFootnote:
			if s := inlineOneLine(el); s != "" {
				b.WriteString("_" + s + "_\n\n")
			}
		case ElCode:
			b.WriteString(codeBlock(el) + "\n\n")
		case ElPageBreak:
			b.WriteString("\n---\n\n")
		default:
			if body := blockText(el); body != "" {
				b.WriteString(body + "\n\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RenderText converts the IR to plain text with page markers. It uses the plain
// text only, so inline formatting never reaches it; page furniture is skipped.
func RenderText(d *Document) string {
	var b strings.Builder
	lastPage := 0
	for _, el := range d.Elements {
		if el.IsFurniture() {
			continue
		}
		if el.Loc != nil && el.Loc.Kind == "pdf" && el.Loc.Page > 0 && el.Loc.Page != lastPage {
			if lastPage != 0 {
				b.WriteString("\f\n")
			}
			lastPage = el.Loc.Page
		}
		switch el.Type {
		case ElTable:
			for i := range d.Tables {
				if d.Tables[i].ID == el.TableID {
					b.WriteString(renderTextTable(&d.Tables[i]) + "\n")
					break
				}
			}
		case ElPageBreak:
			continue
		case ElListItem:
			b.WriteString(strings.Repeat("  ", max(0, el.Level-1)) + "- " + oneLine(el.Text) + "\n")
		default:
			if el.Text != "" {
				b.WriteString(strings.TrimRight(el.Text, "\n") + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// listState tracks one run of consecutive list items so ordered items are numbered
// per run and nested items are indented to their parent's content column (CommonMark
// needs "1. " children at three spaces, "- " children at two).
type listState struct {
	open    bool
	counter map[int]int  // next number per level for ordered items without a marker
	ordered map[int]bool // whether the run at a level is ordered
	content []int        // content column per level (index 0 = level 1)
}

var numericMarkerRe = regexp.MustCompile(`^[0-9]{1,9}[.)]$`)

func (s *listState) item(el *Element) string {
	if !s.open {
		*s = listState{open: true, counter: map[int]int{}, ordered: map[int]bool{}}
	}
	level := max(1, el.Level)
	// Leaving deeper levels ends their runs: a later item at that depth starts again at 1.
	for l := range s.counter {
		if l > level {
			delete(s.counter, l)
			delete(s.ordered, l)
		}
	}
	if len(s.content) > level {
		s.content = s.content[:level]
	}
	indent := 0
	if level > 1 {
		if len(s.content) >= level-1 {
			indent = s.content[level-2]
		} else if len(s.content) > 0 {
			indent = s.content[len(s.content)-1] + 2*(level-1-len(s.content))
		} else {
			indent = 2 * (level - 1)
		}
	}
	ordered := el.Metadata[MetaListOrdered] == "true"
	if prev, seen := s.ordered[level]; seen && prev != ordered {
		delete(s.counter, level) // a bullet run and a numbered run are different lists
	}
	s.ordered[level] = ordered
	text := listItemText(el)
	marker := "-"
	if ordered {
		m := strings.TrimSpace(el.Metadata[MetaListMarker])
		switch {
		case numericMarkerRe.MatchString(m):
			marker = m
			if n, err := strconv.Atoi(strings.TrimRight(m, ".)")); err == nil {
				s.counter[level] = n + 1
			}
		case m != "":
			// "a)" or "iv." is not a CommonMark list marker; keep the source's label
			// visible on a bullet rather than renumbering it.
			text = escapeInline(m) + " " + text
		default:
			n := s.counter[level]
			if n == 0 {
				n = 1
			}
			marker = strconv.Itoa(n) + "."
			s.counter[level] = n + 1
		}
	}
	for len(s.content) < level {
		s.content = append(s.content, 0)
	}
	s.content[level-1] = indent + len(marker) + 1
	return strings.Repeat(" ", indent) + marker + " " + text + "\n"
}

var taskMarkerRe = regexp.MustCompile(`^\[[ xX]\] `)

func listItemText(el *Element) string {
	if el.RichText != "" {
		return oneLine(el.RichText)
	}
	t := oneLine(el.Text)
	// "[ ] " / "[x] " is a GFM task marker the engine reported; keep it live.
	if m := taskMarkerRe.FindString(t); m != "" {
		return m + escapeLine(t[len(m):])
	}
	return escapeLine(t)
}

func headingText(el *Element) string {
	if el.RichText != "" {
		return oneLine(el.RichText)
	}
	s := escapeInline(oneLine(el.Text))
	// A trailing " #" run would be read as the optional closing sequence.
	if strings.HasSuffix(s, "#") {
		s = strings.TrimRight(s, "#") + strings.Repeat(`\#`, len(s)-len(strings.TrimRight(s, "#")))
	}
	return s
}

func inlineOneLine(el *Element) string {
	if el.RichText != "" {
		return oneLine(el.RichText)
	}
	return escapeInline(oneLine(el.Text))
}

// blockText is a paragraph body: rich text verbatim, or plain text escaped line by line.
func blockText(el *Element) string {
	if el.RichText != "" {
		return strings.TrimSpace(el.RichText)
	}
	text := strings.TrimSpace(el.Text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			// A blank line inside one paragraph element would split it in two.
			continue
		}
		out = append(out, escapeLine(l))
	}
	return strings.Join(out, "\n")
}

func blockquote(el *Element) string {
	body := el.RichText
	if body == "" {
		var lines []string
		for _, l := range strings.Split(strings.TrimSpace(el.Text), "\n") {
			lines = append(lines, escapeLine(strings.TrimSpace(l)))
		}
		body = strings.Join(lines, "\n")
	}
	var b strings.Builder
	for i, l := range strings.Split(strings.TrimSpace(body), "\n") {
		if i > 0 {
			b.WriteString("\n")
		}
		if l = strings.TrimSpace(l); l == "" {
			b.WriteString(">")
		} else {
			b.WriteString("> " + l)
		}
	}
	return b.String()
}

var codeLangRe = regexp.MustCompile(`^[A-Za-z0-9_+#.-]{1,32}$`)

func codeBlock(el *Element) string {
	body := strings.TrimRight(el.Text, "\n")
	// The fence must be longer than any backtick run inside the block.
	run, longest := 0, 0
	for _, r := range body {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	lang := strings.TrimSpace(el.Metadata[MetaCodeLanguage])
	if !codeLangRe.MatchString(lang) {
		lang = ""
	}
	return fence + lang + "\n" + body + "\n" + fence
}

func renderMarkdownTable(t *Table) string {
	if t.HeaderRows > 1 || hasSpans(t) {
		// A pipe table has exactly one header row and no spans; anything else would be
		// flattened into a grid that misstates which cells belong together.
		return renderHTMLTable(t)
	}
	grid := tableGrid(t, markdownCell)
	if len(grid) == 0 {
		return ""
	}
	var b strings.Builder
	for i, row := range grid {
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", len(row)) + "\n")
		}
	}
	return b.String()
}

func hasSpans(t *Table) bool {
	for _, c := range t.Cells {
		if c.RowSpan > 1 || c.ColSpan > 1 {
			return true
		}
	}
	return false
}

// markdownCell renders one pipe-table cell: inline markup kept, pipes escaped, line
// breaks as <br> (a raw newline would end the row).
func markdownCell(c *Cell) string {
	s := c.RichText
	if s == "" {
		s = escapeInline(c.Text)
	}
	s = strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(s), "\r\n", "\n"), "\n", "<br>")
	return escapePipes(s)
}

// escapePipes escapes every '|' that is not already escaped.
func escapePipes(s string) string {
	if !strings.Contains(s, "|") {
		return s
	}
	var b strings.Builder
	backslashes := 0
	for _, r := range s {
		if r == '|' && backslashes%2 == 0 {
			b.WriteByte('\\')
		}
		if r == '\\' {
			backslashes++
		} else {
			backslashes = 0
		}
		b.WriteRune(r)
	}
	return b.String()
}

// renderHTMLTable renders a table with merged cells. Cell text is HTML-escaped (it is
// untrusted document content), line breaks become <br>, and the output contains no
// blank line so CommonMark keeps it as one HTML block.
func renderHTMLTable(t *Table) string {
	if t.Rows <= 0 || t.Cols <= 0 || int64(t.Rows)*int64(t.Cols) > maxRenderCells {
		return ""
	}
	anchors := make(map[[2]int]*Cell, len(t.Cells))
	for i := range t.Cells {
		c := &t.Cells[i]
		if c.Row < 0 || c.Row >= t.Rows || c.Col < 0 || c.Col >= t.Cols {
			continue
		}
		if _, dup := anchors[[2]int{c.Row, c.Col}]; !dup {
			anchors[[2]int{c.Row, c.Col}] = c
		}
	}
	covered := make([]bool, t.Rows*t.Cols)
	hdr := min(max(t.HeaderRows, 0), t.Rows)
	var b strings.Builder
	b.WriteString("<table>\n")
	for r := 0; r < t.Rows; r++ {
		if r == 0 && hdr > 0 {
			b.WriteString("<thead>\n")
		}
		if r == hdr {
			b.WriteString("<tbody>\n")
		}
		tag := "td"
		if r < hdr {
			tag = "th"
		}
		b.WriteString("<tr>")
		for c := 0; c < t.Cols; c++ {
			if covered[r*t.Cols+c] {
				continue
			}
			cell := anchors[[2]int{r, c}]
			rs, cs := 1, 1
			text := ""
			if cell != nil {
				rs = min(max(cell.RowSpan, 1), t.Rows-r)
				cs = min(max(cell.ColSpan, 1), t.Cols-c)
				// A span may not reach into a cell that is already covered by an earlier
				// span; shorten it rather than emit an overlapping grid.
				for k := 1; k < cs; k++ {
					if covered[r*t.Cols+c+k] {
						cs = k
						break
					}
				}
				text = htmlCellText(cell.Text)
			}
			for dr := 0; dr < rs; dr++ {
				for dc := 0; dc < cs; dc++ {
					covered[(r+dr)*t.Cols+c+dc] = true
				}
			}
			b.WriteString("<" + tag)
			if rs > 1 {
				fmt.Fprintf(&b, ` rowspan="%d"`, rs)
			}
			if cs > 1 {
				fmt.Fprintf(&b, ` colspan="%d"`, cs)
			}
			b.WriteString(">" + text + "</" + tag + ">")
		}
		b.WriteString("</tr>\n")
		if r == hdr-1 {
			b.WriteString("</thead>\n")
		}
	}
	if hdr < t.Rows {
		b.WriteString("</tbody>\n")
	}
	b.WriteString("</table>\n")
	return b.String()
}

func htmlCellText(s string) string {
	s = html.EscapeString(strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n")))
	return strings.ReplaceAll(s, "\n", "<br>")
}

func renderTextTable(t *Table) string {
	var b strings.Builder
	for _, row := range tableGrid(t, textCell) {
		b.WriteString(strings.Join(row, "\t") + "\n")
	}
	return b.String()
}

func textCell(c *Cell) string {
	return strings.ReplaceAll(strings.ReplaceAll(c.Text, "\n", " "), "|", "\\|")
}

// maxRenderCells mirrors Limits.MaxTableCells: ingest validation rejects larger tables, and
// this is the second line of defence so no caller can turn a bad IR into a huge allocation.
const maxRenderCells = 10_000_000

func tableGrid(t *Table, cell func(*Cell) string) [][]string {
	if t.Rows <= 0 || t.Cols <= 0 {
		return nil
	}
	if int64(t.Rows)*int64(t.Cols) > maxRenderCells {
		return nil
	}
	grid := make([][]string, t.Rows)
	for r := range grid {
		grid[r] = make([]string, t.Cols)
	}
	for i := range t.Cells {
		c := &t.Cells[i]
		if c.Row < 0 || c.Row >= t.Rows || c.Col < 0 || c.Col >= t.Cols {
			continue
		}
		grid[c.Row][c.Col] = cell(c)
	}
	return grid
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SummaryLine is a compact human string used in logs and CLI output.
func SummaryLine(d *Document) string {
	return fmt.Sprintf("engine=%s/%s pages=%d elements=%d tables=%d chars=%d warnings=%d",
		d.ParserEngine, d.ParserVersion, d.PageCount, len(d.Elements), len(d.Tables),
		d.Metrics.OutputChars, len(d.Warnings))
}
