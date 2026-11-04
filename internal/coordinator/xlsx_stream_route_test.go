package coordinator

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/jobs"
	"parsedock.dev/parsedock/internal/storage"
)

// xlsxBody builds a structurally valid workbook whose sheet XML expands to about
// sheetBytes (stored, not deflated, so the bomb-ratio check stays out of the way).
func xlsxBody(t *testing.T, sheetBytes int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add("[Content_Types].xml", `<Types/>`)
	add("xl/workbook.xml", `<workbook><sheets><sheet name="S" sheetId="1"/></sheets></workbook>`)
	add("xl/worksheets/sheet1.xml", `<worksheet><sheetData>`+strings.Repeat(" ", sheetBytes)+`</sheetData></worksheet>`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func xlsxStreamService(t *testing.T, withStream bool) *Service {
	return testService(t, func(c *Config) {
		c.Engines = []EngineSpec{
			{Name: "xberg", Device: "cpu", Priority: 50, Formats: []string{"pdf", "xlsx"}, Modes: []string{"fast", "auto"}},
			{Name: "docling-rs", Device: "cpu", Priority: 40, Formats: []string{"pdf", "xlsx"}, Modes: []string{"accurate"}},
		}
		if withStream {
			c.Engines = append(c.Engines, EngineSpec{Name: xlsxStreamEngine, Device: "cpu", Priority: 0,
				Formats: []string{"xlsx"}, Modes: []string{"fast", "accurate", "auto"}})
		}
		lim := storage.DefaultLimits()
		lim.MaxZipUncompressed = 256 << 10
		lim.MaxXLSXStreamUncompressed = 4 << 20
		c.Limits = lim
		c.XLSXStreamMinExpanded = 128 << 10
	})
}

func TestXLSXStreamRouting(t *testing.T) {
	s := xlsxStreamService(t, true)
	cases := []struct {
		name       string
		size       int
		mode, dev  string
		wantEngine string
	}{
		{"small workbook keeps the mode's engine", 1 << 10, jobs.ModeAuto, jobs.DeviceAuto, "xberg"},
		{"small accurate workbook", 1 << 10, jobs.ModeAccurate, jobs.DeviceAuto, "docling-rs"},
		{"large workbook, any mode", 200 << 10, jobs.ModeAccurate, jobs.DeviceAuto, xlsxStreamEngine},
		{"large workbook, cpu", 200 << 10, jobs.ModeFast, jobs.DeviceCPU, xlsxStreamEngine},
		// Above the ordinary cap: admitted only because the streaming engine exists.
		{"beyond the ordinary expanded cap", 1 << 20, jobs.ModeAuto, jobs.DeviceAuto, xlsxStreamEngine},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job := create(t, s, "book.xlsx", xlsxBody(t, c.size), CreateParams{Mode: c.mode, Device: c.dev})
			if job.EngineHint != c.wantEngine {
				t.Fatalf("engine = %s (%s), want %s", job.EngineHint, job.RoutingReason, c.wantEngine)
			}
			if c.wantEngine == xlsxStreamEngine && !strings.Contains(job.RoutingReason, "expands to") {
				t.Fatalf("routing reason must explain the size rule: %s", job.RoutingReason)
			}
		})
	}

	// Beyond the streaming cap the workbook is still refused as a size problem.
	_, _, err := s.CreateJob(context.Background(), CreateParams{OwnerID: "owner-1", InputName: "huge.xlsx",
		Body: bytes.NewReader(xlsxBody(t, 5<<20))})
	if AsError(err) == nil || AsError(err).Code != jobs.ErrExpandedTooLarge {
		t.Fatalf("err = %v, want expanded_too_large beyond the streaming cap", err)
	}
	// gpu_required never lands on a CPU engine.
	_, _, err = s.CreateJob(context.Background(), CreateParams{OwnerID: "owner-1", InputName: "gpu.xlsx",
		Device: jobs.DeviceGPURequired, Body: bytes.NewReader(xlsxBody(t, 200<<10))})
	if AsError(err) == nil || AsError(err).Code != jobs.ErrGPUUnavailable {
		t.Fatalf("err = %v, want gpu_unavailable", err)
	}
}

func TestXLSXStreamCapNeedsTheEngine(t *testing.T) {
	s := xlsxStreamService(t, false)
	// Without the streaming engine the ordinary cap applies and the size rule is inert.
	_, _, err := s.CreateJob(context.Background(), CreateParams{OwnerID: "owner-1", InputName: "big.xlsx",
		Body: bytes.NewReader(xlsxBody(t, 1<<20))})
	if AsError(err) == nil || AsError(err).Code != jobs.ErrExpandedTooLarge {
		t.Fatalf("err = %v, want expanded_too_large", err)
	}
	job := create(t, s, "mid.xlsx", xlsxBody(t, 200<<10), CreateParams{Mode: jobs.ModeAuto})
	if job.EngineHint != "xberg" {
		t.Fatalf("engine = %s, want xberg when xlsx-stream is not configured", job.EngineHint)
	}
}
