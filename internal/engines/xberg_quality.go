package engines

import (
	"fmt"

	"parsedock.dev/parsedock/internal/documentir"
)

// OCR quality thresholds. Both are calibrated against measurements, not taste:
//
//   - LowConfidenceThreshold 0.60: tesseract reports 0.84-0.95 on clean pages,
//     0.66 on a page downscaled 6x (where the accurate engine is measurably
//     worse, so a retry must NOT fire) and 0.26-0.31 on rotated pages (where the
//     accurate engine fixes the page completely).
//   - NoTextLayerRunes 20: same rule the accurate engine already used, i.e.
//     "extraction produced almost nothing", which is a failure signal rather
//     than a description of scans in general.
const (
	LowConfidenceThreshold = 0.60
	NoTextLayerRunes       = 20
)

// ocrScannableFormats are the inputs for which "almost no text" means the OCR
// or the text extraction failed. Text formats are excluded on purpose: an empty
// CSV is a legitimate empty file, not a failed scan.
func ocrScannableFormat(format string) bool {
	switch format {
	case "pdf", "png", "jpeg", "jpg", "webp":
		return true
	}
	return false
}

// applyOCRQuality turns the engine's OCR metadata into the three independent
// IR signals:
//
//	ocr_used             - OCR ran (informational, never a quality claim)
//	ocr_quality_unknown  - OCR ran, backend reported no confidence at all
//	ocr_low_confidence   - OCR ran and a page reported a score below the
//	                       threshold (the score is quoted in the message)
//
// It also records the numbers on doc.OCR so the scheduler can decide about a
// retry without parsing warning strings.
func (x *Xberg) applyOCRQuality(doc *documentir.Document, out *xbergOutput, used bool, format string) {
	if !used {
		return
	}
	q := &documentir.OCRQuality{Used: true}
	backend := "unknown"
	for _, p := range out.Result.Pages {
		if b := p.OCRConfidence.Backend; b != "" {
			backend = b
		}
		if p.OCRConfidence.Score == nil {
			continue
		}
		score := *p.OCRConfidence.Score
		if !q.Scored || score < q.MinScore {
			q.Scored = true
			q.MinScore = score
			q.MinScorePage = p.PageNumber
		}
	}
	doc.OCR = q

	doc.AddWarning(documentir.WarnOCRUsed,
		fmt.Sprintf("OCR ran on at least one page (backend=%s)", backend), "info")
	if !q.Scored {
		doc.AddWarning(documentir.WarnOCRUsedUnknown,
			fmt.Sprintf("OCR ran but backend %s reported no confidence; quality is unknown, not known-bad", backend), "info")
	} else if q.MinScore < LowConfidenceThreshold {
		doc.AddWarning(documentir.WarnOCRLowConfidence,
			fmt.Sprintf("OCR confidence %.2f on page %d is below the %.2f threshold (backend=%s)",
				q.MinScore, q.MinScorePage, LowConfidenceThreshold, backend), "warn")
	}
	if ocrScannableFormat(format) && doc.TextLength() < NoTextLayerRunes {
		doc.AddWarning(documentir.WarnNoTextLayer,
			fmt.Sprintf("extraction produced %d characters: the page likely has no usable text layer or the OCR found nothing", doc.TextLength()), "warn")
	}
}
