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
//
// The table element is moved to where its rows were, not merely kept where the engine put it:
// engines that emit body text and tables separately (xberg returns page content plus a tables
// array) append every table at the end, so deleting the rows alone would drop them from their
// position and repeat the table somewhere else.
func (d *Document) DropTableShadows() int {
	if len(d.Tables) == 0 || len(d.Elements) == 0 {
		return 0
	}
	rows := make(map[string][][]string, len(d.Tables))
	for i := range d.Tables {
		rows[d.Tables[i].ID] = d.Tables[i].gridText()
	}
	// Where a table's text run was found, the table element belongs at that
	// position. Engines that append their structured tables after the body (xberg
	// returns page content and a separate tables array) would otherwise leave the
	// table at the end of the section: the rows disappear from where they were and
	// reappear out of order, which is exactly what readers notice.
	type relocation struct {
		tableEl int // index of the ElTable element
		start   int // index of the first shadow paragraph
		count   int // number of shadow paragraphs
	}
	drop := make([]bool, len(d.Elements))
	var moves []relocation
	removed := 0
	// floor is where the next table may start looking. When a table's rows are
	// found, the next table must look after them; otherwise the two appended
	// tables an engine emits would share one window and the second would never
	// see its own rows (the first table element sits after every body element).
	floor := 0
	for i := range d.Elements {
		el := &d.Elements[i]
		if el.Type != ElTable {
			continue
		}
		table, ok := rows[el.TableID]
		if ok && len(table) > 1 {
			if start, count, found := matchTableRun(d.Elements, floor, i, table); found {
				for k := start; k < start+count; k++ {
					if !drop[k] {
						drop[k] = true
						removed++
					}
				}
				moves = append(moves, relocation{tableEl: i, start: start, count: count})
				floor = start + count
				continue
			}
		}
		floor = i + 1
	}
	if len(moves) == 0 {
		return 0
	}
	moveAt := make(map[int]int, len(moves))
	movedFrom := make(map[int]bool, len(moves))
	for _, m := range moves {
		moveAt[m.start] = m.tableEl
		movedFrom[m.tableEl] = true
	}
	kept := make([]Element, 0, len(d.Elements))
	for i, el := range d.Elements {
		if tableEl, ok := moveAt[i]; ok {
			// The table takes the place of the first shadow paragraph (which is
			// dropped in the same step, keeping row order intact).
			kept = append(kept, d.Elements[tableEl])
		}
		if drop[i] || movedFrom[i] {
			continue
		}
		kept = append(kept, el)
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
	texts := make([]string, 0, len(lines))
	for _, line := range lines {
		texts = append(texts, line)
	}
	return rowsMatchNormalised(texts, table)
}

func rowsMatchTexts(run []Element, table [][]string) bool {
	texts := make([]string, 0, len(run))
	for _, el := range run {
		texts = append(texts, el.Text)
	}
	return rowsMatchNormalised(texts, table)
}

// rowsMatchNormalised compares rendered lines with a table's rows.
//
// One row may differ: header rows are the part engines reformat most (footnote
// marks, spacing inside a cell), and requiring a byte-exact header was why a table
// whose body matched its shadow exactly was never relocated. The tolerance is
// capped at a single row and needs at least three rows to apply, so a paragraph
// that merely quotes one line of a table still cannot trigger a relocation.
func rowsMatchNormalised(texts []string, table [][]string) bool {
	if len(texts) < len(table) {
		return false
	}
	mismatches := 0
	for r := range table {
		if normalizeSpace(texts[r]) == normalizeSpace(strings.Join(table[r], " ")) {
			continue
		}
		mismatches++
		allowed := 1
		if len(table) < 3 {
			allowed = 0
		}
		if mismatches > allowed {
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
