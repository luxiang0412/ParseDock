package documentir

import (
	"encoding/json"
	"testing"
)

// The IR is a public contract. A client built against doc-ir/0.1 must keep
// working when the server adds fields or emits vocabulary it does not know:
// element types and warning codes are open strings precisely so this can happen.
func TestUnknownVocabularyAndFieldsAreTolerated(t *testing.T) {
	raw := []byte(`{
	  "schema_version": "doc-ir/0.1",
	  "source": {"file_name": "a.pdf", "mime_type": "application/pdf", "format": "pdf", "size_bytes": 1, "sha256": "x"},
	  "parser_engine": "xberg",
	  "parser_version": "1",
	  "routing": {"requested_mode": "auto", "requested_device": "auto", "actual_device": "cpu",
	              "engine": "xberg", "fallback": false, "reason": "test"},
	  "page_count": 1,
	  "page_kind": "pdf",
	  "elements": [
	    {"id": "el_1", "type": "chart", "text": "sales", "reading_order": 0,
	     "future_field": {"nested": true}},
	    {"id": "el_2", "type": "paragraph", "text": "hello", "reading_order": 1}
	  ],
	  "warnings": [
	    {"code": "ocr_quality_unknown", "message": "x", "severity": "info"},
	    {"code": "brand_new_warning", "message": "added after the client shipped", "severity": "warn"}
	  ],
	  "metrics": {"parse_ms": 5},
	  "future_top_level": [1, 2, 3]
	}`)
	var d Document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatalf("a doc-ir/0.1 client must tolerate additive changes: %v", err)
	}
	if len(d.Elements) != 2 || d.Elements[0].Type != "chart" {
		t.Fatalf("unknown element type was not preserved verbatim: %+v", d.Elements)
	}
	if len(d.Warnings) != 2 || d.Warnings[1].Code != "brand_new_warning" {
		t.Fatalf("unknown warning code was not preserved: %+v", d.Warnings)
	}
	// Round-tripping must not drop the unknown vocabulary it just read.
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(out) || !contains(string(out), `"brand_new_warning"`) {
		t.Fatalf("round-trip lost data: %s", out)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
