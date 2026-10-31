package engines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// Xberg adapts the Xberg CLI (Rust core). It is invoked as a subprocess and
// communicates exclusively through the Document IR.
type Xberg struct {
	Bin        string
	LibDir     string // directory holding the bundled .so files
	VersionS   string
	OCR        bool
	OCRBackend string
	OCRLang    string
	// Cache enables xberg's on-disk extraction cache. Disabled by default: a
	// worker must not persist document text between jobs.
	Cache    bool
	MaxRSSMB int64
	Sandbox  string
	ExtraEnv []string
	Timeout  time.Duration
}

// Name implements Engine.
func (x *Xberg) Name() string { return "xberg" }

// Version implements Engine.
func (x *Xberg) Version() string {
	if x.VersionS != "" {
		return x.VersionS
	}
	return "unknown"
}

// Device implements Engine.
func (x *Xberg) Device() string { return "cpu" }

// Formats implements Engine.
func (x *Xberg) Formats() []string {
	return []string{"pdf", "docx", "xlsx", "pptx", "doc", "xls", "ppt",
		"csv", "tsv", "txt", "md", "png", "jpeg", "webp"}
}

// Modes implements Engine.
func (x *Xberg) Modes() []string { return []string{"fast", "auto"} }

// Probe implements Engine.
func (x *Xberg) Probe(ctx context.Context) error {
	_, err := RunCLI(ctx, x.spec(), os.TempDir(), "--version")
	return err
}

func (x *Xberg) spec() CLISpec {
	env := append([]string{}, x.ExtraEnv...)
	if x.LibDir != "" {
		env = append(env, "LD_LIBRARY_PATH="+x.LibDir)
	}
	return CLISpec{Bin: x.Bin, Env: env, Sandbox: x.Sandbox, MaxOutput: 256 << 20, MaxRSSMB: x.MaxRSSMB}
}

type xbergOutput struct {
	ExtractionTimeMs float64 `json:"extraction_time_ms"`
	PeakMemoryBytes  float64 `json:"peak_memory_bytes"`
	Result           struct {
		Content string `json:"content"`
		Pages   []struct {
			PageNumber int    `json:"page_number"`
			Content    string `json:"content"`
		} `json:"pages"`
		Tables []struct {
			Cells       [][]string `json:"cells"`
			Markdown    string     `json:"markdown"`
			PageNumber  int        `json:"page_number"`
			BoundingBox *struct {
				X0 float64 `json:"x0"`
				Y0 float64 `json:"y0"`
				X1 float64 `json:"x1"`
				Y1 float64 `json:"y1"`
			} `json:"bounding_box"`
		} `json:"tables"`
		Metadata struct {
			Title  string `json:"title"`
			Format struct {
				PageCount int  `json:"page_count"`
				Encrypted bool `json:"is_encrypted"`
			} `json:"format"`
			OCRUsed          bool   `json:"ocr_used"`
			ExtractionMethod string `json:"extraction_method"`
		} `json:"metadata"`
		Counts struct {
			Pages  int `json:"pages"`
			Tables int `json:"tables"`
			Images int `json:"images"`
		} `json:"counts"`
		QualityScore         float64 `json:"quality_score"`
		ExtractionConfidence struct {
			TextCoverage *float64 `json:"text_coverage"`
			OCRAggregate *float64 `json:"ocr_aggregate"`
			Combined     *float64 `json:"combined"`
		} `json:"extraction_confidence"`
		ExtractionMethod string `json:"extraction_method"`
	} `json:"result"`
}

var styleAnnoRe = regexp.MustCompile(`\(style_name:\s*([^)]+)\)\s*$`)

