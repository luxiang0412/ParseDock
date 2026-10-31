package engines

import (
	"fmt"

	"parsedock.dev/parsedock/internal/documentir"
)

// remapTables renames a fragment's tables into a document-wide namespace and rewrites every
// element reference to them. Adapters merge per-page fragments; without the rename two pages
// that both start at tbl_1 collide, and a rename without rewriting TableID leaves elements
// pointing at a table that no longer exists.
func remapTables(fragment *documentir.Document, existingTables, existingElements int) map[string]string {
	renames := make(map[string]string, len(fragment.Tables))
	for i := range fragment.Tables {
		old := fragment.Tables[i].ID
		fragment.Tables[i].ID = fmt.Sprintf("tbl_%d", existingTables+i+1)
		if old != "" {
			renames[old] = fragment.Tables[i].ID
		}
	}
	for i := range fragment.Elements {
		el := &fragment.Elements[i]
		// Element IDs are fragment-local too (el_1, el_2, ...), so two pages produced
		// duplicate IDs; renumber them into the document namespace here, with the
		// references, so callers cannot forget one of the two.
		el.ID = fmt.Sprintf("el_%d", existingElements+i+1)
		if el.TableID != "" {
			if nid, ok := renames[el.TableID]; ok {
				el.TableID = nid
			}
		}
	}
	return renames
}
