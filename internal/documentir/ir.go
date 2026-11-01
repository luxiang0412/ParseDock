// Package documentir defines ParseDock's engine-independent Document IR v0.1.
//
// The IR is the single source of truth for parse output. Markdown/plain text
// are derived views (see export.go); JSON is the IR itself. Nothing in the IR
// may invent precision that an engine did not provide: missing coordinates,
// page numbers or confidences are represented as absent (nil / empty), never
// as fabricated values.
package documentir

import "time"

// SchemaVersion is the IR contract version. Bump on breaking changes.
const SchemaVersion = "doc-ir/0.1"

// Element types. Keep this list closed so downstream consumers can switch on it.
const (
	ElTitle     = "title"
	ElSection   = "section"
	ElParagraph = "paragraph"
	ElList      = "list"
	ElListItem  = "list_item"
	ElTable     = "table"
	ElImage     = "image"
	ElFormula   = "formula"
	ElCaption   = "caption"
	ElFootnote  = "footnote"
	ElHeader    = "header"
	ElFooter    = "footer"
	ElCode      = "code"
	ElPageBreak = "page_break"
	ElUnknown   = "unknown"
)

// Document is the root object.
type Document struct {
	SchemaVersion string    `json:"schema_version"`
	Source        Source    `json:"source"`
	ParserEngine  string    `json:"parser_engine"`
	ParserVersion string    `json:"parser_version"`
	Routing       Routing   `json:"routing"`
	PageCount     int       `json:"page_count"` // PDF pages / slides / sheets; 0 when not applicable
	PageKind      string    `json:"page_kind"`  // "pdf" | "sheet" | "slide" | "none"
	Elements      []Element `json:"elements"`
	Tables        []Table   `json:"tables,omitempty"`
	Warnings      []Warning `json:"warnings"`
	Metrics       Metrics   `json:"metrics"`

	// OCR is the engine's own OCR quality evidence, kept in memory only (see
	// OCRQuality). Adapters that report no confidence leave it nil.
	OCR *OCRQuality `json:"-"`
}