// Parse implements Engine.
func (x *Xberg) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	timeout := x.Timeout
	if timeout == 0 {
		timeout = req.Timeout
	}
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"extract", "--no-config-discovery", "--format", "json"}
	if !x.Cache {
		args = append(args, "--no-cache", "true")
	}
	if x.OCR {
		args = append(args, "--ocr", "true")
		if strings.TrimSpace(x.OCRBackend) != "" {
			args = append(args, "--ocr-backend", x.OCRBackend)
		}
		if strings.TrimSpace(x.OCRLang) != "" {
			args = append(args, "--ocr-language", x.OCRLang)
		}
	} else {
		args = append(args, "--ocr", "false")
	}
	// Copy the input into the worker's scratch dir and let the engine write
	// only there; the original published input stays read-only.
	local := workFile(req.WorkDir, "engine-input."+req.Format)
	if err := copyFile(req.Path, local); err != nil {
		return nil, Errf(true, jobs.ErrInternal, "stage input: %v", err)
	}
	args = append(args, local)
	if req.Progress != nil {
		req.Progress(5, "engine_start")
	}
	res, err := RunCLI(ctx, x.spec(), req.WorkDir, args...)
	if res != nil && res.Truncated {
		// The parser's output exceeded the ceiling; a JSON prefix may even still parse, so
		// this must be an explicit rejection rather than a "malformed file".
		return nil, fmt.Errorf("%w: %d MiB ceiling", ErrOutputTooLarge, 512)
	}
	if err != nil && res != nil && !res.OOM && res.ExitCode == 0 && len(res.Stdout) > 0 && json.Valid(res.Stdout) {
		// exec.Cmd can report context.Canceled after a successful run when the
		// context is cancelled while Wait() settles; if the engine produced valid
		// output, trust the output instead of failing the job.
		err = nil
	}
	if err != nil {
		if res != nil && res.TimedOut {
			return nil, Errf(true, jobs.ErrTimeout, "xberg timed out after %s", timeout)
		}
		if errors.Is(err, ErrOOM) {
			return nil, Errf(true, jobs.ErrEngineOOM,
				"xberg exceeded its memory ceiling while parsing this document")
		}
		if errors.Is(err, ErrSandboxUnavailable) {
			return nil, Errf(false, jobs.ErrEngineUnavailable, "engine sandbox requested but unavailable: %v", err)
		}
		code, msg := jobs.ErrMalformed, "xberg failed"
		if res != nil {
			code, msg = ClassifyCLIError(string(res.Stderr))
			msg = fmt.Sprintf("xberg exit %d: %s", res.ExitCode, msg)
		}
		return nil, &Error{Code: code, Message: msg}
	}
	if req.Progress != nil {
		req.Progress(80, "engine_done")
	}
	var out xbergOutput
	if uerr := json.Unmarshal(res.Stdout, &out); uerr != nil {
		// The engine can exit 0 while aborting mid-stream (e.g. it hit its archive
		// limit after emitting part of the JSON). Prefer the stderr explanation.
		code, msg := ClassifyCLIError(string(res.Stderr))
		if code == jobs.ErrMalformed && strings.TrimSpace(msg) == "" {
			return nil, Errf(false, jobs.ErrInternal, "xberg returned invalid JSON: %v", uerr)
		}
		return nil, &Error{Code: code, Message: fmt.Sprintf("xberg produced %d bytes of unusable output: %s", len(res.Stdout), msg)}
	}
	doc := documentir.New()
	doc.ParserEngine = x.Name()
	doc.ParserVersion = x.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = fileSize(req.Path)
	doc.Routing.Reason = "xberg native/ocr pipeline"
	doc.Routing.Engine = x.Name()
	doc.Routing.ActualDevice = x.Device()
	doc.Routing.RequestedMode = req.Mode
	doc.Routing.RequestedDevice = req.Device
	doc.Metrics.ParseMs = res.Elapsed.Milliseconds()
	rss := res.PeakRSSMB
	if rss == 0 && out.PeakMemoryBytes > 0 {
		rss = int64(out.PeakMemoryBytes) / (1 << 20)
	}
	if rss > 0 {
		doc.Metrics.PeakRSSMB = &rss
	}

	method := firstNonEmptyStr(out.Result.ExtractionMethod, out.Result.Metadata.ExtractionMethod)
	if out.Result.Metadata.OCRUsed || method == "ocr" {
		doc.AddWarning(documentir.WarnOCRLowConfidence,
			"OCR was used; text accuracy is lower than the native text layer", "info")
	}
	if out.Result.QualityScore > 0 && out.Result.QualityScore < 0.8 {
		doc.AddWarning(documentir.WarnDegraded,
			fmt.Sprintf("engine quality score %.2f is below 0.80", out.Result.QualityScore), "warn")
	}
	if out.Result.Metadata.Format.Encrypted {
		doc.AddWarning(documentir.WarnEncrypted, "document reports encryption", "warn")
	}

	switch {
	case len(out.Result.Pages) > 0:
		x.mapPages(doc, &out, req)
	case out.Result.Content != "":
		if looksLikeMarkdown(out.Result.Content) {
			LiftMarkdown(doc, out.Result.Content)
		} else {
			liftPlainText(doc, out.Result.Content)
		}
		x.attachTables(doc, &out, 0, true)
	default:
		return nil, Errf(false, jobs.ErrMalformed, "engine produced no content")
	}

	doc.PageCount = out.Result.Metadata.Format.PageCount
	if doc.PageCount == 0 {
		doc.PageCount = out.Result.Counts.Pages
	}
	if len(out.Result.Pages) > 0 {
		doc.PageKind = "pdf"
		if req.Format == "pptx" {
			doc.PageKind = "slide"
		}
		if req.Format == "xlsx" {
			doc.PageKind = "sheet"
		}
	}
	doc.Metrics.Pages = doc.PageCount
	doc.Metrics.InputBytes = fileSize(req.Path)
	if req.Format == "xlsx" {
		// Xberg reports sheet index but not sheet names; do not fabricate them.
		doc.AddWarning("sheet_name_unavailable",
			"engine reports sheet index only; sheet names require a structured engine", "info")
	}
	return doc, nil
}

