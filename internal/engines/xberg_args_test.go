package engines

import (
	"strings"
	"testing"
)

func argsOf(x *Xberg) string {
	return strings.Join(x.extractArgs(), " ")
}

// The default deployment must not change: no --ocr-backend flag at all, so the
// engine applies its own default (tesseract).
func TestXbergArgsDefaultHasNoOCRBackend(t *testing.T) {
	x := &Xberg{OCR: true, OCRLang: "eng"}
	got := argsOf(x)
	if strings.Contains(got, "--ocr-backend") {
		t.Fatalf("default args must not select an OCR backend, got %q", got)
	}
	if !strings.Contains(got, "--ocr-language eng") {
		t.Fatalf("language flag missing: %q", got)
	}
	if !strings.Contains(got, "--ocr true") {
		t.Fatalf("ocr flag missing: %q", got)
	}
}

func TestXbergArgsPassesOCRBackend(t *testing.T) {
	x := &Xberg{OCR: true, OCRBackend: "paddle-ocr", OCRLang: "ch"}
	got := argsOf(x)
	if !strings.Contains(got, "--ocr-backend paddle-ocr") {
		t.Fatalf("backend flag missing: %q", got)
	}
	if !strings.Contains(got, "--ocr-language ch") {
		t.Fatalf("language flag missing: %q", got)
	}
}

func TestXbergArgsTrimsWhitespace(t *testing.T) {
	x := &Xberg{OCR: true, OCRBackend: "  paddle-ocr\t", OCRLang: " ch "}
	got := argsOf(x)
	if !strings.Contains(got, "--ocr-backend paddle-ocr") || strings.Contains(got, "paddle-ocr\t") {
		t.Fatalf("backend not trimmed: %q", got)
	}
	if !strings.Contains(got, "--ocr-language ch") || strings.Contains(got, "--ocr-language  ch") {
		t.Fatalf("language not trimmed: %q", got)
	}
}

// With OCR disabled the backend must not be passed either: --ocr false plus an
// explicit backend is contradictory and xberg removes the OCR configuration.
func TestXbergArgsNoBackendWhenOCRDisabled(t *testing.T) {
	x := &Xberg{OCR: false, OCRBackend: "paddle-ocr", OCRLang: "ch"}
	got := argsOf(x)
	if strings.Contains(got, "--ocr-backend") || strings.Contains(got, "--ocr-language") {
		t.Fatalf("OCR disabled must not carry OCR configuration: %q", got)
	}
	if !strings.Contains(got, "--ocr false") {
		t.Fatalf("expected --ocr false, got %q", got)
	}
}

func TestValidateOCRBackend(t *testing.T) {
	for _, ok := range []string{"", "  ", "tesseract", "paddle-ocr", " tesseract "} {
		if err := ValidateOCRBackend(ok); err != nil {
			t.Fatalf("ValidateOCRBackend(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"paddleocr", "Tesseract", "en", "bogus", "paddle_ocr"} {
		if err := ValidateOCRBackend(bad); err == nil {
			t.Fatalf("ValidateOCRBackend(%q) = nil, want error", bad)
		}
	}
}

// The allow-list must stay a superset of what the pinned binary registers,
// otherwise a valid backend would be rejected at startup. The names below are
// the ones xberg 1.3.10 prints in its own "Invalid OCR backend" message.
func TestOCRBackendListCoversPinnedBinary(t *testing.T) {
	pinned := []string{
		"tesseract", "paddle-ocr", "sceptre", "vlm",
		"candle-trocr", "candle-paddleocr-vl", "candle-glm-ocr", "candle-deepseek-ocr",
	}
	have := map[string]bool{}
	for _, b := range OCRBackends {
		have[b] = true
	}
	for _, b := range pinned {
		if !have[b] {
			t.Fatalf("xberg registers %q but ParseDock would reject it", b)
		}
	}
	if len(OCRBackends) != len(pinned) {
		t.Fatalf("backend list drifted: have %d, binary lists %d", len(OCRBackends), len(pinned))
	}
}