// Source identifies the input without leaking its bytes.
type Source struct {
	FileName  string `json:"file_name"` // original client filename (sanitized, display only)
	MimeType  string `json:"mime_type"`
	Format    string `json:"format"` // pdf | docx | xlsx | ...
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// Routing records why this engine was selected for the job (auditability).
type Routing struct {
	RequestedMode   string `json:"requested_mode"`
	RequestedDevice string `json:"requested_device"`
	ActualDevice    string `json:"actual_device"`
	Engine          string `json:"engine"`
	Fallback        bool   `json:"fallback"`
	Reason          string `json:"reason"`
}

// Element is one addressable unit of reading order.
//
// Text is always plain: inline markup is stripped so text search, embeddings, the
// text view and quality scoring see the words only. RichText, when present, is the
// same content with CommonMark inline markup (**bold**, *italic*, `code`,
// [text](url), ~~strike~~) and is what the Markdown view renders. It is absent when
// the engine reported no inline formatting, so consumers fall back to Text.
type Element struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Text         string          `json:"text"`
	RichText     string          `json:"rich_text,omitempty"`
	Level        int             `json:"level,omitempty"` // heading level, list nesting
	ReadingOrder int             `json:"reading_order"`
	Confidence   *float64        `json:"confidence,omitempty"`
	Loc          *SourceLocation `json:"source_location,omitempty"`
	TableID      string          `json:"table_id,omitempty"`
	ImageRef     string          `json:"image_ref,omitempty"` // artifact-relative ref, never inline bytes
	// Metadata carries open-ended, string-valued hints. The keys the renderers act on
	// are the Meta* constants below; anything else is passed through untouched.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Metadata keys with a defined meaning (docs/DOCUMENT-IR.md, "Element metadata").
const (
	// MetaContentLayer = "furniture" marks page furniture (running headers/footers,
	// page numbers) that the engine kept out of the body. JSON keeps it; the Markdown
	// and text views skip it.
	MetaContentLayer = "content_layer"
	// MetaMarkdown = "blockquote" marks a quoted paragraph.
	MetaMarkdown = "markdown"
	// MetaListOrdered = "true" marks an item of a numbered list.
	MetaListOrdered = "list_ordered"
	// MetaListMarker is the marker the source used ("3.", "a)", "iv."), when known.
	MetaListMarker = "list_marker"
	// MetaCodeLanguage is the language of a code block ("go", "python"), when known.
	MetaCodeLanguage = "code_language"
	// MetaRecovered = "text_layer" marks a paragraph the adapter put back from the PDF
	// text layer because the engine's structured output had lost it. Its position is the
	// best the adapter could infer, and it carries no inline formatting.
	MetaRecovered = "recovered"

	ContentLayerFurniture = "furniture"
	MarkdownBlockquote    = "blockquote"
	RecoveredTextLayer    = "text_layer"
)

// IsFurniture reports whether an element is page furniture rather than body
// content: a page header/footer element, or anything the engine put on the
// furniture content layer.
func (e *Element) IsFurniture() bool {
	return e.Type == ElHeader || e.Type == ElFooter || e.Metadata[MetaContentLayer] == ContentLayerFurniture
}

// SourceLocation declares which coordinate system a box belongs to.
//
// A bbox is only interpretable if the consumer knows the page box, so the page
// size is carried alongside it when the engine provides it (PDF points, 1/72").
// Absent page size means "unknown", never a default.
type SourceLocation struct {
	Kind         string  `json:"kind"` // pdf | sheet | slide | none
	Page         int     `json:"page,omitempty"`
	BBox         *BBox   `json:"bbox,omitempty"`
	Sheet        string  `json:"sheet,omitempty"`
	Range        string  `json:"cell_range,omitempty"` // A1:F20
	Line         int     `json:"line,omitempty"`       // plain-text sources
	Rotation     int     `json:"rotation_deg,omitempty"`
	PageWidthPt  float64 `json:"page_width_pt,omitempty"`
	PageHeightPt float64 `json:"page_height_pt,omitempty"`
}

// BBox is axis-aligned; Coord names the coordinate space so consumers never
// mix PDF user space with image pixel space.
type BBox struct {
	Coord string  `json:"coord"` // pdf_user_space_v1 | image_px_v1
	X0    float64 `json:"x0"`
	Y0    float64 `json:"y0"`
	X1    float64 `json:"x1"`
	Y1    float64 `json:"y1"`
}

// Table is a structured grid; cells may carry formulas and cached values.
type Table struct {
	ID         string          `json:"id"`
	Rows       int             `json:"rows"`
	Cols       int             `json:"cols"`
	HeaderRows int             `json:"header_rows"`
	Cells      []Cell          `json:"cells"`
	Loc        *SourceLocation `json:"source_location,omitempty"`
	Caption    string          `json:"caption,omitempty"`
	Confidence *float64        `json:"confidence,omitempty"`
}

// Cell is one grid cell. Merged cells appear once at their anchor position.
type Cell struct {
	Row         int      `json:"row"`
	Col         int      `json:"col"`
	RowSpan     int      `json:"row_span"`
	ColSpan     int      `json:"col_span"`
	Text        string   `json:"text"`
	RichText    string   `json:"rich_text,omitempty"` // Text with CommonMark inline markup; see Element.RichText
	Formula     string   `json:"formula,omitempty"`
	CachedValue string   `json:"cached_value,omitempty"`
	DataType    string   `json:"data_type,omitempty"` // string|number|date|bool
	Confidence  *float64 `json:"confidence,omitempty"`
}

// Warning codes are stable strings; the message is human-readable.
const (
	WarnOCRNotRun = "ocr_not_run"
	// WarnOCRUsed says only that OCR ran. It is informational: OCR on a clean
	// scan is the normal path, not a defect, so nothing may treat it as a
	// quality signal.
	WarnOCRUsed = "ocr_used"
	// WarnOCRLowConfidence is reserved for a *measured* confidence below the
	// engine's threshold. It must never be emitted because a backend declined to
	// report a score - that is WarnOCRUsedUnknown.
	WarnOCRLowConfidence = "ocr_low_confidence"
	WarnNoTextLayer      = "no_text_layer"
	WarnTruncated        = "truncated"
	WarnPageLimit        = "page_limit_reached"
	WarnPartialContent   = "partial_content"
	WarnEncrypted        = "encrypted"
	WarnDegraded         = "degraded_conversion"
	WarnUnsupported      = "unsupported_element"
	WarnLayoutHeuristic  = "layout_heuristic"
	WarnNoCoordinates    = "no_coordinates"
	WarnNoPageBoundaries = "no_page_boundaries"
	WarnFormulaMissing   = "formula_text_unavailable"
	WarnOCRUsedUnknown   = "ocr_quality_unknown"
	// WarnTextRecovered (info) says the adapter put back paragraphs from the PDF text
	// layer that the engine's structured output had lost; they carry
	// metadata.recovered = "text_layer".
	WarnTextRecovered = "text_recovered"
)

// OCRQuality carries the OCR quality evidence an engine actually reported. It is
// deliberately not serialised: consumers read the warnings, while the scheduler
// needs the numbers while the document is still in memory to decide whether a
// retry is justified. Nil means "the engine said nothing about OCR quality".
type OCRQuality struct {
	Used         bool    `json:"-"`
	Scored       bool    `json:"-"` // at least one page reported a confidence
	MinScore     float64 `json:"-"` // valid only when Scored
	MinScorePage int     `json:"-"`
}

// Warning is a non-fatal observation about the parse.
type Warning struct {
	Code     string          `json:"code"`
	Message  string          `json:"message"`
	Loc      *SourceLocation `json:"source_location,omitempty"`
	Severity string          `json:"severity"` // info | warn | error
}

// Metrics are engine-measured, not estimated.
type Metrics struct {
	ParseMs     int64      `json:"parse_ms"`
	InputBytes  int64      `json:"input_bytes"`
	OutputChars int        `json:"output_chars"`
	Pages       int        `json:"pages"`
	PeakRSSMB   *int64     `json:"peak_rss_mb,omitempty"`
	PeakVRAMMB  *int64     `json:"peak_vram_mb,omitempty"`
	ModelLoadMs *int64     `json:"model_load_ms,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// New returns a Document with schema version and non-nil slices.
func New() *Document {
	return &Document{
		SchemaVersion: SchemaVersion,
		PageKind:      "none",
		Elements:      []Element{},
		Warnings:      []Warning{},
	}
}

// AddWarning appends a warning with severity.
func (d *Document) AddWarning(code, msg, severity string) {
	d.Warnings = append(d.Warnings, Warning{Code: code, Message: msg, Severity: severity})
}

// Finalize fills derived counters and renumbers reading order defensively.
func (d *Document) Finalize() {
	chars := 0
	for i := range d.Elements {
		d.Elements[i].ReadingOrder = i
		chars += len([]rune(d.Elements[i].Text))
	}
	d.Metrics.OutputChars = chars
	if d.Metrics.Pages == 0 {
		d.Metrics.Pages = d.PageCount
	}
}

// TextLength counts runes in element text plus table cell text. It is the
// comparison used when deciding whether an escalated parse is better: ignoring
// table cells would let a retry silently drop extracted tables.
func (d *Document) TextLength() int {
	n := 0
	for _, el := range d.Elements {
		n += len([]rune(el.Text))
	}
	for _, t := range d.Tables {
		for _, c := range t.Cells {
			n += len([]rune(c.Text))
		}
	}
	return n
}

// HasBBox reports whether any element or table carries geometry. Adapters use it
// to decide whether a `no_coordinates` warning is truthful.
func (d *Document) HasBBox() bool {
	for _, el := range d.Elements {
		if el.Loc != nil && el.Loc.BBox != nil {
			return true
		}
	}
	for _, t := range d.Tables {
		if t.Loc != nil && t.Loc.BBox != nil {
			return true
		}
	}
	return false
}

// HasImageRefs reports whether any element references an image artifact. No
// adapter publishes image artifacts yet (PR-05), so this must stay false until a
// download path exists; the test suite pins that.
func (d *Document) HasImageRefs() bool {
	for _, el := range d.Elements {
		if el.ImageRef != "" {
			return true
		}
	}
	return false
}
