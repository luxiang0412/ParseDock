package engines

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

// Native is the dependency-free engine for text-like formats. It never loads
// an OCR model and never leaves the process.
type Native struct{ VersionStr string }

// Name implements Engine.
func (n *Native) Name() string { return "native" }

// Version implements Engine.
func (n *Native) Version() string {
	if n.VersionStr != "" {
		return n.VersionStr
	}
	return "go-native-1"
}

// Device implements Engine.
func (n *Native) Device() string { return "cpu" }

// Formats implements Engine.
func (n *Native) Formats() []string { return []string{"txt", "md", "csv", "tsv"} }

// Modes implements Engine.
func (n *Native) Modes() []string { return []string{"fast", "auto"} }

// Probe implements Engine.
func (n *Native) Probe(context.Context) error { return nil }

// Parse implements Engine.
func (n *Native) Parse(ctx context.Context, req Request) (*documentir.Document, error) {
	raw, err := os.ReadFile(req.Path)
	if err != nil {
		return nil, Errf(true, jobs.ErrInternal, "read input: %v", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, Errf(true, jobs.ErrTimeout, "parse cancelled")
	}
	text, enc, err := decodeText(raw)
	if err != nil {
		return nil, Errf(false, jobs.ErrMalformed, "cannot decode text: %v", err)
	}
	doc := documentir.New()
	doc.ParserEngine = n.Name()
	doc.ParserVersion = n.Version()
	doc.Source.Format = req.Format
	doc.Source.MimeType = req.MimeType
	doc.Source.SizeBytes = int64(len(raw))
	if enc != "utf-8" {
		doc.AddWarning("encoding_converted", "input decoded from "+enc, "info")
	}
	switch req.Format {
	case "csv", "tsv":
		if err := n.parseDelimited(ctx, doc, text, req); err != nil {
			return nil, err
		}
	case "md":
		LiftMarkdown(doc, text)
	default:
		liftPlainText(doc, text)
	}
	return doc, nil
}

// decodeText detects BOMs, then UTF-8 validity, then GB18030.
func decodeText(raw []byte) (string, string, error) {
	switch {
	case bytes.HasPrefix(raw, []byte{0xEF, 0xBB, 0xBF}):
		return string(raw[3:]), "utf-8-bom", nil
	case bytes.HasPrefix(raw, []byte{0xFF, 0xFE}):
		return decodeUTF16(raw[2:], false), "utf-16le", nil
	case bytes.HasPrefix(raw, []byte{0xFE, 0xFF}):
		return decodeUTF16(raw[2:], true), "utf-16be", nil
	}
	if utf8.Valid(raw) {
		return string(raw), "utf-8", nil
	}
	out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(raw)
	if err == nil {
		return string(out), "gb18030", nil
	}
	// Latin-1 is always decodable and better than failing a text upload.
	var b strings.Builder
	for _, c := range raw {
		b.WriteRune(rune(c))
	}
	return b.String(), "latin-1", nil
}

func decodeUTF16(b []byte, bigEndian bool) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		if bigEndian {
			u = append(u, uint16(b[i])<<8|uint16(b[i+1]))
		} else {
			u = append(u, uint16(b[i+1])<<8|uint16(b[i]))
		}
	}
	return string(utf16.Decode(u))
}

func (n *Native) parseDelimited(ctx context.Context, doc *documentir.Document, text string, req Request) error {
	delim := ','
	if req.Format == "tsv" {
		delim = '\t'
	} else if d := sniffDelimiter(text); d != 0 {
		delim = d
	}
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	records, err := r.ReadAll()
	if err != nil {
		return Errf(false, jobs.ErrMalformed, "csv parse: %v", err)
	}
	if len(records) == 0 {
		doc.AddWarning(documentir.WarnPartialContent, "file contains no records", "warn")
		return nil
	}
	const maxRows = 200000
	truncated := false
	if len(records) > maxRows {
		records = records[:maxRows]
		truncated = true
	}
	cols := 0
	for _, row := range records {
		if len(row) > cols {
			cols = len(row)
		}
	}
	t := &documentir.Table{
		ID:         "tbl_1",
		Rows:       len(records),
		Cols:       cols,
		HeaderRows: 1,
		Loc:        &documentir.SourceLocation{Kind: "sheet", Sheet: "csv", Range: "A1"},
	}
	for ri, row := range records {
		for ci, val := range row {
			cell := documentir.Cell{Row: ri, Col: ci, RowSpan: 1, ColSpan: 1, Text: val, DataType: guessType(val)}
			t.Cells = append(t.Cells, cell)
		}
	}
	doc.Tables = append(doc.Tables, *t)
	doc.Elements = append(doc.Elements, documentir.Element{
		ID: "el_1", Type: documentir.ElTable, TableID: t.ID, Text: fmt.Sprintf("%d rows x %d cols", t.Rows, t.Cols),
		Loc: t.Loc,
	})
	if truncated {
		doc.AddWarning(documentir.WarnTruncated, fmt.Sprintf("only the first %d rows were parsed", maxRows), "warn")
	}
	doc.Metrics.Pages = 1
	doc.PageCount = 1
	doc.PageKind = "sheet"
	return nil
}

func sniffDelimiter(s string) rune {
	line := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		line = s[:i]
	}
	counts := map[rune]int{',': strings.Count(line, ","), ';': strings.Count(line, ";"),
		'\t': strings.Count(line, "\t"), '|': strings.Count(line, "|")}
	best, bestN := rune(0), 0
	for _, r := range []rune{',', ';', '\t', '|'} {
		if counts[r] > bestN {
			best, bestN = r, counts[r]
		}
	}
	if bestN == 0 {
		return 0
	}
	return best
}

func guessType(s string) string {
	if s == "" {
		return ""
	}
	if _, err := strconv.ParseFloat(strings.ReplaceAll(s, ",", ""), 64); err == nil {
		return "number"
	}
	return "string"
}

func liftPlainText(doc *documentir.Document, text string) {
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	var para []string
	line := 0
	flush := func() {
		if len(para) == 0 {
			return
		}
		body := strings.TrimSpace(strings.Join(para, "\n"))
		para = para[:0]
		if body == "" {
			return
		}
		doc.Elements = append(doc.Elements, documentir.Element{
			ID:   fmt.Sprintf("el_%d", len(doc.Elements)+1),
			Type: documentir.ElParagraph,
			Text: body,
			Loc:  &documentir.SourceLocation{Kind: "none", Line: line},
		})
	}
	for sc.Scan() {
		line++
		l := sc.Text()
		if strings.TrimSpace(l) == "" {
			flush()
			continue
		}
		para = append(para, l)
	}
	flush()
	doc.PageCount = 1
	doc.PageKind = "none"
	doc.Metrics.Pages = 1
}
