package engines

import (
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// Office inputs carry no provenance, and docling's body tree references only a
// few inline runs while the paragraphs live in the texts array. Walking the tree
// there put a caption dozens of elements away from the table it introduces, and
// appended every table at the end.
func TestDoclingOfficeOrderUsesTextArrayAndAnchorsTables(t *testing.T) {
	raw := []byte(`{
	  "schema_name": "DoclingDocument",
	  "version": "1.0.0",
	  "body": {"children": [{"$ref": "#/texts/0"}]},
	  "texts": [
	    {"self_ref": "#/texts/0", "label": "text", "text": "Title"},
	    {"self_ref": "#/texts/1", "label": "text", "text": "Caption:"},
	    {"self_ref": "#/texts/2", "label": "text", "text": "Region"},
	    {"self_ref": "#/texts/3", "label": "text", "text": "Pallets"},
	    {"self_ref": "#/texts/4", "label": "text", "text": "After the table"}
	  ],
	  "groups": [
	    {"self_ref": "#/groups/0", "label": "unspecified", "name": "rich_cell_group_1_0_",
	     "children": [{"$ref": "#/texts/2"}, {"$ref": "#/texts/3"}]}
	  ],
	  "tables": [{
	    "self_ref": "#/tables/0",
	    "label": "table",
	    "children": [{"$ref": "#/groups/0"}],
	    "data": {"num_rows": 2, "num_cols": 2, "table_cells": [
	      {"start_row_offset_idx": 0, "start_col_offset_idx": 0, "text": "Region"},
	      {"start_row_offset_idx": 0, "start_col_offset_idx": 1, "text": "Pallets"},
	      {"start_row_offset_idx": 1, "start_col_offset_idx": 0, "text": "North"},
	      {"start_row_offset_idx": 1, "start_col_offset_idx": 1, "text": "48,120"}
	    ]}
	  }]
	}`)
	doc := documentir.New()
	if err := mapDocling(doc, raw, "none"); err != nil {
		t.Fatalf("mapDocling: %v", err)
	}
	var types []string
	for _, el := range doc.Elements {
		types = append(types, el.Type)
	}
	want := []string{documentir.ElParagraph, documentir.ElParagraph, documentir.ElTable, documentir.ElParagraph}
	if len(types) != len(want) {
		t.Fatalf("elements = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("position %d = %s, want %s (all: %v)", i, types[i], want[i], types)
		}
	}
	if doc.Elements[1].Text != "Caption:" || doc.Elements[3].Text != "After the table" {
		t.Fatalf("order around the table is wrong: %+v", doc.Elements)
	}
	if len(doc.Tables) != 1 {
		t.Fatalf("tables = %d, want 1", len(doc.Tables))
	}
	// The header cells are rendered by the table, not as extra paragraphs.
	for _, el := range doc.Elements {
		if el.Type == documentir.ElParagraph && (el.Text == "Region" || el.Text == "Pallets") {
			t.Fatalf("table header cell leaked into the body: %+v", el)
		}
	}
}

// Documents with provenance (PDF) keep the body tree as reading order: the texts
// array may be column-interleaved there.
func TestDoclingPDFOrderUsesBodyTree(t *testing.T) {
	raw := []byte(`{
	  "schema_name": "DoclingDocument",
	  "version": "1.0.0",
	  "pages": {"1": {"page_no": 1, "size": {"width": 595, "height": 842}}},
	  "body": {"children": [{"$ref": "#/texts/1"}, {"$ref": "#/texts/0"}]},
	  "texts": [
	    {"self_ref": "#/texts/0", "label": "text", "text": "second column",
	     "prov": [{"page_no": 1, "bbox": {"l": 300, "t": 100, "r": 500, "b": 200, "coord_origin": "BOTTOMLEFT"}}]},
	    {"self_ref": "#/texts/1", "label": "text", "text": "first column",
	     "prov": [{"page_no": 1, "bbox": {"l": 50, "t": 100, "r": 250, "b": 200, "coord_origin": "BOTTOMLEFT"}}]}
	  ]
	}`)
	doc := documentir.New()
	if err := mapDocling(doc, raw, "pdf"); err != nil {
		t.Fatalf("mapDocling: %v", err)
	}
	if len(doc.Elements) != 2 || doc.Elements[0].Text != "first column" || doc.Elements[1].Text != "second column" {
		t.Fatalf("body order not honoured: %+v", doc.Elements)
	}
}
