package engines

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// OCRBackends is the set of OCR backends xberg 1.3.10 registers. The CLI
// rejects anything else ("Invalid OCR backend '<x>'. Valid backends: …"), so
// this list exists to fail at startup instead of on the first scanned document.
var OCRBackends = []string{
	"tesseract", "paddle-ocr", "sceptre", "vlm",
	"candle-trocr", "candle-paddleocr-vl", "candle-glm-ocr", "candle-deepseek-ocr",
}

// ValidateOCRBackend rejects a backend name this xberg build cannot run.
func ValidateOCRBackend(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil // unset: xberg's own default (tesseract) applies
	}
	for _, b := range OCRBackends {
		if b == name {
			return nil
		}
	}
	return fmt.Errorf("unknown xberg OCR backend %q (valid: %s)", name, strings.Join(OCRBackends, ", "))
}

// extractArgs is the argv the worker passes to `xberg extract`, without the
// input path. Split out from Parse so the OCR flag plumbing is testable without
// running the engine.
func (x *Xberg) extractArgs() []string {
	// --content-format markdown: xberg's default content is plain text, which loses
	// headings, lists, quotes, inline formatting and table positions. Per-page content
	// and the tables array are unaffected by the switch.
	args := []string{"extract", "--no-config-discovery", "--format", "json", "--content-format", "markdown"}
	if !x.Cache {
		args = append(args, "--no-cache", "true")
	}
	if x.OCR {
		args = append(args, "--ocr", "true")
		if b := strings.TrimSpace(x.OCRBackend); b != "" {
			args = append(args, "--ocr-backend", b)
		}
		// xberg normalises both spellings: ISO 639-3 ("eng", "chi_sim") and the
		// PaddleOCR short codes ("en", "ch") select the same recognition family.
		if l := strings.TrimSpace(x.OCRLang); l != "" {
			args = append(args, "--ocr-language", l)
		}
	} else {
		args = append(args, "--ocr", "false")
	}
	return args
}

// xbergPage is one entry of result.pages: a PDF page, a slide or a sheet.
type xbergPage struct {
	PageNumber int    `json:"page_number"`
	Content    string `json:"content"`
	SheetName  string `json:"sheet_name"` // spreadsheets only
	// Reported for tesseract; null score / 0 words for paddle-ocr, which
	// is "unknown", not "low".
	OCRConfidence struct {
		Score     *float64 `json:"score"`
		WordCount int      `json:"word_count"`
		Backend   string   `json:"backend"`
	} `json:"ocr_confidence"`
}

