package documentir

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(SchemaJSON))
	if err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("document-ir-0.1.json", doc); err != nil {
		t.Fatal(err)
	}
	sch, err := c.Compile("document-ir-0.1.json")
	if err != nil {
		t.Fatalf("schema does not compile: %v", err)
	}
	return sch
}

func validate(t *testing.T, sch *jsonschema.Schema, doc *Document) {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if err := sch.Validate(v); err != nil {
		t.Fatalf("IR does not validate:\n%v\n%s", err, string(b))
	}
}

// The schema must accept every IR shape the adapters and renderers produce.
func TestSchemaAcceptsProducedIR(t *testing.T) {
	sch := compileSchema(t)
	validate(t, sch, sample())

	// A fully populated document: bbox with coordinate space, page size, sheet
	// name, merged cell, formula, confidence, metrics with nulls.
	conf := 0.93
	rss := int64(818)
	d := New()
	d.ParserEngine = "docling-rs"
	d.ParserVersion = "2.53.3"
	d.PageKind = "pdf"
	d.PageCount = 3
	d.Source = Source{FileName: "a.pdf", MimeType: "application/pdf", Format: "pdf", SizeBytes: 1234, SHA256: "abc"}
	d.Routing = Routing{RequestedMode: "accurate", RequestedDevice: "auto", ActualDevice: "cpu",
		Engine: "docling-rs", Fallback: true, Reason: "ocr_quality_escalation"}
	d.Metrics = Metrics{ParseMs: 812, InputBytes: 1234, Pages: 3, PeakRSSMB: &rss}
	d.Elements = []Element{
		{ID: "el_1", Type: ElTitle, Text: "T", Level: 1, ReadingOrder: 0, Confidence: &conf,
			Loc: &SourceLocation{Kind: "pdf", Page: 1, PageWidthPt: 595.28, PageHeightPt: 841.89,
				BBox: &BBox{Coord: "pdf_user_space_v1", X0: 1, Y0: 2, X1: 3, Y1: 4}}},
		{ID: "el_2", Type: ElTable, Text: "1x1", TableID: "tbl_1", ReadingOrder: 1,
			Loc: &SourceLocation{Kind: "sheet", Sheet: "Summary", Range: "A1"}},
		{ID: "el_3", Type: ElImage, Text: "alt", ReadingOrder: 2, Metadata: map[string]string{"k": "v"}},
		{ID: "el_4", Type: ElListItem, Text: "bold item", RichText: "**bold** item", Level: 1, ReadingOrder: 3,
			Metadata: map[string]string{MetaListOrdered: "true", MetaListMarker: "3."}},
	}
	d.Tables = []Table{{ID: "tbl_1", Rows: 1, Cols: 1, HeaderRows: 1, Confidence: &conf,
		Loc: &SourceLocation{Kind: "sheet", Sheet: "Summary"},
		Cells: []Cell{{Row: 0, Col: 0, RowSpan: 1, ColSpan: 2, Text: "42", RichText: "**42**",
			Formula: "=SUM(A1:A2)", CachedValue: "42", DataType: "number", Confidence: &conf}}}}
	d.AddWarning(WarnOCRLowConfidence, "low", "warn")
	d.Finalize()
	validate(t, sch, d)
}

// The schema must reject fabricated or malformed provenance.
func TestSchemaRejectsInvalidIR(t *testing.T) {
	sch := compileSchema(t)
	bad := []string{
		`{"schema_version":"doc-ir/0.1","source":{},"parser_engine":"x","routing":{},"page_count":0,
		  "page_kind":"pdf","elements":[{"id":"e","type":"nonsense","text":"","reading_order":0}],
		  "warnings":[],"metrics":{}}`,
		`{"schema_version":"doc-ir/0.1","source":{},"parser_engine":"x","routing":{},"page_count":0,
		  "page_kind":"pdf","elements":[{"id":"e","type":"paragraph","text":"",
		  "reading_order":0,"source_location":{"kind":"pdf","bbox":{"coord":"pixels","x0":1,"y0":2,"x1":3,"y1":4}}}],
		  "warnings":[],"metrics":{}}`,
		`{"schema_version":"doc-ir/0.1","source":{},"parser_engine":"x","routing":{},"page_count":0,
		  "page_kind":"pdf","elements":[],"tables":[{"id":"t","rows":1,"cols":1,"header_rows":1,
		  "cells":[{"row":0,"col":0,"row_span":0,"col_span":1,"text":"x"}]}],"warnings":[],"metrics":{}}`,
		`{"schema_version":"doc-ir/0.2","source":{},"parser_engine":"x","routing":{},"page_count":0,
		  "page_kind":"pdf","elements":[],"warnings":[],"metrics":{}}`,
	}
	for i, raw := range bad {
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if err := sch.Validate(v); err == nil {
			t.Errorf("case %d: invalid IR accepted", i)
		}
	}
}

// The published copy under api/ must be identical to the embedded schema.
func TestPublishedSchemaMatches(t *testing.T) {
	published := filepath.Join("..", "..", "api", "document-ir-0.1.schema.json")
	b, err := os.ReadFile(published)
	if err != nil {
		t.Fatalf("published schema missing (run `make schema`): %v", err)
	}
	if !bytes.Equal(b, SchemaJSON) {
		t.Fatal("api/document-ir-0.1.schema.json differs from the embedded schema (run `make schema`)")
	}
}
