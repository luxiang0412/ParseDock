package engines

import (
	"fmt"
	"unicode"

	"parsedock.dev/parsedock/internal/documentir"
)

// ShouldEscalateToAccurate reports whether a fast-path result is a poor OCR
// result on a CJK-heavy document, i.e. exactly the case where the accurate
// engine's PP-OCR models are measurably better (real-corpus test: xberg 0.82 CER
// vs docling-rs 0.098 on a Chinese scan).
//
// It is deliberately narrow: only mode=auto, only OCR warns, only CJK text, and
// only for image/PDF inputs. Everything else stays on the engine the router chose.
func ShouldEscalateToAccurate(mode, format string, doc *documentir.Document) bool {
	if mode != "" && mode != "auto" {
		return false
	}
	switch format {
	case "pdf", "png", "jpeg", "webp":
	default:
		return false
	}
	if doc == nil || !hasOCRWarning(doc) {
		return false
	}
	return CJKRatio(doc) >= 0.15
}

func hasOCRWarning(doc *documentir.Document) bool {
	for _, w := range doc.Warnings {
		switch w.Code {
		case documentir.WarnOCRLowConfidence, documentir.WarnNoTextLayer:
			return true
		}
	}
	return false
}

// CJKRatio is the share of letters that are Han/Kana/Hangul, computed over the
// element text of the document (tables included via their cell text).
func CJKRatio(doc *documentir.Document) float64 {
	var letters, cjk int
	count := func(s string) {
		for _, r := range s {
			if !unicode.IsLetter(r) {
				continue
			}
			letters++
			if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
				cjk++
			}
		}
	}
	for _, el := range doc.Elements {
		count(el.Text)
	}
	for _, t := range doc.Tables {
		for _, c := range t.Cells {
			count(c.Text)
		}
	}
	if letters == 0 {
		return 0
	}
	return float64(cjk) / float64(letters)
}

// EscalationReason is recorded in routing.reason so the fallback is auditable.
func EscalationReason(from, to string, ratio float64) string {
	return fmt.Sprintf("ocr_quality_escalation: %s warned low OCR confidence on a CJK-heavy document (%.0f%% CJK letters); retried with %s",
		from, ratio*100, to)
}
