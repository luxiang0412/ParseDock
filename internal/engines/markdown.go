package engines

import (
	"fmt"
	"regexp"
	"strings"

	"parsedock.dev/parsedock/internal/documentir"
)

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	listRe    = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	imageRe   = regexp.MustCompile(`^!\[([^\]]*)\]\(([^)]+)\)\s*$`)
)

// LiftMarkdown converts Markdown into the Document IR. It is used for native
// Markdown input and as a lossy fallback for engines that only emit Markdown.
//
// It never invents coordinates or confidences: source locations are absent
// unless the caller supplies them separately.
func LiftMarkdown(doc *documentir.Document, md string) {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var para []string
	var code []string
	inCode, inFormula := false, false
	var table []string
	lineNo := 0

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
		doc.Elements = append(doc.Elements, documentir.Element{
			ID: nextID(), Type: documentir.ElParagraph, Text: body,
			Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
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

		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				doc.Elements = append(doc.Elements, documentir.Element{
					ID: nextID(), Type: documentir.ElCode, Text: strings.Join(code, "\n"),
					Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
				})
				code = code[:0]
				inCode = false
			} else {
				flushPara()
				inCode = true
			}
			continue
		}
		if inCode {
			code = append(code, line)
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
				inFormula = true
			}
			continue
		}
		if inFormula {
			code = append(code, line)
			continue
		}
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
			typ := documentir.ElSection
			if len(m[1]) == 1 {
				typ = documentir.ElTitle
			}
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: nextID(), Type: typ, Text: strings.TrimSpace(m[2]), Level: len(m[1]),
				Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
			})
			continue
		}
		if m := imageRe.FindStringSubmatch(trimmed); m != nil {
			flushPara()
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: nextID(), Type: documentir.ElImage, Text: m[1], ImageRef: m[2],
				Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
			})
			continue
		}
		if m := listRe.FindStringSubmatch(line); m != nil {
			flushPara()
			level := len(strings.ReplaceAll(m[1], "\t", "    "))/2 + 1
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: nextID(), Type: documentir.ElListItem, Text: strings.TrimSpace(m[3]), Level: level,
				Loc: &documentir.SourceLocation{Kind: "none", Line: lineNo},
			})
			continue
		}
		if strings.HasPrefix(trimmed, "> ") {
			flushPara()
			doc.Elements = append(doc.Elements, documentir.Element{
				ID: nextID(), Type: documentir.ElParagraph, Text: strings.TrimSpace(trimmed[2:]),
				Metadata: map[string]string{"markdown": "blockquote"},
				Loc:      &documentir.SourceLocation{Kind: "none", Line: lineNo},
			})
			continue
		}
		if strings.HasPrefix(trimmed, "---") && len(trimmed) >= 3 && strings.Trim(trimmed, "-") == "" {
			flushPara()
			doc.Elements = append(doc.Elements, documentir.Element{ID: nextID(), Type: documentir.ElPageBreak})
			continue
		}
		para = append(para, line)
	}
	flushTable()
	flushPara()
	doc.PageKind = "none"
	doc.Metrics.Pages = 1
}

func parseMarkdownTable(rows []string) *documentir.Table {
	grid := make([][]string, 0, len(rows))
	headerRows := 0
	for _, r := range rows {
		inner := strings.TrimSuffix(strings.TrimPrefix(r, "|"), "|")
		cells := strings.Split(inner, "|")
		for i := range cells {
			cells[i] = strings.TrimSpace(cells[i])
		}
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
			t.Cells = append(t.Cells, documentir.Cell{Row: ri, Col: ci, RowSpan: 1, ColSpan: 1, Text: val, DataType: guessType(val)})
		}
	}
	return t
}

func isSeparatorRow(cells []string) bool {
	if len(cells) == 0 {
		return false
	}
	for _, c := range cells {
		if strings.Trim(c, ":- ") != "" {
			return false
		}
	}
	return true
}
