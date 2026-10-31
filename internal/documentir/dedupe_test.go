package documentir

import "testing"

func shadowTable(id string) Table {
	rows := [][]string{
		{"Metric", "Current", "Prior"},
		{"Revenue", "1,284", "1,150"},
		{"Margin", "18.4%", "17.9%"},
	}
	var cells []Cell
	for r, row := range rows {
		for c, v := range row {
			cells = append(cells, Cell{Row: r, Col: c, Text: v})
		}
	}
	return Table{ID: id, Rows: len(rows), Cols: 3, HeaderRows: 1, Cells: cells}
}

// Review finding: only the first element of a per-row run was checked for type, so a
// heading (or another table) inside the run was deleted as a "shadow".
func TestDropTableShadowsNeverDeletesNonParagraphs(t *testing.T) {
	d := &Document{
		Tables: []Table{shadowTable("tbl_1")},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Metric Current Prior"},
			{ID: "el_2", Type: ElSection, Text: "Revenue 1,284 1,150"},
			{ID: "el_3", Type: ElParagraph, Text: "Margin 18.4% 17.9%"},
			{ID: "el_4", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 0 {
		t.Fatalf("removed %d elements, want 0 (a heading is not a shadow)", n)
	}
	if len(d.Elements) != 4 {
		t.Fatalf("elements = %+v", d.Elements)
	}
}

// The tolerated mismatch applies to the header row only: a body row that differs means
// the run is not this table's text.
func TestDropTableShadowsBodyMismatchIsNotTolerated(t *testing.T) {
	d := &Document{
		Tables: []Table{shadowTable("tbl_1")},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Metric Current Prior"},
			{ID: "el_2", Type: ElParagraph, Text: "Revenue rose sharply this quarter."},
			{ID: "el_3", Type: ElParagraph, Text: "Margin 18.4% 17.9%"},
			{ID: "el_4", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 0 {
		t.Fatalf("removed %d elements, want 0", n)
	}
}

// An unrelated paragraph above the body rows is not a "reformatted header".
func TestDropTableShadowsHeaderToleranceNeedsResemblance(t *testing.T) {
	d := &Document{
		Tables: []Table{shadowTable("tbl_1")},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Two risks need attention."},
			{ID: "el_2", Type: ElParagraph, Text: "Revenue 1,284 1,150"},
			{ID: "el_3", Type: ElParagraph, Text: "Margin 18.4% 17.9%"},
			{ID: "el_4", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 0 {
		t.Fatalf("removed %d elements, want 0", n)
	}
	if d.Elements[0].Text != "Two risks need attention." {
		t.Fatalf("prose deleted: %+v", d.Elements)
	}
}

// The intended tolerance still works for per-row runs: a reformatted header row.
func TestDropTableShadowsReformattedHeaderRun(t *testing.T) {
	d := &Document{
		Tables: []Table{shadowTable("tbl_1")},
		Elements: []Element{
			{ID: "el_1", Type: ElParagraph, Text: "Metric¹ Current Prior"},
			{ID: "el_2", Type: ElParagraph, Text: "Revenue 1,284 1,150"},
			{ID: "el_3", Type: ElParagraph, Text: "Margin 18.4% 17.9%"},
			{ID: "el_4", Type: ElTable, TableID: "tbl_1"},
		},
	}
	if n := d.DropTableShadows(); n != 3 {
		t.Fatalf("removed %d elements, want 3", n)
	}
	if len(d.Elements) != 1 || d.Elements[0].Type != ElTable {
		t.Fatalf("elements = %+v", d.Elements)
	}
}
