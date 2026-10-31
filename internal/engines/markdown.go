package engines

import (
	"fmt"
	"regexp"
	"strings"

	"parsedock.dev/parsedock/internal/documentir"
)

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	listRe    = regexp.MustCompile(`^(\s*)([-*+]|\d{1,9}[.)])\s+(.*)$`)
	imageRe   = regexp.MustCompile(`^!\[([^\]]*)\]\(([^)]+)\)\s*$`)
	fenceRe   = regexp.MustCompile("^(`{3,}|~{3,})\\s*([^`\\s]*)")
	closingRe = regexp.MustCompile(`\s+#+\s*$`)
)

// LiftMarkdown converts Markdown into the Document IR. It is used for native
// Markdown input and as a lossy fallback for engines that only emit Markdown.
//
// Inline markup is kept in rich_text and stripped from text, so the plain views
// and scoring see words only. Ordered list markers, nesting, quotes and code
// fence languages are recorded as element metadata (docs/DOCUMENT-IR.md).
//
// It never invents coordinates or confidences: source locations are absent
// unless the caller supplies them separately.
func LiftMarkdown(doc *documentir.Document, md string) {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var para []string
	var code []string
	var quote []string
	inCode, inFormula := false, false
	fence, codeLang := "", ""
	var table []string
	lineNo := 0
	// Indentation of the open list levels, so "1. a" followed by "   - b" is level 2
	// whatever the parent marker's width.
	var listIndent []int

	nextID := func() string { return fmt.Sprintf("el_%d", len(doc.Elements)+1) }

	flushPara := func() {
		if len(para) == 0 {
			return
		}
		body := strings.TrimSpace(strings.Join(para, "\n"))
		para = para[:0]
		if body == "" {
			return
		}
		plain, rich := liftInline(body)
		doc.Elements = append(doc.Elements, documentir.Element{
			ID: nextID(), Type: documentir.ElParagraph, Text: plain, RichText: rich,
			Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
		})
	}
	flushQuote := func() {
		if len(quote) == 0 {
			return
		}
		body := strings.Trim(strings.Join(quote, "\n"), "\n")
		quote = quote[:0]
		if strings.TrimSpace(body) == "" {
			return
		}
		plain, rich := liftInline(body)
		doc.Elements = append(doc.Elements, documentir.Element{
			ID: nextID(), Type: documentir.ElParagraph, Text: plain, RichText: rich,
			Metadata: map[string]string{documentir.MetaMarkdown: documentir.MarkdownBlockquote},
			Loc:      &documentir.SourceLocation{Kind: "none", Line: lineNo},
		})
	}
	flushTable := func() {
		if len(table) == 0 {
			return
		}
		if t := parseMarkdownTable(table); t != nil {
			// ID from the document-wide counter: deriving it from the row count made two
			// ordinary tables with equal sizes collide, and the renderers resolve a duplicated
			// ID to different tables (text picks the first, markdown the last), so one table's
			// content could disappear.
			t.ID = fmt.Sprintf("tbl_%d", len(doc.Tables)+1)
			doc.Tables = append(doc.Tables, *t)
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: nextID(), Type: documentir.ElTable, TableID: t.ID,
				Text: fmt.Sprintf("%d rows x %d cols", t.Rows, t.Cols), Loc: t.Loc,
			})
		}
		table = table[:0]
	}

	for _, raw := range lines {
		lineNo++
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)

		if inCode {
			// A closing fence uses the same character, at least as many of them, and
			// nothing else (CommonMark 4.5).
			if strings.HasPrefix(trimmed, fence) && strings.Trim(trimmed, fence[:1]) == "" {
				el := documentir.Element{
					ID: nextID(), Type: documentir.ElCode, Text: strings.Join(code, "\n"),
					Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
				}
				if codeLang != "" {
					el.Metadata = map[string]string{documentir.MetaCodeLanguage: codeLang}
				}
				doc.Elements = append(doc.Elements, el)
				code = code[:0]
				inCode = false
				continue
			}
			code = append(code, line)
			continue
		}
		if m := fenceRe.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			flushQuote()
			flushTable()
			listIndent = listIndent[:0]
			inCode = true
			fence, codeLang = m[1], m[2]
			continue
		}
		if trimmed == "$$" || strings.HasPrefix(trimmed, "\\[") {
			if inFormula {
				doc.Elements = append(doc.Elements, documentir.Element{
					ID: nextID(), Type: documentir.ElFormula, Text: strings.Join(code, "\n"),
				})
				code = code[:0]
				inFormula = false
			} else {
				flushPara()
				flushQuote()
				inFormula = true
			}
			continue
		}
		if inFormula {
			code = append(code, line)
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushPara()
			flushTable()
			listIndent = listIndent[:0]
			body := strings.TrimPrefix(trimmed, ">")
			body = strings.TrimPrefix(body, " ")
			quote = append(quote, body)
			continue
		}
		flushQuote()
		if strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|") && len(trimmed) > 2 {
			flushPara()
			table = append(table, trimmed)
			continue
		}
		flushTable()

		if trimmed == "" {
			flushPara()
			continue
		}
		if m := headingRe.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			listIndent = listIndent[:0]
			typ := documentir.ElSection
			if len(m[1]) == 1 {
				typ = documentir.ElTitle
			}
			// "## Title ##": the closing sequence is not part of the heading.
			body := strings.TrimSpace(closingRe.ReplaceAllString(" "+m[2], ""))
			plain, rich := liftInline(body)
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: nextID(), Type: typ, Text: plain, RichText: rich, Level: len(m[1]),
				Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
			})
			continue
		}
		if m := imageRe.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			listIndent = listIndent[:0]
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: nextID(), Type: documentir.ElImage, Text: m[1], ImageRef: m[2],
				Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
			})
			continue
		}
		if m := listRe.FindStringSubmatch(line); m != nil && !isThematicBreak(trimmed) {
			flushPara()
			indent := len(strings.ReplaceAll(m[1], "\t", "    "))
			for len(listIndent) > 0 && listIndent[len(listIndent)-1] > indent {
				listIndent = listIndent[:len(listIndent)-1]
			}
			if len(listIndent) == 0 || listIndent[len(listIndent)-1] < indent {
				listIndent = append(listIndent, indent)
			}
			plain, rich := liftInline(strings.TrimSpace(m[3]))
			el := documentir.Element{
				ID: nextID(), Type: documentir.ElListItem, Text: plain, RichText: rich, Level: len(listIndent),
				Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
			}
			if marker := m[2]; marker[0] >= '0' && marker[0] <= '9' {
				el.Metadata = map[string]string{documentir.MetaListOrdered: "true", documentir.MetaListMarker: marker}
			}
			doc.Elements = append(doc.Elements, el)
			continue
		}
		if isThematicBreak(trimmed) {
			flushPara()
			listIndent = listIndent[:0]
			doc.Elements = append(doc.Elements, documentir.Element{ID: nextID(), Type: documentir.ElPageBreak})
			continue
		}
		if len(para) == 0 {
			// A paragraph (not a lazy continuation) ends the list.
			listIndent = listIndent[:0]
		}
		para = append(para, line)
	}
	if inCode && len(code) > 0 {
		// An unclosed fence runs to the end of the document (CommonMark 4.5).
		el := documentir.Element{ID: nextID(), Type: documentir.ElCode, Text: strings.Join(code, "\n")}
		if codeLang != "" {
			el.Metadata = map[string]string{documentir.MetaCodeLanguage: codeLang}
		}
		doc.Elements = append(doc.Elements, el)
	}
	flushQuote()
	flushTable()
	flushPara()
	doc.PageKind = "none"
	doc.Metrics.Pages = 1
}

