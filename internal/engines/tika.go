package engines

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// Tika adapts an Apache Tika server. Tika is a broad-coverage fallback: it
// returns text reliably for many formats but not layout, coordinates or
// confidence, so those stay empty in the IR and a warning says so.
type Tika struct {
	BaseURL   string
	VersionS  string
	DeviceStr string
	OCR       bool
	HTTP      *http.Client
}

// Name implements Engine.
func (t *Tika) Name() string { return "tika" }

// Version implements Engine.
func (t *Tika) Version() string {
	if t.VersionS != "" {
		return t.VersionS
	}
	return "unknown"
}

// Device implements Engine.
func (t *Tika) Device() string {
	if t.DeviceStr != "" {
		return t.DeviceStr
	}
	return "cpu"
}

// Formats implements Engine.
func (t *Tika) Formats() []string {
	return []string{"pdf", "docx", "xlsx", "pptx", "csv", "tsv", "txt", "md", "png", "jpeg", "webp", "html"}
}

// Modes implements Engine.
func (t *Tika) Modes() []string { return []string{"fast", "auto"} }

func (t *Tika) client() *http.Client {
	if t.HTTP != nil {
		return t.HTTP
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

// Probe implements Engine.
func (t *Tika) Probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.BaseURL+"/version", nil)
	if err != nil {
		return err
	}
	resp, err := t.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("tika /version: %s", resp.Status)
	}
	return nil
}

// Parse implements Engine.
func (t *Tika) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	f, err := os.Open(req.Path)
	if err != nil {
		return nil, Errf(true, jobs.ErrInternal, "open input: %v", err)
	}
	defer f.Close()
	st, _ := f.Stat()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, t.BaseURL+"/tika", f)
	if err != nil {
		return nil, Errf(false, jobs.ErrInternal, "build request: %v", err)
	}
	if st != nil {
		httpReq.ContentLength = st.Size()
	}
	httpReq.Header.Set("Accept", "text/plain")
	httpReq.Header.Set("Content-Type", "application/octet-stream")
	if t.OCR {
		httpReq.Header.Set("X-Tika-PDFExtractInlineImages", "true")
		httpReq.Header.Set("X-Tika-OCRLanguage", "eng+chi_sim")
	}
	if req.Progress != nil {
		req.Progress(20, "engine_start")
	}
	start := time.Now()
	resp, err := t.client().Do(httpReq)
	if err != nil {
		return nil, Errf(true, jobs.ErrEngineUnavailable, "tika request: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode >= 300 {
		return nil, Errf(resp.StatusCode >= 500, jobs.ErrMalformed, "tika %s: %s", resp.Status, tail(string(raw), 300))
	}
	doc := documentir.New()
	doc.ParserEngine = t.Name()
	doc.ParserVersion = t.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = fileSize(req.Path)
	doc.Routing = documentir.Routing{
		RequestedMode: req.Mode, RequestedDevice: req.Device, ActualDevice: t.Device(),
		Engine: t.Name(), Reason: "tika text extraction fallback",
	}
	doc.Metrics.ParseMs = time.Since(start).Milliseconds()

	text := string(raw)
	// Tika separates PDF pages with form feeds.
	pages := strings.Split(text, "\f")
	isPDF := req.Format == "pdf"
	for i, page := range pages {
		page = strings.TrimSpace(page)
		if page == "" {
			continue
		}
		blocks := splitBlocks(page)
		if len(blocks) == 0 {
			blocks = []string{page}
		}
		for _, b := range blocks {
			el := documentir.Element{
				ID:   fmt.Sprintf("el_%d", len(doc.Elements)+1),
				Type: documentir.ElParagraph,
				Text: b,
			}
			if isPDF {
				el.Loc = &documentir.SourceLocation{Kind: "pdf", Page: i + 1}
			} else {
				el.Loc = &documentir.SourceLocation{Kind: "none"}
			}
			doc.Elements = append(doc.Elements, el)
		}
	}
	doc.PageCount = len(pages)
	if !isPDF {
		doc.PageCount = 1
	}
	doc.PageKind = pageKind(req.Format)
	doc.Metrics.Pages = doc.PageCount
	doc.AddWarning(documentir.WarnNoCoordinates,
		"Tika returns text only: no bounding boxes, headings or table structure", "info")
	if len(doc.Elements) == 0 {
		return nil, Errf(false, jobs.ErrMalformed, "tika produced no text")
	}
	return doc, nil
}
