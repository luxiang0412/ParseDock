package engines

import (
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

func docWith(text string, warn string) *documentir.Document {
	d := documentir.New()
	d.Elements = []documentir.Element{{ID: "el_1", Type: documentir.ElParagraph, Text: text}}
	if warn != "" {
		d.AddWarning(warn, "test", "info")
	}
	return d
}

func TestShouldEscalateToAccurate(t *testing.T) {
	cjkScan := docWith("采购订单 供应商 华北物流有限公司 金额 人民币", documentir.WarnOCRLowConfidence)
	latinScan := docWith("INVOICE Supplier Northwind Logistics total amount", documentir.WarnOCRLowConfidence)
	cjkClean := docWith("采购订单 供应商 华北物流有限公司 金额 人民币", "")
	empty := documentir.New()

	cases := []struct {
		name   string
		mode   string
		format string
		doc    *documentir.Document
		want   bool
	}{
		{"cjk scan in auto mode", "auto", "pdf", cjkScan, true},
		{"cjk scan in fast mode is an explicit choice", "fast", "pdf", cjkScan, false},
		{"latin scan stays on the fast engine", "auto", "pdf", latinScan, false},
		{"clean cjk text needs no retry", "auto", "pdf", cjkClean, false},
		{"non-image formats never escalate", "auto", "docx", cjkScan, false},
		{"no-text-layer warning also triggers", "auto", "png", docWith("采购订单 金额", documentir.WarnNoTextLayer), true},
		{"empty document", "auto", "pdf", empty, false},
		{"nil document", "auto", "pdf", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ShouldEscalateToAccurate(c.mode, c.format, c.doc); got != c.want {
				t.Fatalf("ShouldEscalateToAccurate = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCJKRatio(t *testing.T) {
	cjk := docWith("采购订单", "")
	if r := CJKRatio(cjk); r < 0.99 {
		t.Fatalf("CJK ratio = %f", r)
	}
	latin := docWith("invoice supplier", "")
	if r := CJKRatio(latin); r != 0 {
		t.Fatalf("latin ratio = %f", r)
	}
	mixed := docWith("invoice 采购 订单", "")
	if r := CJKRatio(mixed); r <= 0 || r >= 1 {
		t.Fatalf("mixed ratio = %f", r)
	}
	if r := CJKRatio(documentir.New()); r != 0 {
		t.Fatalf("empty ratio = %f", r)
	}
}

func TestEscalationReasonIsAuditable(t *testing.T) {
	reason := EscalationReason("xberg", "docling-rs", 0.62)
	for _, want := range []string{"ocr_quality_escalation", "xberg", "docling-rs", "62%"} {
		if !contains(reason, want) {
			t.Fatalf("reason %q missing %q", reason, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