// isThematicBreak reports a line of three or more '-', '*' or '_' (spaces allowed).
func isThematicBreak(s string) bool {
	s = strings.ReplaceAll(s, " ", "")
	if len(s) < 3 {
		return false
	}
	return strings.Trim(s, "-") == "" || strings.Trim(s, "*") == "" || strings.Trim(s, "_") == ""
}

func parseMarkdownTable(rows []string) *documentir.Table {
	grid := make([][]string, 0, len(rows))
	headerRows := 0
	for _, r := range rows {
		cells := splitTableRow(r)
		if isSeparatorRow(cells) {
			headerRows = 1
			continue
		}
		grid = append(grid, cells)
	}
	if len(grid) == 0 {
		return nil
	}
	cols := 0
	for _, r := range grid {
		if len(r) > cols {
			cols = len(r)
		}
	}
	t := &documentir.Table{
		ID:         "tbl_pending",
		Rows:       len(grid),
		Cols:       cols,
		HeaderRows: headerRows,
	}
	for ri, r := range grid {
		for ci, val := range r {
			plain, rich := liftInline(val)
			t.Cells = append(t.Cells, documentir.Cell{Row: ri, Col: ci, RowSpan: 1, ColSpan: 1,
				Text: plain, RichText: rich, DataType: guessType(plain)})
		}
	}
	return t
}

// splitTableRow splits a GFM table row on unescaped pipes. A "\|" stays inside the cell
// (GFM 4.10: the escape applies even inside code spans).
func splitTableRow(row string) []string {
	inner := strings.TrimSpace(row)
	inner = strings.TrimPrefix(inner, "|")
	if strings.HasSuffix(inner, "|") && !strings.HasSuffix(inner, `\|`) {
		inner = inner[:len(inner)-1]
	}
	var cells []string
	var cur strings.Builder
	backslashes := 0
	for _, r := range inner {
		if r == '|' && backslashes%2 == 0 {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			backslashes = 0
			continue
		}
		if r == '\\' {
			backslashes++
		} else {
			backslashes = 0
		}
		cur.WriteRune(r)
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

var delimiterCellRe = regexp.MustCompile(`^:?-+:?$`)

// isSeparatorRow reports a GFM delimiter row. Every cell needs at least one '-': a row
// of empty cells (an empty spreadsheet row) is data, and treating it as the delimiter
// silently dropped it.
func isSeparatorRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if !delimiterCellRe.MatchString(strings.ReplaceAll(c, " ", "")) {
			return false
		}
	}
	return true
}
