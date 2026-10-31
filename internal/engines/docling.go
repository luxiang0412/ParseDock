package engines

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// DoclingRS adapts the Rust Docling CLI (docling.rs). It runs the real PDF
// pipeline (layout + TableFormer + OCR) or, in fast mode, the text layer only.
type DoclingRS struct {
	Bin           string
	ModelRoot     string // directory containing .models/ (used as cmd.Dir)
	VersionS      string
	DeviceStr     string
	TextLayerOnly bool
	OCR           bool
	OCRLang       string // en | ch
	NoTables      bool
	MaxRSSMB      int64
	Sandbox       string
	ExtraEnv      []string
	Timeout       time.Duration
}

// Name implements Engine.
func (d *DoclingRS) Name() string { return "docling-rs" }

// Version implements Engine.
func (d *DoclingRS) Version() string {
	if d.VersionS != "" {
		return d.VersionS
	}
	return "unknown"
}

// Device implements Engine.
func (d *DoclingRS) Device() string {
	if d.DeviceStr != "" {
		return d.DeviceStr
	}
	return "cpu"
}

// Formats implements Engine.
func (d *DoclingRS) Formats() []string {
	return []string{"pdf", "docx", "xlsx", "pptx", "doc", "xls", "ppt",
		"md", "txt", "html", "png", "jpeg", "webp"}
}

// Modes implements Engine.
func (d *DoclingRS) Modes() []string { return []string{"accurate", "auto"} }

// Probe implements Engine.
func (d *DoclingRS) Probe(ctx context.Context) error {
	_, err := RunCLI(ctx, CLISpec{Bin: d.Bin}, d.ModelRoot, "--version")
	return err
}

// Parse implements Engine.
func (d *DoclingRS) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	timeout := d.Timeout
	if timeout == 0 {
		timeout = req.Timeout
	}
	if timeout == 0 {
		timeout = 15 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"--to", "json", "--no-progress"}
	if d.TextLayerOnly {
		args = append(args, "--text-layer-only")
	} else {
		if !d.OCR {
			args = append(args, "--no-ocr")
		} else if d.OCRLang != "" {
			args = append(args, "--ocr-lang", d.OCRLang)
		}
		if d.NoTables {
			args = append(args, "--no-table-former")
		}
		args = append(args, "--document-timeout", strconv.Itoa(int(timeout.Seconds())))
	}
	local := workFile(req.WorkDir, "engine-input."+req.Format)
	if err := copyFile(req.Path, local); err != nil {
		return nil, Errf(true, jobs.ErrInternal, "stage input: %v", err)
	}
	args = append(args, local)
	if req.Progress != nil {
		req.Progress(5, "engine_start")
	}
	spec := CLISpec{Bin: d.Bin, Env: d.ExtraEnv, Sandbox: d.Sandbox, MaxOutput: 256 << 20, MaxRSSMB: d.MaxRSSMB}
	res, err := RunCLI(ctx, spec, d.ModelRoot, args...)
	if res != nil && res.Truncated {
		// The parser's output exceeded the ceiling; a JSON prefix may even still parse, so
		// this must be an explicit rejection rather than a "malformed file".
		return nil, Errf(false, jobs.ErrExpandedTooLarge,
			"docling-rs produced more than the %d MiB result ceiling; this document needs windowed extraction", 256)
	}
	if err != nil && res != nil && !res.OOM && res.ExitCode == 0 && len(res.Stdout) > 0 && json.Valid(res.Stdout) {
		err = nil
	}
	if err != nil {
		return nil, cliFailure("docling-rs", timeout, res, err)
	}
	if req.Progress != nil {
		req.Progress(80, "engine_done")
	}
	doc := documentir.New()
	req.Report(doclingExecution(res.Stderr, map[string]string{"DOCLING_RS_EP": os.Getenv("DOCLING_RS_EP")}))

	doc.ParserEngine = d.Name()
	doc.ParserVersion = d.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = fileSize(req.Path)
	doc.Routing = documentir.Routing{
		RequestedMode: req.Mode, RequestedDevice: req.Device, ActualDevice: d.Device(),
		Engine: d.Name(), Reason: "docling-rs pipeline",
	}
	doc.Metrics.ParseMs = res.Elapsed.Milliseconds()
	if res.PeakRSSMB > 0 {
		rss := res.PeakRSSMB
		doc.Metrics.PeakRSSMB = &rss
	}
	if err := mapDocling(doc, res.Stdout, pageKind(req.Format)); err != nil {
		return nil, Errf(false, jobs.ErrMalformed, "%v", err)
	}
	if len(doc.Elements) == 0 && len(doc.Tables) == 0 {
		// docling-rs succeeds but skips oversized sheets (cell cap) and pages it
		// cannot handle; surface that instead of a misleading "no text".
		code, msg := ClassifyCLIError(string(res.Stderr))
		if code == jobs.ErrExpandedTooLarge {
			return nil, &Error{Code: code, Message: msg}
		}
		return nil, Errf(false, jobs.ErrMalformed,
			"docling-rs produced no content (size/cell limits or unsupported layout): %s", msg)
	}
	if d.TextLayerOnly {
		doc.AddWarning(documentir.WarnLayoutHeuristic,
			"text-layer-only mode: no layout model, tables and reading order are approximate", "info")
	}
	textLen := 0
	for _, el := range doc.Elements {
		textLen += len([]rune(el.Text))
	}
	if textLen < 20 && (req.Format == "pdf" || req.Format == "png" || req.Format == "jpeg" || req.Format == "webp") {
		doc.AddWarning(documentir.WarnNoTextLayer,
			"almost no text was extracted; the document may be a scan without a text layer", "warn")
	}
	doc.Metrics.InputBytes = fileSize(req.Path)
	return doc, nil
}

