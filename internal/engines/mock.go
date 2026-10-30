package engines

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// Mock is a controllable parser used to exercise the lifecycle end to end
// (ACK/TTL/lease/fencing) without loading real models. Its behaviour is driven
// by environment variables so tests can be scripted from the outside:
//
//	PARSEDOCK_MOCK_DELAY_MS  artificial work time (default 200)
//	PARSEDOCK_MOCK_FAIL      fail every attempt with this error code
//	PARSEDOCK_MOCK_RETRY     "1" makes the failure retryable
//	PARSEDOCK_MOCK_HANG      "1" ignores cancellation (tests SIGKILL recovery)
//	PARSEDOCK_MOCK_FORMATS   comma list (default "*")
type Mock struct {
	NameOverride string
	VersionStr   string
	DeviceStr    string
}

// Name implements Engine.
func (m *Mock) Name() string {
	if m.NameOverride != "" {
		return m.NameOverride
	}
	return "mock"
}

// Version implements Engine.
func (m *Mock) Version() string {
	if m.VersionStr != "" {
		return m.VersionStr
	}
	return "mock-1.0"
}

// Device implements Engine.
func (m *Mock) Device() string {
	if m.DeviceStr != "" {
		return m.DeviceStr
	}
	return "cpu"
}

// Formats implements Engine.
func (m *Mock) Formats() []string {
	if v := os.Getenv("PARSEDOCK_MOCK_FORMATS"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"*"}
}

// Modes implements Engine.
func (m *Mock) Modes() []string { return []string{"fast", "accurate", "auto"} }

// Probe implements Engine.
func (m *Mock) Probe(context.Context) error { return nil }

// Parse implements Engine.
func (m *Mock) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	delay := 200 * time.Millisecond
	if v := os.Getenv("PARSEDOCK_MOCK_DELAY_MS"); v != "" {
		if ms, err := strconv.Atoi(v); err == nil {
			delay = time.Duration(ms) * time.Millisecond
		}
	}
	if os.Getenv("PARSEDOCK_MOCK_HANG") == "1" {
		select {} // deliberately ignore ctx: the worker must be recovered by lease expiry
	}
	steps := 10
	for i := 0; i < steps; i++ {
		select {
		case <-ctx.Done():
			return nil, Errf(true, jobs.ErrTimeout, "mock parse cancelled")
		case <-time.After(delay / time.Duration(steps)):
		}
		if req.Progress != nil {
			req.Progress((i+1)*10, "mock_parse")
		}
	}
	if code := os.Getenv("PARSEDOCK_MOCK_FAIL"); code != "" {
		return nil, Errf(os.Getenv("PARSEDOCK_MOCK_RETRY") == "1", code, "mock failure for %s", req.Format)
	}
	doc := documentir.New()
	doc.ParserEngine = m.Name()
	doc.ParserVersion = m.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.PageKind = "none"
	doc.PageCount = 1
	doc.Metrics.Pages = 1
	doc.Elements = append(doc.Elements,
		documentir.Element{ID: "el_1", Type: documentir.ElTitle, Text: "Mock parse of " + req.Format, Level: 1},
		documentir.Element{ID: "el_2", Type: documentir.ElParagraph,
			Text: fmt.Sprintf("Deterministic placeholder output for job %s (device=%s).", req.JobID, req.Device)},
	)
	// Optional large payload: lets the lifecycle tests hold a download open
	// long enough to exercise ACK-while-streaming.
	if v := os.Getenv("PARSEDOCK_MOCK_BLOB_KB"); v != "" {
		if kb, err := strconv.Atoi(v); err == nil && kb > 0 {
			line := "0123456789abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOP\n"
			var b strings.Builder
			for b.Len() < kb*1024 {
				b.WriteString(line)
			}
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: "el_3", Type: documentir.ElParagraph, Text: b.String(),
			})
		}
	}
	return doc, nil
}
