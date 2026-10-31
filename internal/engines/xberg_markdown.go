package engines

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"parsedock.dev/parsedock/internal/documentir"
)

// startsWithHeading reports whether Markdown content opens with an ATX heading whose
// text is name ("## Summary").
func startsWithHeading(content, name string) bool {
	first, _, _ := strings.Cut(content, "\n")
	m := headingRe.FindStringSubmatch(strings.TrimSpace(first))
	return m != nil && strings.TrimSpace(m[2]) == name
}

// matchInlineTable returns the first unmatched inline table that is the same table as
// the structured grid: same shape and at least 80 % of the non-empty cells equal after
// whitespace normalisation. The threshold tolerates the few cells Markdown rendering
// changes (escaped characters, a line break folded into a space) without pairing two
// different tables of the same shape.
func matchInlineTable(inline []documentir.Table, matched []bool, cells [][]string) int {
	rows, cols := len(cells), 0
	for _, r := range cells {
		cols = max(cols, len(r))
	}
	for i := range inline {
		t := &inline[i]
		if matched[i] || t.Rows != rows || t.Cols != cols {
			continue
		}
		grid := plainGrid(t)
		compared, equal := 0, 0
		for r := range cells {
			for c := range cells[r] {
				a, b := normCell(cells[r][c]), normCell(grid[r][c])
				if a == "" && b == "" {
					continue
				}
				compared++
				if a == b {
					equal++
				}
			}
		}
		if compared == 0 || equal*5 >= compared*4 {
			return i
		}
	}
	return -1
}

// overlapsInlineTable reports whether at least half of the structured table's non-empty
// cells (and at least two) appear in one inline table: the same table, shaped differently.
func overlapsInlineTable(inline []documentir.Table, cells [][]string) bool {
	for i := range inline {
		have := map[string]bool{}
		for _, c := range inline[i].Cells {
			if n := normCell(c.Text); n != "" {
				have[n] = true
			}
		}
		total, found := 0, 0
		for _, row := range cells {
			for _, v := range row {
				n := normCell(v)
				if n == "" {
					continue
				}
				total++
				if have[n] {
					found++
				}
			}
		}
		if total >= 2 && found*2 >= total {
			return true
		}
	}
	return false
}

// enrichInlineTable copies what only the structured table has onto the inline one:
// its location (page, bbox) and cell text with line breaks. Inline formatting from the
// Markdown is kept; a cell whose words differ keeps the inline text, which is what the
// reader saw in the Markdown.
func enrichInlineTable(t *documentir.Table, cells [][]string, loc *documentir.SourceLocation) {
	t.Loc = loc
	for i := range t.Cells {
		c := &t.Cells[i]
		if c.Row >= len(cells) || c.Col >= len(cells[c.Row]) {
			continue
		}
		v := cells[c.Row][c.Col]
		if c.RichText == "" && v != c.Text && normCell(v) == normCell(c.Text) {
			c.Text = v
			c.DataType = guessType(v)
		}
	}
}

// plainGrid returns a table's plain cell text as a dense grid.
func plainGrid(t *documentir.Table) [][]string {
	grid := make([][]string, t.Rows)
	for r := range grid {
		grid[r] = make([]string, t.Cols)
	}
	for _, c := range t.Cells {
		if c.Row >= 0 && c.Row < t.Rows && c.Col >= 0 && c.Col < t.Cols {
			grid[c.Row][c.Col] = c.Text
		}
	}
	return grid
}

func normCell(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

var (
	// A paragraph that stops mid-sentence: ends in a letter, digit or comma.
	openEndRe = regexp.MustCompile(`[\p{L}\p{N},]$`)
	// xberg's Markdown writer starts a new paragraph at a source line that begins with a
	// number ("4.2 hours ...", "2.4.1, which ..."), presumably to keep it from being read
	// as an ordered-list marker. The plain content has no such break.
	digitStartRe = regexp.MustCompile(`^[0-9]`)
)

// joinSplitParagraphs repairs xberg's Markdown paragraph splits on text-layer PDF
// pages: a paragraph that ends mid-sentence followed by one that starts with a digit is
// one paragraph. Both conditions are required and the first part must be prose-length
// (40+ characters), so a label line ("Total amount") above a figure is not joined.
// Measured on the fixtures in TestXbergParagraphRepairAudit.
func joinSplitParagraphs(d *documentir.Document) int {
	if len(d.Elements) < 2 {
		return 0
	}
	out := d.Elements[:1]
	joined := 0
	for _, el := range d.Elements[1:] {
		prev := &out[len(out)-1]
		if canJoinParagraphs(prev, &el) {
			sep := joinSeparator(prev.Text, el.Text)
			if prev.RichText != "" || el.RichText != "" {
				// The side without rich text is plain: escape it before it becomes markup.
				prev.RichText = richOrEscaped(prev) + sep + richOrEscaped(&el)
			}
			prev.Text = prev.Text + sep + el.Text
			joined++
			continue
		}
		out = append(out, el)
	}
	d.Elements = out
	return joined
}

// joinSeparator is the text placed where a split paragraph is rejoined: nothing between
// Chinese/Japanese characters (those scripts do not separate words with spaces, so a
// space would be an artefact of the PDF line break), a single space otherwise.
func joinSeparator(prev, next string) string {
	a, _ := utf8.DecodeLastRuneInString(strings.TrimRight(prev, " "))
	b, _ := utf8.DecodeRuneInString(strings.TrimLeft(next, " "))
	if isCJKRune(a) || isCJKRune(b) {
		return ""
	}
	return " "
}

func isCJKRune(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) ||
		(r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

func richOrEscaped(el *documentir.Element) string {
	if el.RichText != "" {
		return el.RichText
	}
	return documentir.EscapeMarkdownInline(el.Text)
}

func canJoinParagraphs(prev, next *documentir.Element) bool {
	if prev.Type != documentir.ElParagraph || next.Type != documentir.ElParagraph {
		return false
	}
	if len(prev.Metadata) > 0 || len(next.Metadata) > 0 {
		return false
	}
	p := strings.TrimSpace(prev.Text)
	n := strings.TrimSpace(next.Text)
	if utf8.RuneCountInString(p) < 40 || n == "" {
		return false
	}
	if !openEndRe.MatchString(p) || !digitStartRe.MatchString(n) {
		return false
	}
	// The continuation must be prose too, not a lone figure or a numbered heading.
	letters := 0
	for _, r := range n {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters >= 10
}