type xbergOutput struct {
	ExtractionTimeMs float64 `json:"extraction_time_ms"`
	PeakMemoryBytes  float64 `json:"peak_memory_bytes"`
	Result           struct {
		Content string      `json:"content"`
		Pages   []xbergPage `json:"pages"`
		Tables  []struct {
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
			OCRUsed           bool    `json:"ocr_used"`
			ExtractionMethod  string  `json:"extraction_method"`
			ScannedConfidence float64 `json:"scanned_confidence"`
			ScannedPages      []int   `json:"scanned_pages"`
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

	args := x.extractArgs()
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
		return nil, Errf(false, jobs.ErrExpandedTooLarge,
			"xberg produced more than the %d MiB result ceiling; this document needs windowed extraction", 256)
	}
	if err != nil && res != nil && !res.OOM && res.ExitCode == 0 && len(res.Stdout) > 0 && json.Valid(res.Stdout) {
		// exec.Cmd can report context.Canceled after a successful run when the
		// context is cancelled while Wait() settles; if the engine produced valid
		// output, trust the output instead of failing the job.
		err = nil
	}
	if err != nil {
		return nil, cliFailure("xberg", timeout, res, err)
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
	// Evidence: this adapter never passes --acceleration, and the pinned build is
	// CPU-only (it answers "compiled without CUDA support" if asked for one).
	req.Report(xbergExecution(res.Stderr))

	doc := documentir.New()
	doc.ParserEngine = x.Name()
	doc.ParserVersion = x.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = fileSize(req.Path)
	doc.Routing.Reason = "xberg native/ocr pipeline"
	if x.OCR && strings.TrimSpace(x.OCRBackend) != "" {
		// Which OCR backend produced this text is part of the result's audit
		// trail: the same document parsed by tesseract and by paddle-ocr is not
		// the same result, and only the reason field records that today.
		doc.Routing.Reason = fmt.Sprintf("xberg native/ocr pipeline (ocr_backend=%s)", strings.TrimSpace(x.OCRBackend))
	}
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
	ocrUsed := out.Result.Metadata.OCRUsed || method == "ocr"
	if out.Result.QualityScore > 0 && out.Result.QualityScore < 0.8 {
		doc.AddWarning(documentir.WarnDegraded,
			fmt.Sprintf("engine quality score %.2f is below 0.80", out.Result.QualityScore), "warn")
	}
	if out.Result.Metadata.Format.Encrypted {
		doc.AddWarning(documentir.WarnEncrypted, "document reports encryption", "warn")
	}

	switch {
	case len(out.Result.Pages) > 0:
		x.mapPages(doc, &out, req, ocrUsed)
	case out.Result.Content != "":
		LiftMarkdown(doc, out.Result.Content)
		x.attachTables(doc, &out, req.Format, 0, true)
	default:
		return nil, Errf(false, jobs.ErrMalformed, "engine produced no content")
	}

	x.applyOCRQuality(doc, &out, ocrUsed, req.Format)

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

// mapPages lifts each page's Markdown into the IR. Elements keep the page number;
// tables keep the engine's bounding box when the inline table and the structured one
// are the same table (see attachTables).
func (x *Xberg) mapPages(doc *documentir.Document, out *xbergOutput, req Request, ocrUsed bool) {
	kind := pageKind(req.Format)
	for _, page := range out.Result.Pages {
		content := strings.TrimSpace(page.Content)
		scratch := documentir.New()
		// Spreadsheet pages are sheets. The document-level content starts each one with
		// "## <sheet>", the per-page content does not; keep the sheet boundary visible.
		if name := strings.TrimSpace(page.SheetName); name != "" && !startsWithHeading(content, name) {
			scratch.Elements = append(scratch.Elements, documentir.Element{
				ID: "el_1", Type: documentir.ElSection, Level: 2, Text: name,
			})
		}
		if content != "" {
			LiftMarkdown(scratch, content)
		}
		if len(scratch.Elements) == 0 {
			continue
		}
		if req.Format == "pdf" && !ocrUsed && page.OCRConfidence.Backend == "" && page.OCRConfidence.Score == nil {
			joinSplitParagraphs(scratch)
		}
		x.attachTables(scratch, out, req.Format, page.PageNumber, false)
		remapTables(scratch, len(doc.Tables), len(doc.Elements))
		tableLoc := make(map[string]*documentir.SourceLocation, len(scratch.Tables))
		for i := range scratch.Tables {
			t := &scratch.Tables[i]
			if t.Loc == nil || t.Loc.Kind == "none" {
				t.Loc = &documentir.SourceLocation{Kind: kind, Page: page.PageNumber}
			}
			tableLoc[t.ID] = t.Loc
		}
		for _, el := range scratch.Elements {
			if l, ok := tableLoc[el.TableID]; ok && el.Type == documentir.ElTable {
				el.Loc = l
			} else {
				el.Loc = &documentir.SourceLocation{Kind: kind, Page: page.PageNumber}
			}
			doc.Elements = append(doc.Elements, el)
		}
		doc.Tables = append(doc.Tables, scratch.Tables...)
	}
}

// attachTables reconciles xberg's structured tables with the tables already lifted
// from the Markdown content of doc (one page, or the whole document when all is set).
//
// The Markdown already holds each table at its position in the reading order, so a
// structured table that is the same table only enriches it: page number, bounding box,
// and cell text with the line breaks Markdown cannot carry. Appending it as well (what
// the adapter did with plain content) printed every table twice, the second copy at the
// end of the page. A structured table with no inline counterpart is appended at the end
// of the page, which is the only position the engine gives for it.
func (x *Xberg) attachTables(doc *documentir.Document, out *xbergOutput, format string, page int, all bool) {
	inline := len(doc.Tables)
	matched := make([]bool, inline)
	for _, t := range out.Result.Tables {
		if !all && t.PageNumber != page {
			continue
		}
		if len(t.Cells) == 0 {
			continue
		}
		loc := &documentir.SourceLocation{Kind: pageKind(format), Page: t.PageNumber}
		if loc.Page == 0 {
			loc.Page = page
		}
		if t.BoundingBox != nil {
			loc.BBox = &documentir.BBox{Coord: "pdf_user_space_v1",
				X0: t.BoundingBox.X0, Y0: t.BoundingBox.Y0, X1: t.BoundingBox.X1, Y1: t.BoundingBox.Y1}
		}
		if i := matchInlineTable(doc.Tables[:inline], matched, t.Cells); i >= 0 {
			matched[i] = true
			enrichInlineTable(&doc.Tables[i], t.Cells, loc)
			continue
		}
		if overlapsInlineTable(doc.Tables[:inline], t.Cells) {
			// Same table in a different shape (e.g. a merged header the Markdown spread
			// over two rows): the inline copy is already in place; a second one would
			// only repeat it.
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

func looksLikeMarkdown(s string) bool {
	return strings.Contains(s, "\n## ") || strings.HasPrefix(s, "## ") ||
		strings.Contains(s, "\n# ") || strings.Contains(s, "| --- |") ||
		strings.Contains(s, "|---|")
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
