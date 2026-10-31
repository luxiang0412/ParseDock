package engines

import (
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// outWith builds the minimal xberg JSON shape the quality derivation reads.
func outWith(backend string, scores ...*float64) *xbergOutput {
	o := &xbergOutput{}
	for i, s := range scores {
		p := xbergPage{PageNumber: i + 1}
		p.OCRConfidence.Score = s
		p.OCRConfidence.Backend = backend
		if s != nil {
			p.OCRConfidence.WordCount = 100
		}
		o.Result.Pages = append(o.Result.Pages, p)
	}
	return o
}

func f(v float64) *float64 { return &v }

func docText(s string) *documentir.Document {
	d := documentir.New()
	if s != "" {
		d.Elements = []documentir.Element{{ID: "el_1", Type: documentir.ElParagraph, Text: s}}
	}
	return d
}

func codes(d *documentir.Document) []string {
	out := make([]string, 0, len(d.Warnings))
	for _, w := range d.Warnings {
		out = append(out, w.Code)
	}
	return out
}

// PR-B: OCR having run is informational. It must not be reported as low quality.
func TestOCRUsedIsNotLowConfidence(t *testing.T) {
	x := &Xberg{}
	doc := docText(strings.Repeat("采购订单金额 ", 40))
	x.applyOCRQuality(doc, outWith("paddle-ocr", nil, nil), true, "pdf")

	got := codes(doc)
	if !containsStr(got, documentir.WarnOCRUsed) {
		t.Fatalf("ocr_used missing: %v", got)
	}
	if !containsStr(got, documentir.WarnOCRUsedUnknown) {
		t.Fatalf("ocr_quality_unknown missing: %v", got)
	}
	if containsStr(got, documentir.WarnOCRLowConfidence) {
		t.Fatalf("unknown confidence reported as low: %v", got)
	}
	if doc.OCR == nil || doc.OCR.Scored {
		t.Fatalf("OCR evidence = %+v, want used/unscored", doc.OCR)
	}
}

// A low score is only low confidence when the engine actually reported one.
func TestLowConfidenceNeedsAScore(t *testing.T) {
	x := &Xberg{}
	low := docText("采购订单")
	x.applyOCRQuality(low, outWith("tesseract", f(0.42)), true, "pdf")
	if !containsStr(codes(low), documentir.WarnOCRLowConfidence) {
		t.Fatalf("0.42 not reported as low: %v", codes(low))
	}
	for _, w := range low.Warnings {
		if w.Code == documentir.WarnOCRLowConfidence {
			if !strings.Contains(w.Message, "0.42") {
				t.Fatalf("warning does not quote the score: %q", w.Message)
			}
			if w.Severity != "warn" {
				t.Fatalf("severity = %q, want warn", w.Severity)
			}
		}
	}
	if low.OCR == nil || !low.OCR.Scored || low.OCR.MinScore != 0.42 || low.OCR.MinScorePage != 1 {
		t.Fatalf("OCR evidence = %+v", low.OCR)
	}

	good := docText("采购订单")
	x.applyOCRQuality(good, outWith("tesseract", f(0.85)), true, "pdf")
	if containsStr(codes(good), documentir.WarnOCRLowConfidence) {
		t.Fatalf("0.85 reported as low: %v", codes(good))
	}
	if good.OCR == nil || !good.OCR.Scored || good.OCR.MinScore != 0.85 {
		t.Fatalf("OCR evidence = %+v", good.OCR)
	}
}

// The minimum across scored pages decides, so one bad page is not averaged away.
func TestLowConfidenceUsesTheWorstPage(t *testing.T) {
	x := &Xberg{}
	doc := docText("采购订单")
	x.applyOCRQuality(doc, outWith("tesseract", f(0.91), f(0.55), f(0.88)), true, "pdf")
	if doc.OCR == nil || doc.OCR.MinScore != 0.55 || doc.OCR.MinScorePage != 2 {
		t.Fatalf("OCR evidence = %+v, want min 0.55 on page 2", doc.OCR)
	}
}

// "Almost nothing extracted" is a failure signal; a scan with text is not.
func TestNoTextLayerOnlyWhenAlmostEmpty(t *testing.T) {
	x := &Xberg{}
	empty := docText("页码")
	x.applyOCRQuality(empty, outWith("tesseract", f(0.9)), true, "pdf")
	if !containsStr(codes(empty), documentir.WarnNoTextLayer) {
		t.Fatalf("empty extraction not flagged: %v", codes(empty))
	}

	full := docText(strings.Repeat("季度物流复核报告 ", 40))
	x.applyOCRQuality(full, outWith("tesseract", f(0.9)), true, "pdf")
	if containsStr(codes(full), documentir.WarnNoTextLayer) {
		t.Fatalf("scan with text flagged as no_text_layer: %v", codes(full))
	}

	// A short text file is a legitimate empty document, not a failed scan.
	txt := docText("x")
	x.applyOCRQuality(txt, outWith("tesseract", f(0.9)), true, "txt")
	if containsStr(codes(txt), documentir.WarnNoTextLayer) {
		t.Fatalf("text format flagged as no_text_layer: %v", codes(txt))
	}
}

// No OCR at all means no OCR warnings of any kind.
func TestNoOCRMeansNoOCRWarning(t *testing.T) {
	x := &Xberg{}
	doc := docText("text layer")
	x.applyOCRQuality(doc, outWith("tesseract", f(0.2)), false, "pdf")
	if len(doc.Warnings) != 0 || doc.OCR != nil {
		t.Fatalf("warnings = %v, ocr = %+v", codes(doc), doc.OCR)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
