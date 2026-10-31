package engines

import (
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// ocrDoc builds a document the way an adapter does after applyOCRQuality: text,
// warnings, and (optionally) the in-memory OCR evidence.
func ocrDoc(text string, warns []string, quality *documentir.OCRQuality) *documentir.Document {
	d := documentir.New()
	d.Elements = []documentir.Element{{ID: "el_1", Type: documentir.ElParagraph, Text: text}}
	for _, w := range warns {
		d.AddWarning(w, "test", "info")
	}
	d.OCR = quality
	return d
}

const (
	cjkText   = "采购订单 供应商 华北物流有限公司 金额 人民币 128,400 元"
	latinText = "INVOICE Supplier Northwind Logistics total amount 18,420.50 EUR"
)

func TestEscalationTriggers(t *testing.T) {
	low := &documentir.OCRQuality{Used: true, Scored: true, MinScore: 0.26, MinScorePage: 1}
	unknown := &documentir.OCRQuality{Used: true} // paddle-ocr: no score at all
	high := &documentir.OCRQuality{Used: true, Scored: true, MinScore: 0.85, MinScorePage: 1}
	// 0.66 is the 6x-downscaled page: readable, and the accurate engine is
	// measurably worse there, so a retry must not fire.
	nearThreshold := &documentir.OCRQuality{Used: true, Scored: true, MinScore: 0.66, MinScorePage: 1}

	cases := []struct {
		name   string
		mode   string
		format string
		doc    *documentir.Document
		want   bool
		code   string
	}{
		{"measured low score on CJK escalates", "auto", "pdf",
			ocrDoc(cjkText, []string{documentir.WarnOCRUsed, documentir.WarnOCRLowConfidence}, low), true, "low_confidence"},
		{"measured low score on Latin also escalates (rotated English recovers too)", "auto", "pdf",
			ocrDoc(latinText, []string{documentir.WarnOCRUsed, documentir.WarnOCRLowConfidence}, low), true, "low_confidence"},
		{"score just above the threshold does not", "auto", "pdf",
			ocrDoc(cjkText, []string{documentir.WarnOCRUsed}, nearThreshold), false, ""},
		{"high score does not", "auto", "pdf",
			ocrDoc(cjkText, []string{documentir.WarnOCRUsed}, high), false, ""},
		{"OCR used with unknown quality does not", "auto", "pdf",
			ocrDoc(cjkText, []string{documentir.WarnOCRUsed, documentir.WarnOCRUsedUnknown}, unknown), false, ""},
		{"OCR used alone does not", "auto", "pdf",
			ocrDoc(cjkText, []string{documentir.WarnOCRUsed}, unknown), false, ""},
		{"nothing extracted escalates regardless of script", "auto", "pdf",
			ocrDoc("页码", []string{documentir.WarnOCRUsed, documentir.WarnNoTextLayer}, unknown), true, "no_text_layer"},
		{"scanned PDF with plenty of text does not escalate", "auto", "pdf",
			ocrDoc(strings.Repeat("采购订单金额 ", 80), []string{documentir.WarnOCRUsed, documentir.WarnOCRUsedUnknown}, unknown), false, ""},
		{"fast mode is an explicit choice", "fast", "pdf",
			ocrDoc(cjkText, []string{documentir.WarnOCRLowConfidence}, low), false, ""},
		{"accurate mode is already the accurate engine", "accurate", "pdf",
			ocrDoc(cjkText, []string{documentir.WarnOCRLowConfidence}, low), false, ""},
		{"text formats never escalate", "auto", "docx",
			ocrDoc(cjkText, []string{documentir.WarnOCRLowConfidence}, low), false, ""},
		{"nil document", "auto", "pdf", nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			trig, got := EscalationTriggerFor(c.mode, c.format, c.doc)
			if got != c.want {
				t.Fatalf("trigger = %v (%s), want %v", got, trig.String(), c.want)
			}
			if got && trig.Code != c.code {
				t.Fatalf("trigger code = %q, want %q", trig.Code, c.code)
			}
			if b := ShouldEscalateToAccurate(c.mode, c.format, c.doc); b != c.want {
				t.Fatalf("ShouldEscalateToAccurate = %v, want %v", b, c.want)
			}
		})
	}
}

// A scan is the normal case for OCR: only "almost nothing extracted" may look
// like a failure.
func TestNoTextLayerIsAFailureSignalNotAScanMarker(t *testing.T) {
	scanWithText := ocrDoc(strings.Repeat("季度物流复核报告 ", 40), []string{documentir.WarnOCRUsed}, nil)
	if ShouldEscalateToAccurate("auto", "pdf", scanWithText) {
		t.Fatal("a scan whose OCR produced text must not be re-parsed")
	}
	emptyScan := ocrDoc("", []string{documentir.WarnOCRUsed, documentir.WarnNoTextLayer}, nil)
	if !ShouldEscalateToAccurate("auto", "pdf", emptyScan) {
		t.Fatal("an empty extraction should be retried once")
	}
}

func TestCJKRatio(t *testing.T) {
	cjk := ocrDoc("采购订单", nil, nil)
	if r := CJKRatio(cjk); r < 0.99 {
		t.Fatalf("CJK ratio = %f", r)
	}
	latin := ocrDoc("invoice supplier", nil, nil)
	if r := CJKRatio(latin); r != 0 {
		t.Fatalf("latin ratio = %f", r)
	}
	mixed := ocrDoc("invoice 采购 订单", nil, nil)
	if r := CJKRatio(mixed); r <= 0 || r >= 1 {
		t.Fatalf("mixed ratio = %f", r)
	}
	if r := CJKRatio(documentir.New()); r != 0 {
		t.Fatalf("empty ratio = %f", r)
	}
	if r := CJKRatio(nil); r != 0 {
		t.Fatalf("nil ratio = %f", r)
	}
}

func TestEscalationReasonIsAuditable(t *testing.T) {
	d := EscalationDecision{
		Trigger:      EscalationTrigger{Code: "low_confidence", Score: 0.26, Page: 1, CJK: 0.62},
		Attempts:     2,
		Accepted:     true,
		FastMs:       430,
		AccurateMs:   1650,
		AccurateName: "docling-rs",
	}
	reason := EscalationReason("xberg", d)
	for _, want := range []string{
		"ocr_quality_escalation", "xberg", "docling-rs",
		"trigger=low_confidence:0.26@1", "attempts=2", "decision=accepted",
		"fast_ms=430", "accurate_ms=1650", "cjk=62%",
	} {
		if !strings.Contains(reason, want) {
			t.Fatalf("reason %q missing %q", reason, want)
		}
	}

	rej := d
	rej.Accepted = false
	rej.Reason = "rejected:alt_low_confidence(0.41)"
	rej.AccurateMs = 900
	rejReason := EscalationReason("xberg", rej)
	if !strings.Contains(rejReason, "decision=rejected:alt_low_confidence(0.41)") {
		t.Fatalf("rejection reason lost the detail: %q", rejReason)
	}
	if !strings.Contains(rejReason, "accurate_ms=900") {
		t.Fatalf("rejection reason lost the retry cost: %q", rejReason)
	}
}

func TestTriggerString(t *testing.T) {
	if got := (EscalationTrigger{Code: "no_text_layer"}).String(); got != "no_text_layer" {
		t.Fatalf("got %q", got)
	}
	if got := (EscalationTrigger{}).String(); got != "none" {
		t.Fatalf("got %q", got)
	}
}
