package documentir

import "fmt"

// Limits bound what the service will accept from an engine before it renders or stores a
// document. Rendering materialises a dense table grid, so an IR that merely *claims* a
// gigantic table would otherwise allocate it inside the coordinator — the engine's own RSS
// limit protects the worker, not us. These ceilings are far above any real document (the
// corporate corpus peaks at ~200k characters and a few thousand table cells).
type Limits struct {
	MaxElements   int
	MaxTables     int
	MaxTableCells int64
	MaxTextBytes  int64
}

// DefaultLimits is used when a caller has no reason to choose its own.
var DefaultLimits = Limits{
	MaxElements: 2_000_000,
	MaxTables:   10_000,
	// A real SAP ledger in the corporate corpus declares 2.16M cells in one sheet; the
	// ceiling is a shape guard against absurd claims, not a quality limit.
	MaxTableCells: 10_000_000,
	MaxTextBytes:  512 << 20,
}

// Validate rejects a document whose declared shape would make rendering unsafe.
//
// It checks declared dimensions rather than actual content, which is the point: the cost of
// rendering is driven by Rows*Cols, not by len(Cells).
func (l Limits) Validate(d *Document) error {
	if d == nil {
		return fmt.Errorf("document is nil")
	}
	if n := len(d.Elements); n > l.MaxElements {
		return fmt.Errorf("document has %d elements (limit %d)", n, l.MaxElements)
	}
	if n := len(d.Tables); n > l.MaxTables {
		return fmt.Errorf("document has %d tables (limit %d)", n, l.MaxTables)
	}
	var cells int64
	var text int64
	for i := range d.Tables {
		t := &d.Tables[i]
		if t.Rows < 0 || t.Cols < 0 {
			return fmt.Errorf("table %q has negative dimensions %dx%d", t.ID, t.Rows, t.Cols)
		}
		// Multiply in int64: Rows and Cols are attacker-controlled ints.
		area := int64(t.Rows) * int64(t.Cols)
		if t.Rows > 0 && area/int64(t.Rows) != int64(t.Cols) {
			return fmt.Errorf("table %q dimensions overflow", t.ID)
		}
		if area > l.MaxTableCells {
			return fmt.Errorf("table %q declares %d cells (limit %d)", t.ID, area, l.MaxTableCells)
		}
		cells += area
		if cells > l.MaxTableCells*2 {
			return fmt.Errorf("document declares more than %d table cells", l.MaxTableCells*2)
		}
	}
	// Rendering cost is driven by *references*, not declarations: every table element renders
	// its table again, so a document with one large table and many references to it would
	// allocate the grid repeatedly.
	areas := make(map[string]int64, len(d.Tables))
	for i := range d.Tables {
		id := d.Tables[i].ID
		if id == "" {
			return fmt.Errorf("table %d has no id", i)
		}
		if _, dup := areas[id]; dup {
			// The renderers disagree on duplicates (text uses the first, markdown the last),
			// so a document with duplicate IDs can neither be rendered faithfully nor be
			// bounded by the reference accounting below.
			return fmt.Errorf("duplicate table id %q", id)
		}
		areas[id] = int64(d.Tables[i].Rows) * int64(d.Tables[i].Cols)
	}
	var rendered int64
	for i := range d.Elements {
		if d.Elements[i].Type != ElTable {
			continue
		}
		rendered += areas[d.Elements[i].TableID]
		if rendered > l.MaxTableCells*2 {
			return fmt.Errorf("document renders %d table cells across its tables (limit %d)",
				rendered, l.MaxTableCells*2)
		}
	}
	// rich_text is a second copy of the same content and is rendered too, so it counts
	// against the same budget as text.
	for i := range d.Elements {
		text += int64(len(d.Elements[i].Text)) + int64(len(d.Elements[i].RichText))
	}
	for i := range d.Tables {
		for j := range d.Tables[i].Cells {
			text += int64(len(d.Tables[i].Cells[j].Text)) + int64(len(d.Tables[i].Cells[j].RichText))
		}
	}
	if text > l.MaxTextBytes {
		return fmt.Errorf("document text is %d bytes (limit %d)", text, l.MaxTextBytes)
	}
	return nil
}
