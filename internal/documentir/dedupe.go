package documentir

import (
	"strings"
)

// DropTableShadows removes paragraph elements that merely repeat a table's cells.
//
// Engines commonly return a table twice: once as the lines of text they saw on the page and
// once as a structured Table. Both end up in the IR, so the rendered Markdown and text
// contain every cell twice (a real corpus document repeated a 5x4 table verbatim). The
// duplicate lines are recognisable — they are the run of paragraphs immediately preceding the
// table element, one per row, in row order, with cells joined by whitespace — so removing
// them is safe; a paragraph that happens to quote one row elsewhere in the document is left
// alone because it is not adjacent to the table element.
//
// Returns the number of elements removed.
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
	for i := len(d.Elements) - 1; i >= 0; i-- {
		el := &d.Elements[i]
		if el.Type != ElTable || el.TableID == "" {
			continue
		}
		table, ok := rows[el.TableID]
		if !ok || len(table) == 0 {
			continue
		}
		// Walk backwards over the immediately preceding paragraphs. They must match the
		// table's rows in order (last row first when walking backwards).
		j := i - 1
		matched := 0
		for r := len(table) - 1; r >= 0 && j >= 0; r-- {
			if d.Elements[j].Type != ElParagraph {
				break
			}
			if normalizeSpace(d.Elements[j].Text) != normalizeSpace(strings.Join(table[r], " ")) {
				break
			}
			j--
			matched++
		}
		// Require the whole table to be covered; a partial match is more likely to be prose.
		if matched != len(table) {
			continue
		}
		for k := j + 1; k < i; k++ {
			if !drop[k] {
				drop[k] = true
				removed++
			}
		}
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
