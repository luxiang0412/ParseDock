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
	rows := make(map[string]tableText, len(d.Tables))
	for i := range d.Tables {
		rows[d.Tables[i].ID] = tableText{rows: d.Tables[i].gridText(), headerRows: max(d.Tables[i].HeaderRows, 1)}
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
		if ok && len(table.rows) > 1 {
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

// tableText is a table's cell text by row, plus how many leading rows are header rows
// (at least one: engines that do not flag headers still reformat the first row).
type tableText struct {
	rows       [][]string
	headerRows int
}

// matchTableRun looks for the table's rows written out as text between start and end.
// It accepts either a single paragraph with one line per row or a run of consecutive
// paragraphs. Every element of the run must be a paragraph: headings, list items, other
// tables and anything else are never shadows, so they are never deleted. Returns the
// first index of the run, its element count, and whether it matched.
func matchTableRun(elements []Element, start, end int, table tableText) (int, int, bool) {
	n := len(table.rows)
	for k := start; k < end; k++ {
		if elements[k].Type != ElParagraph {
			continue
		}
		// One paragraph holding the whole block (Office exports: tab-separated, newline rows).
		if lines := strings.Split(strings.Trim(elements[k].Text, "\n"), "\n"); len(lines) == n {
			if rowsMatch(lines, table) {
				return k, 1, true
			}
		}
		// One paragraph per row.
		if k+n <= end && rowsMatchTexts(elements[k:k+n], table) {
			return k, n, true
		}
	}
	return 0, 0, false
}

func rowsMatchTexts(run []Element, table tableText) bool {
	texts := make([]string, 0, len(run))
	for _, el := range run {
		if el.Type != ElParagraph {
			return false
		}
		texts = append(texts, el.Text)
	}
	return rowsMatch(texts, table)
}

// rowsMatch compares rendered lines with a table's rows.
//
// One header row may differ: header rows are the part engines reformat most (footnote
// marks, spacing inside a cell), and requiring a byte-exact header was why a table
// whose body matched its shadow exactly was never relocated. The tolerance is capped
// at a single row, applies only to a header row, and needs at least three rows, so a
// paragraph that merely quotes one line of a table - or a body row that says something
// different - still cannot trigger a deletion.
func rowsMatch(texts []string, table tableText) bool {
	if len(texts) < len(table.rows) {
		return false
	}
	mismatches := 0
	for r := range table.rows {
		if normalizeSpace(texts[r]) == normalizeSpace(strings.Join(table.rows[r], " ")) {
			continue
		}
		mismatches++
		if len(table.rows) < 3 || r >= table.headerRows || mismatches > 1 || !resemblesRow(texts[r], table.rows[r]) {
			return false
		}
	}
	return true
}

// resemblesRow reports whether a reformatted line still carries most of a row's cells, so
// the header tolerance cannot swallow an unrelated paragraph that happens to sit above
// the body rows.
func resemblesRow(line string, row []string) bool {
	line = normalizeSpace(line)
	cells, found := 0, 0
	for _, c := range row {
		c = normalizeSpace(c)
		if c == "" {
			continue
		}
		cells++
		if strings.Contains(line, c) {
			found++
		}
	}
	return cells > 0 && found*2 >= cells
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
