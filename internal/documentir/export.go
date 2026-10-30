package documentir

import (
	"fmt"
	"strings"
)

// Format is a supported derivative representation.
type Format string

const (
	FormatJSON     Format = "json"
	FormatMarkdown Format = "markdown"
	FormatText     Format = "text"
)

// ParseFormat validates an API format query value.
func ParseFormat(s string) (Format, bool) {
	switch Format(strings.ToLower(strings.TrimSpace(s))) {
	case "", FormatMarkdown, "md":
		return FormatMarkdown, true
	case FormatJSON:
		return FormatJSON, true
	case FormatText:
		return FormatText, true
	}
	return "", false
}

// RenderMarkdown converts the IR to Markdown. Tables are rendered from the
// structured grid, never re-parsed from text.
func RenderMarkdown(d *Document) string {
	var b strings.Builder
	tables := map[string]*Table{}
	for i := range d.Tables {
		tables[d.Tables[i].ID] = &d.Tables[i]
	}
	prevList := false
	for _, el := range d.Elements {
		if prevList && el.Type != ElListItem {
			b.WriteString("\n")
			prevList = false
		}
		switch el.Type {
		case ElTitle, ElSection:
			lvl := el.Level
			if lvl < 1 {
				lvl = 1
			}
			if lvl > 6 {
				lvl = 6
			}
			b.WriteString(strings.Repeat("#", lvl) + " " + oneLine(el.Text) + "\n\n")
		case ElParagraph:
			b.WriteString(strings.TrimRight(el.Text, "\n") + "\n\n")
		case ElList:
			// container only
		case ElListItem:
			ind := strings.Repeat("  ", max(0, el.Level-1))
			b.WriteString(ind + "- " + oneLine(el.Text) + "\n")
			prevList = true
		case ElTable:
			if t := tables[el.TableID]; t != nil {
				b.WriteString(renderMarkdownTable(t) + "\n")
			}
		case ElImage:
			alt := oneLine(el.Text)
			if alt == "" {
				alt = "image"
			}
			b.WriteString("![" + alt + "](" + el.ImageRef + ")\n\n")
		case ElFormula:
			b.WriteString("$$\n" + strings.TrimSpace(el.Text) + "\n$$\n\n")
		case ElCaption, ElFootnote:
			b.WriteString("_" + oneLine(el.Text) + "_\n\n")
		case ElCode:
			b.WriteString("```\n" + strings.TrimRight(el.Text, "\n") + "\n```\n\n")
		case ElPageBreak:
			b.WriteString("\n---\n\n")
		case ElHeader, ElFooter:
			// rendered only in JSON/text layers; keep Markdown body clean
		default:
			if strings.TrimSpace(el.Text) != "" {
				b.WriteString(strings.TrimRight(el.Text, "\n") + "\n\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// RenderText converts the IR to plain text with page markers.
func RenderText(d *Document) string {
	var b strings.Builder
	lastPage := 0
	for _, el := range d.Elements {
		if el.Loc != nil && el.Loc.Kind == "pdf" && el.Loc.Page > 0 && el.Loc.Page != lastPage {
			if lastPage != 0 {
				b.WriteString("\f\n")
			}
			lastPage = el.Loc.Page
		}
		switch el.Type {
		case ElTable:
			for i := range d.Tables {
				if d.Tables[i].ID == el.TableID {
					b.WriteString(renderTextTable(&d.Tables[i]) + "\n")
					break
				}
			}
		case ElPageBreak, ElHeader, ElFooter:
			continue
		case ElListItem:
			b.WriteString(strings.Repeat("  ", max(0, el.Level-1)) + "- " + oneLine(el.Text) + "\n")
		default:
			if el.Text != "" {
				b.WriteString(strings.TrimRight(el.Text, "\n") + "\n")
			}
		}
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func renderMarkdownTable(t *Table) string {
	grid := tableGrid(t)
	if len(grid) == 0 {
		return ""
	}
	var b strings.Builder
	hdr := 0
	if t.HeaderRows > 0 && t.HeaderRows <= len(grid) {
		hdr = t.HeaderRows
	}
	for i, row := range grid {
		b.WriteString("| " + strings.Join(row, " | ") + " |\n")
		if i == max(0, hdr-1) || (hdr == 0 && i == 0) {
			b.WriteString("|" + strings.Repeat(" --- |", len(row)) + "\n")
		}
	}
	return b.String()
}

func renderTextTable(t *Table) string {
	var b strings.Builder
	for _, row := range tableGrid(t) {
		b.WriteString(strings.Join(row, "\t") + "\n")
	}
	return b.String()
}

// maxRenderCells caps the dense grid a single table may allocate. Ingest validation rejects
// such tables already (Limits.Validate); this is the second line of defence so no future
// caller can turn a bad IR into a multi-gigabyte allocation.
const maxRenderCells = 2_000_000

func tableGrid(t *Table) [][]string {
	if t.Rows <= 0 || t.Cols <= 0 {
		return nil
	}
	if int64(t.Rows)*int64(t.Cols) > maxRenderCells {
		return nil
	}
	grid := make([][]string, t.Rows)
	for r := range grid {
		grid[r] = make([]string, t.Cols)
	}
	for _, c := range t.Cells {
		if c.Row < 0 || c.Row >= t.Rows || c.Col < 0 || c.Col >= t.Cols {
			continue
		}
		grid[c.Row][c.Col] = strings.ReplaceAll(strings.ReplaceAll(c.Text, "\n", " "), "|", "\\|")
	}
	return grid
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// SummaryLine is a compact human string used in logs and CLI output.
func SummaryLine(d *Document) string {
	return fmt.Sprintf("engine=%s/%s pages=%d elements=%d tables=%d chars=%d warnings=%d",
		d.ParserEngine, d.ParserVersion, d.PageCount, len(d.Elements), len(d.Tables),
		d.Metrics.OutputChars, len(d.Warnings))
}
