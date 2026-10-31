package documentir

import "strings"

// DropTableShadows removes paragraph elements that merely repeat a table's cells.
//
// Engines commonly return a table twice: once as the lines of text they saw on the page and
// once as a structured Table. Both reach the IR, so the rendered Markdown and text showed
// every cell twice. The duplicate is recognisable without guessing: it is a run of one or more
// paragraphs (or a single paragraph with embedded newlines, as Office exports produce) whose
// lines match the table's rows one per row, in order.
//
// The search is bounded to the elements since the previous table, so two identical tables in
// one document cannot delete each other's text, and a paragraph that merely quotes one row is
// never touched (the whole table must be covered).
func (d *Document) DropTableShadows() int {
	if len(d.Tables) == 0 || len(d.Elements) == 0 {
		return 0
	}
	rows := make(map[string][][]string, len(d.Tables))
	for i := range d.Tables {
		rows[d.Tables[i].ID] = d.Tables[i].gridText()
	}
	drop := make([]bool, len(d.Elements))
	removed := 0
	sectionStart := 0
	for i := range d.Elements {
		el := &d.Elements[i]
		if el.Type != ElTable {
			continue
		}
		table, ok := rows[el.TableID]
		if ok && len(table) > 1 {
			if start, count, found := matchTableRun(d.Elements, sectionStart, i, table); found {
				for k := start; k < start+count; k++ {
					if !drop[k] {
						drop[k] = true
						removed++
					}
				}
			}
		}
		sectionStart = i + 1
	}
	if removed == 0 {
		return 0
	}
	kept := d.Elements[:0]
	for i, el := range d.Elements {
		if !drop[i] {
			kept = append(kept, el)
		}
	}
	d.Elements = kept
	return removed
}

// matchTableRun looks for the table's rows written out as text between start and end.
// It accepts either a single paragraph with one line per row or a run of consecutive
// paragraphs. Returns the first index of the run, its element count, and whether it matched.
func matchTableRun(elements []Element, start, end int, table [][]string) (int, int, bool) {
	for k := start; k < end; k++ {
		if elements[k].Type != ElParagraph {
			continue
		}
		// One paragraph holding the whole block (Office exports: tab-separated, newline rows).
		if lines := strings.Split(strings.Trim(elements[k].Text, "\n"), "\n"); len(lines) == len(table) {
			if rowsMatch(lines, table, 0) {
				return k, 1, true
			}
		}
		// One paragraph per row.
		if k+len(table) <= end && rowsMatchTexts(elements[k:k+len(table)], table) {
			return k, len(table), true
		}
	}
	return 0, 0, false
}

func rowsMatch(lines []string, table [][]string, offset int) bool {
	for r := 0; r+offset < len(table) && r < len(lines); r++ {
		if normalizeSpace(lines[r]) != normalizeSpace(strings.Join(table[r+offset], " ")) {
			return false
		}
	}
	return true
}

func rowsMatchTexts(run []Element, table [][]string) bool {
	for r := range table {
		if normalizeSpace(run[r].Text) != normalizeSpace(strings.Join(table[r], " ")) {
			return false
		}
	}
	return true
}

// gridText returns the table's rows as cell text, ordered by row then column.
func (t *Table) gridText() [][]string {
	if t.Rows <= 0 || t.Cols <= 0 {
		return nil
	}
	grid := make([][]string, t.Rows)
	for i := range grid {
		grid[i] = make([]string, t.Cols)
	}
	for _, c := range t.Cells {
		if c.Row < 0 || c.Row >= t.Rows || c.Col < 0 || c.Col >= t.Cols {
			continue
		}
		grid[c.Row][c.Col] = c.Text
	}
	return grid
}

func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
