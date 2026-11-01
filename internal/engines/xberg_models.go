package engines

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ocrProbePNG is a 160x40 synthetic image reading "OK 123"; one OCR pass over it loads every
// model the paddle-ocr backend needs.
//
//go:embed assets/ocr-probe.png
var ocrProbePNG []byte

// ocrProbeTimeout bounds the probe. A pass with local models takes well under a second;
// the budget covers a cold model load on a slow disk.
const ocrProbeTimeout = 2 * time.Minute

// ModelProber is implemented by engines that need model files beyond their binary. It
// reports checked=false when the current configuration needs no models.
type ModelProber interface {
	ProbeModels(ctx context.Context) (checked bool, err error)
}

// ProbeModels runs one OCR pass with HF_HUB_OFFLINE=1 when the paddle-ocr backend is
// selected. Without seeded models xberg would download them on the first scanned
// document, which fails on an offline network: this turns that into a startup error that
// names the missing file. Tesseract needs no model files beyond tessdata, so nothing is
// checked for it.
func (x *Xberg) ProbeModels(ctx context.Context) (bool, error) {
	if !x.OCR || strings.TrimSpace(x.OCRBackend) != "paddle-ocr" {
		return false, nil
	}
	dir, err := os.MkdirTemp("", "parsedock-ocr-probe-")
	if err != nil {
		return true, fmt.Errorf("paddle-ocr model probe: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	img := filepath.Join(dir, "probe.png")
	if err := os.WriteFile(img, ocrProbePNG, 0o600); err != nil {
		return true, fmt.Errorf("paddle-ocr model probe: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, ocrProbeTimeout)
	defer cancel()
	spec := x.spec()
	spec.Env = append(spec.Env, "HF_HUB_OFFLINE=1")
	res, err := RunCLI(ctx, spec, dir, append(x.extractArgs(), img)...)
	if err == nil && res != nil && res.ExitCode == 0 {
		return true, nil
	}
	return true, fmt.Errorf("paddle-ocr models are not available offline (HF_HUB_CACHE=%q): %s; "+
		"seed them with `xberg cache seed-classic-ocr --cache-dir \"$HF_HUB_CACHE\"` (docs/DEPLOYMENT.md)",
		os.Getenv("HF_HUB_CACHE"), probeCause(res, err))
}

// probeCause picks the most specific line of xberg's stderr: the last line after
// "Caused by:" (the innermost cause), else the "Error:" line, else the exec error.
func probeCause(res *ExecResult, err error) string {
	if res != nil {
		var cause, errLine string
		inCause := false
		for _, l := range strings.Split(string(res.Stderr), "\n") {
			l = strings.TrimSpace(l)
			switch {
			case l == "Caused by:":
				inCause = true
			case inCause && l != "":
				cause = l
			case strings.HasPrefix(l, "Error:"):
				errLine = l
			}
		}
		if cause != "" {
			return cause
		}
		if errLine != "" {
			return errLine
		}
		if res.ExitCode != 0 {
			return fmt.Sprintf("xberg exit %d", res.ExitCode)
		}
	}
	if err != nil {
		return err.Error()
	}
	return "unknown failure"
}
