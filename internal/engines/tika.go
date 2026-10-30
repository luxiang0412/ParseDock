package engines

import (
	"context"
	"encoding/json"
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
	return []string{"pdf", "docx", "xlsx", "pptx", "doc", "xls", "ppt",
		"csv", "tsv", "txt", "md", "png", "jpeg", "webp", "html"}
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
//
// Two Tika behaviours matter for IR honesty (F08):
//   - For PDFs it returns plain text with *no* page boundaries (verified: 0 form
//     feeds on a 3-page file), so page numbers are only assigned when form feeds
//     are actually present. A missing boundary becomes a warning, never page 1
//     for the whole document.
//   - For OOXML/HTML it returns markdown (headings, pipe tables), which is lifted
//     into the IR instead of being flattened into paragraphs.
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
	isPDF := req.Format == "pdf"
	doc.PageKind = pageKind(req.Format)

	if !strings.Contains(text, "\f") {
		// No page boundaries from Tika: lift the whole body, keep provenance
		// honest. Tika's OOXML output is markdown, so use the markdown lifter.
		if looksLikeMarkdown(text) {
			LiftMarkdown(doc, text)
		} else {
			liftPlainTextWithKind(doc, text, doc.PageKind)
		}
		if isPDF {
			doc.AddWarning(documentir.WarnNoPageBoundaries,
				"Tika returned no page boundaries for this PDF; elements carry no page number", "warn")
			if pages := t.pdfPageCount(ctx, req.Path); pages > 0 {
				doc.PageCount = pages
			}
		} else {
			doc.PageCount = 1
		}
	} else {
		pages := strings.Split(text, "\f")
		for i, page := range pages {
			page = strings.TrimSpace(page)
			if page == "" {
				continue
			}
			scratch := documentir.New()
			if looksLikeMarkdown(page) {
				LiftMarkdown(scratch, page)
			} else {
				liftPlainTextWithKind(scratch, page, doc.PageKind)
			}
			for _, el := range scratch.Elements {
				el.Loc = &documentir.SourceLocation{Kind: doc.PageKind, Page: i + 1}
				doc.Elements = append(doc.Elements, el)
			}
			for _, tbl := range scratch.Tables {
				tbl.Loc = &documentir.SourceLocation{Kind: doc.PageKind, Page: i + 1}
				tbl.ID = fmt.Sprintf("tbl_%d", len(doc.Tables)+1)
				doc.Tables = append(doc.Tables, tbl)
			}
		}
		doc.PageCount = len(pages)
	}
	doc.Metrics.Pages = doc.PageCount
	if !doc.HasBBox() {
		doc.AddWarning(documentir.WarnNoCoordinates,
			"Tika returns no bounding boxes; table structure is only available for OOXML/HTML inputs", "info")
	}
	if len(doc.Elements) == 0 {
		return nil, Errf(false, jobs.ErrMalformed, "tika produced no text")
	}
	return doc, nil
}

// pdfPageCount asks Tika's metadata endpoint for the authoritative page count.
// It is best effort: an error just means the count stays unknown.
func (t *Tika) pdfPageCount(ctx context.Context, path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, t.BaseURL+"/rmeta/text", f)
	if err != nil {
		return 0
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := t.client().Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0
	}
	var meta []map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&meta); err != nil || len(meta) == 0 {
		return 0
	}
	for _, key := range []string{"xmpTPg:NPages", "meta:page-count", "pdf:page-count"} {
		if v, ok := meta[0][key]; ok {
			if n, ok := toInt(v); ok && n > 0 {
				return n
			}
		}
	}
	return 0
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case string:
		var out int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &out); err == nil {
			return out, true
		}
	}
	return 0, false
}

// liftPlainTextWithKind is liftPlainText with an explicit page kind.
func liftPlainTextWithKind(doc *documentir.Document, text, kind string) {
	before := len(doc.Elements)
	liftPlainText(doc, text)
	for i := before; i < len(doc.Elements); i++ {
		if doc.Elements[i].Loc != nil {
			doc.Elements[i].Loc.Kind = kind
		}
	}
	doc.PageCount = 0 // unknown: no page boundaries were provided
}