func (x *Xberg) mapPages(doc *documentir.Document, out *xbergOutput, req Request) {
	for _, page := range out.Result.Pages {
		content := strings.TrimSpace(page.Content)
		if content == "" {
			continue
		}
		if looksLikeMarkdown(content) {
			scratch := documentir.New()
			LiftMarkdown(scratch, content)
			remapTables(scratch, len(doc.Tables), len(doc.Elements))
			for i := range scratch.Elements {
				el := scratch.Elements[i]
				el.Loc = &documentir.SourceLocation{Kind: pageKind(req.Format), Page: page.PageNumber}
				doc.Elements = append(doc.Elements, el)
			}
			for _, t := range scratch.Tables {
				t.Loc = &documentir.SourceLocation{Kind: pageKind(req.Format), Page: page.PageNumber}
				doc.Tables = append(doc.Tables, t)
			}
			continue
		}
		blocks := splitBlocks(content)
		for _, b := range blocks {
			el := documentir.Element{
				Type: documentir.ElParagraph, Text: b,
				Loc: &documentir.SourceLocation{Kind: pageKind(req.Format), Page: page.PageNumber},
			}
			if txt, style := splitStyleAnnotation(b); style != "" {
				el.Text = txt
				el.Level, el.Type = styleLevel(style)
			}
			el.ID = fmt.Sprintf("el_%d", len(doc.Elements)+1)
			doc.Elements = append(doc.Elements, el)
		}
		x.attachTables(doc, out, page.PageNumber, false)
	}
}

// attachTables appends engine-detected tables with real bounding boxes.
func (x *Xberg) attachTables(doc *documentir.Document, out *xbergOutput, page int, all bool) {
	existing := map[string]bool{}
	for _, t := range doc.Tables {
		existing[t.ID] = true
	}
	for _, t := range out.Result.Tables {
		if !all && t.PageNumber != page {
			continue
		}
		if len(t.Cells) == 0 {
			continue
		}
		id := fmt.Sprintf("tbl_%d", len(doc.Tables)+1)
		tbl := documentir.Table{ID: id, Rows: len(t.Cells), Cols: 0, HeaderRows: 1}
		for ri, row := range t.Cells {
			if len(row) > tbl.Cols {
				tbl.Cols = len(row)
			}
			for ci, val := range row {
				tbl.Cells = append(tbl.Cells, documentir.Cell{
					Row: ri, Col: ci, RowSpan: 1, ColSpan: 1, Text: val, DataType: guessType(val),
				})
			}
		}
		loc := &documentir.SourceLocation{Kind: pageKindOfPage(page), Page: t.PageNumber}
		if loc.Page == 0 {
			loc.Page = page
		}
		if t.BoundingBox != nil {
			loc.BBox = &documentir.BBox{Coord: "pdf_user_space_v1",
				X0: t.BoundingBox.X0, Y0: t.BoundingBox.Y0, X1: t.BoundingBox.X1, Y1: t.BoundingBox.Y1}
		}
		tbl.Loc = loc
		doc.Tables = append(doc.Tables, tbl)
		doc.Elements = append(doc.Elements, documentir.Element{
			ID: fmt.Sprintf("el_%d", len(doc.Elements)+1), Type: documentir.ElTable, TableID: id,
			Text: fmt.Sprintf("%d rows x %d cols", tbl.Rows, tbl.Cols), Loc: loc,
		})
	}
}

func pageKind(format string) string {
	switch format {
	case "pdf":
		return "pdf"
	case "xlsx", "csv", "tsv":
		return "sheet"
	case "pptx":
		return "slide"
	default:
		return "none"
	}
}

func pageKindOfPage(int) string { return "pdf" }

func splitStyleAnnotation(s string) (string, string) {
	m := styleAnnoRe.FindStringSubmatch(s)
	if m == nil {
		return s, ""
	}
	return strings.TrimSpace(styleAnnoRe.ReplaceAllString(s, "")), strings.ToLower(strings.TrimSpace(m[1]))
}

func styleLevel(style string) (int, string) {
	switch {
	case style == "title":
		return 1, documentir.ElTitle
	case strings.HasPrefix(style, "heading"):
		var lvl int
		if _, err := fmt.Sscanf(style, "heading %d", &lvl); err == nil && lvl >= 1 && lvl <= 6 {
			return lvl, documentir.ElSection
		}
		return 2, documentir.ElSection
	case style == "subtitle":
		return 2, documentir.ElSection
	}
	return 0, documentir.ElParagraph
}

func looksLikeMarkdown(s string) bool {
	return strings.Contains(s, "\n## ") || strings.HasPrefix(s, "## ") ||
		strings.Contains(s, "\n# ") || strings.Contains(s, "| --- |") ||
		strings.Contains(s, "| --- |") || strings.Contains(s, "|---|")
}

func splitBlocks(s string) []string {
	parts := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n\n")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := out.ReadFrom(in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
