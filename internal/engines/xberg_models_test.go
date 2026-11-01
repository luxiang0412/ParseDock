package engines

import (
	"context"
	"errors"
	"testing"
)

func TestProbeModelsSkipsBackendsWithoutModels(t *testing.T) {
	for _, x := range []*Xberg{
		{Bin: "/nonexistent/xberg", OCR: false, OCRBackend: "paddle-ocr"},
		{Bin: "/nonexistent/xberg", OCR: true, OCRBackend: ""},
		{Bin: "/nonexistent/xberg", OCR: true, OCRBackend: "tesseract"},
	} {
		if checked, err := x.ProbeModels(context.Background()); checked || err != nil {
			t.Fatalf("OCR=%v backend=%q: checked=%v err=%v; want nothing checked", x.OCR, x.OCRBackend, checked, err)
		}
	}
}

func TestProbeModelsReportsFailure(t *testing.T) {
	x := &Xberg{Bin: "/nonexistent/xberg", OCR: true, OCRBackend: "paddle-ocr"}
	checked, err := x.ProbeModels(context.Background())
	if !checked || err == nil {
		t.Fatalf("checked=%v err=%v; want a failed check", checked, err)
	}
}

func TestProbeCause(t *testing.T) {
	stderr := "INFO noise\nError: Failed to extract input 'probe.png'\n\nCaused by:\n" +
		"    Plugin error in 'paddle-ocr': offline mode is enabled and 'v6/rec/small/model.onnx' is not available\n"
	if got := probeCause(&ExecResult{Stderr: []byte(stderr), ExitCode: 1}, errors.New("exit status 1")); got !=
		"Plugin error in 'paddle-ocr': offline mode is enabled and 'v6/rec/small/model.onnx' is not available" {
		t.Fatalf("cause = %q", got)
	}
	if got := probeCause(&ExecResult{Stderr: []byte("Error: boom\n"), ExitCode: 2}, nil); got != "Error: boom" {
		t.Fatalf("error line = %q", got)
	}
	if got := probeCause(nil, errors.New("fork failed")); got != "fork failed" {
		t.Fatalf("exec error = %q", got)
	}
}