// DoclingServe adapts a running docling-serve instance over HTTP.
type DoclingServe struct {
	BaseURL   string
	VersionS  string
	DeviceStr string
	OCR       bool
	TableMode string // fast | accurate
	HTTP      *http.Client
}

// Name implements Engine.
func (d *DoclingServe) Name() string { return "docling-serve" }

// Version implements Engine.
func (d *DoclingServe) Version() string {
	if d.VersionS != "" {
		return d.VersionS
	}
	return "unknown"
}

// Device implements Engine.
func (d *DoclingServe) Device() string {
	if d.DeviceStr != "" {
		return d.DeviceStr
	}
	return "cpu"
}

// Formats implements Engine.
func (d *DoclingServe) Formats() []string {
	return []string{"pdf", "docx", "xlsx", "pptx", "md", "html", "png", "jpeg", "webp"}
}

// Modes implements Engine.
func (d *DoclingServe) Modes() []string { return []string{"accurate", "auto"} }

func (d *DoclingServe) client() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return &http.Client{Timeout: 20 * time.Minute}
}

// Probe implements Engine.
func (d *DoclingServe) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.BaseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := d.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("docling-serve health: %s", resp.Status)
	}
	return nil
}

// Parse implements Engine.
func (d *DoclingServe) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	// Evidence: docling-serve answers over HTTP and reports nothing about the
	// device it used. Even a CUDA-enabled image is unprovable from here.
	req.Report(UnknownEvidence("docling-serve:no-marker",
		"docling-serve does not report the execution device per request"))
	f, err := os.Open(req.Path)
	if err != nil {
		return nil, Errf(true, jobs.ErrInternal, "open input: %v", err)
	}
	defer f.Close()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("files", filepath.Base(req.Path))
	if err != nil {
		return nil, Errf(true, jobs.ErrInternal, "multipart: %v", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return nil, Errf(true, jobs.ErrInternal, "multipart copy: %v", err)
	}
	fields := map[string]string{
		"to_formats":        "json",
		"image_export_mode": "placeholder",
	}
	if d.OCR {
		fields["do_ocr"] = "true"
	} else {
		fields["do_ocr"] = "false"
	}
	if d.TableMode != "" {
		fields["table_mode"] = d.TableMode
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()

	if req.Progress != nil {
		req.Progress(10, "engine_start")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, d.BaseURL+"/v1/convert/file", &body)
	if err != nil {
		return nil, Errf(false, jobs.ErrInternal, "build request: %v", err)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	httpReq.Header.Set("Accept", "application/json")
	resp, err := d.client().Do(httpReq)
	if err != nil {
		return nil, Errf(true, jobs.ErrEngineUnavailable, "docling-serve request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 512<<20))
	if resp.StatusCode >= 300 {
		return nil, Errf(resp.StatusCode >= 500, jobs.ErrInternal, "docling-serve %s: %s",
			resp.Status, tail(string(raw), 300))
	}
	var out struct {
		Document struct {
			JSONContent json.RawMessage `json:"json_content"`
			MDContent   string          `json:"md_content"`
			Filename    string          `json:"filename"`
		} `json:"document"`
		Status         string  `json:"status"`
		ProcessingTime float64 `json:"processing_time"`
		Errors         []struct {
			ErrorMessage string `json:"error_message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, Errf(false, jobs.ErrInternal, "decode docling-serve response: %v", err)
	}
	doc := documentir.New()
	doc.ParserEngine = d.Name()
	doc.ParserVersion = d.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = fileSize(req.Path)
	doc.Routing = documentir.Routing{
		RequestedMode: req.Mode, RequestedDevice: req.Device, ActualDevice: d.Device(),
		Engine: d.Name(), Reason: "docling-serve HTTP pipeline",
	}
	doc.Metrics.ParseMs = int64(out.ProcessingTime * 1000)
	if len(out.Document.JSONContent) > 0 && string(out.Document.JSONContent) != "null" {
		if err := mapDocling(doc, out.Document.JSONContent, pageKind(req.Format)); err != nil {
			return nil, Errf(false, jobs.ErrMalformed, "%v", err)
		}
	} else if out.Document.MDContent != "" {
		LiftMarkdown(doc, out.Document.MDContent)
		doc.AddWarning(documentir.WarnPartialContent, "server returned Markdown only; no IR metadata", "warn")
	} else {
		return nil, Errf(false, jobs.ErrMalformed, "docling-serve returned no document")
	}
	if out.Status != "" && !strings.EqualFold(out.Status, "success") {
		for _, e := range out.Errors {
			doc.AddWarning(documentir.WarnPartialContent, e.ErrorMessage, "warn")
		}
	}
	return doc, nil
}
