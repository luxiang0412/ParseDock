package engines

import (
	"fmt"
	"strings"
	"unicode"

	"parsedock.dev/parsedock/internal/documentir"
)

// EscalationTrigger names the evidence that justified (or would justify) a
// retry on the accurate engine. It exists so the decision is auditable: the IR
// records which signal fired and with what number.
type EscalationTrigger struct {
	Code  string  // "low_confidence" | "no_text_layer"
	Score float64 // measured confidence, only for low_confidence
	Page  int     // page the score came from, only for low_confidence
	CJK   float64 // share of CJK letters in the fast result
}

// String renders the trigger for routing.reason, e.g. "low_confidence:0.26@1".
func (t EscalationTrigger) String() string {
	switch t.Code {
	case "low_confidence":
		return fmt.Sprintf("low_confidence:%.2f@%d", t.Score, t.Page)
	case "":
		return "none"
	}
	return t.Code
}

// EscalationTriggerFor decides whether a fast-path OCR result is unreliable
// enough to be worth one retry on the accurate engine.
//
// Two signals qualify, and only these two:
//
//   - a *measured* OCR confidence below LowConfidenceThreshold;
//   - "almost no text was extracted" (no_text_layer), which is a failure
//     regardless of script.
//
// The low-confidence rule is deliberately not restricted to CJK. An earlier
// draft required CJK >= 0.15 to avoid retrying Latin pages, but measurement
// showed the gate was a blind spot: a 90-degree rotated Chinese page comes back
// as Latin-looking garbage (CJK ratio 0.00) with a low score, and a rotated
// English page comes back empty with no score at all - docling-rs recovers both
// exactly. The script ratio stays in the routing reason for auditability, but it
// no longer gates anything.
//
// Explicitly not signals: "OCR ran" (ocr_used), "the backend reported no
// confidence" (ocr_quality_unknown), a low engine quality score on its own, and
// a high CJK ratio on its own. Treating "unknown" as "bad" would re-parse every
// PP-OCRv6 result, and a scan whose OCR succeeded is the normal case, not a
// defect.
func EscalationTriggerFor(mode, format string, doc *documentir.Document) (EscalationTrigger, bool) {
	if mode != "" && mode != "auto" {
		return EscalationTrigger{}, false
	}
	if !ocrScannableFormat(format) {
		return EscalationTrigger{}, false
	}
	if doc == nil {
		return EscalationTrigger{}, false
	}
	ratio := CJKRatio(doc)
	trig := EscalationTrigger{CJK: ratio}

	// Failure to extract anything is the strongest and simplest signal, and it
	// is script-independent.
	if hasWarningCode(doc, documentir.WarnNoTextLayer) {
		trig.Code = "no_text_layer"
		return trig, true
	}

	// A measured low score is the strongest per-document evidence available, and
	// a retry is measurably better for garbled pages in both scripts.
	if doc.OCR != nil && doc.OCR.Scored && doc.OCR.MinScore < LowConfidenceThreshold {
		trig.Code = "low_confidence"
		trig.Score = doc.OCR.MinScore
		trig.Page = doc.OCR.MinScorePage
		return trig, true
	}
	return EscalationTrigger{}, false
}

// ShouldEscalateToAccurate is the boolean form used by callers that do not need
// the evidence; keep them in sync.
func ShouldEscalateToAccurate(mode, format string, doc *documentir.Document) bool {
	_, ok := EscalationTriggerFor(mode, format, doc)
	return ok
}

func hasWarningCode(doc *documentir.Document, code string) bool {
	for _, w := range doc.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// CJKRatio is the share of letters that are Han/Kana/Hangul, computed over the
// element text of the document (tables included via their cell text).
func CJKRatio(doc *documentir.Document) float64 {
	if doc == nil {
		return 0
	}
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

// EscalationDecision is what happened to the retry, recorded verbatim in
// routing.reason so an operator can see cost and outcome without a second run.
type EscalationDecision struct {
	Trigger      EscalationTrigger
	Attempts     int
	Accepted     bool
	Reason       string // rejected:{error,empty,alt_low_confidence}
	FastMs       int64
	AccurateMs   int64
	AccurateName string
}

// DecisionString renders the decision suffix, e.g.
// "decision=rejected:alt_low_confidence(0.41)".
func (d EscalationDecision) DecisionString() string {
	if d.Accepted {
		return "decision=accepted"
	}
	return "decision=rejected:" + strings.TrimPrefix(d.Reason, "rejected:")
}

// EscalationReason is recorded in routing.reason so the retry, its trigger, its
// cost and its outcome are auditable.
func EscalationReason(from string, d EscalationDecision) string {
	return fmt.Sprintf(
		"ocr_quality_escalation: %s -> %s (trigger=%s, attempts=%d, %s, fast_ms=%d, accurate_ms=%d, cjk=%.0f%%)",
		from, d.AccurateName, d.Trigger.String(), d.Attempts, d.DecisionString(), d.FastMs, d.AccurateMs, d.Trigger.CJK*100)
}
