package engines

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"parsedock.dev/parsedock/internal/documentir"
)

// Text-layer recovery for xberg PDFs.
//
// xberg's Markdown writer (--content-format markdown, which the adapter needs for
// headings, lists and table placement) drops some text that its plain writer keeps:
// measured on a 526-PDF private corpus, 0.1-0.2 % of the words overall but up to 18 % of
// a short document, mostly whole or half lines next to tables. The plain text layer is
// cheap to read (--ocr false: p50 0.09 s against 0.32 s for the main run), so the adapter
// reads it as well and puts back the lines the Markdown result lost.
//
// Only lines that are mostly missing are recovered. The far more common case, a line with
// one or two words that differ between the two writers (words run together, a word
// spelled differently), is left alone: re-inserting such a line would repeat the other
// nine words of it.

const (
	// A line is recovered when at least this share of its words, and at least
	// recoverMinAbsent of them, occur nowhere in the parsed document.
	recoverAbsentShare = 0.5
	recoverMinAbsent   = 2
	// Lines shorter than this are page numbers, labels and stray fragments.
	recoverMinTokens = 3
)

// plainArgs is the argv for the text-layer read: no OCR (scanned pages yield nothing and
// are not checked), plain content.
func (x *Xberg) plainArgs() []string {
	args := []string{"extract", "--no-config-discovery", "--format", "json", "--content-format", "plain", "--ocr", "false"}
	if !x.Cache {
		args = append(args, "--no-cache", "true")
	}
	return args
}

// recoverLostText reads the text layer and puts back what the Markdown result lost. It is
// best effort: a failed read means "no recovery", never a failed job, because the main
// result stands on its own.
func (x *Xberg) recoverLostText(ctx context.Context, doc *documentir.Document, out *xbergOutput, workDir, local string) {
	res, err := RunCLI(ctx, x.spec(), workDir, append(x.plainArgs(), local)...)
	if res != nil {
		doc.Metrics.ParseMs += res.Elapsed.Milliseconds()
	}
	if err != nil || res == nil || res.Truncated || len(res.Stdout) == 0 {
		return
	}
	var plain xbergOutput
	if json.Unmarshal(res.Stdout, &plain) != nil {
		return
	}
	// Pages the main run OCRed hold recognised text, not the text layer; comparing the two
	// would "recover" every OCR misreading.
	skip := map[int]bool{}
	for _, pg := range out.Result.Pages {
		if pg.OCRConfidence.Backend != "" || pg.OCRConfidence.Score != nil || pg.OCRConfidence.WordCount > 0 {
			skip[pg.PageNumber] = true
		}
	}
	if n := recoverTextLayer(doc, plain.Result.Pages, skip); n > 0 {
		doc.AddWarning(documentir.WarnTextRecovered,
			fmt.Sprintf("%d paragraph(s) restored from the PDF text layer that the structured output had dropped", n), "info")
	}
}

// recoverTextLayer inserts, as paragraphs, the text-layer lines whose words the document
// lost. skip lists the pages not to check (pages the main run OCRed: their text is not the
// text layer's). It returns the number of paragraphs inserted.
func recoverTextLayer(doc *documentir.Document, plain []xbergPage, skip map[int]bool) int {
	if len(plain) == 0 {
		return 0
	}
	present := map[string]bool{}
	for i := range doc.Elements {
		if doc.Elements[i].Type != documentir.ElTable {
			forTokens(doc.Elements[i].Text, func(t string) { present[t] = true })
		}
	}
	for i := range doc.Tables {
		for _, c := range doc.Tables[i].Cells {
			forTokens(c.Text, func(t string) { present[t] = true })
		}
	}
	// A line that repeats on several pages is a running header or footer; the Markdown
	// writer leaving it out is the right outcome.
	onPages := map[string]map[int]bool{}
	for _, pg := range plain {
		for _, l := range strings.Split(pg.Content, "\n") {
			k := lineKey(l)
			if k == "" {
				continue
			}
			if onPages[k] == nil {
				onPages[k] = map[int]bool{}
			}
			onPages[k][pg.PageNumber] = true
		}
	}

	type block struct {
		page   int
		anchor string // tail of the last surviving line before the block ("" = page top)
		lines  []string
	}
	var blocks []block
	for _, pg := range plain {
		if skip[pg.PageNumber] {
			continue
		}
		anchor := ""
		var cur *block
		for _, raw := range strings.Split(pg.Content, "\n") {
			l := strings.TrimSpace(raw)
			n, absent := 0, 0
			forTokens(l, func(t string) {
				n++
				if !present[t] {
					absent++
				}
			})
			if n == 0 {
				continue
			}
			lost := n >= recoverMinTokens && absent >= recoverMinAbsent &&
				float64(absent) >= recoverAbsentShare*float64(n) && len(onPages[lineKey(l)]) < 2
			if !lost {
				cur = nil
				anchor = anchorOf(l)
				continue
			}
			if cur == nil {
				blocks = append(blocks, block{page: pg.PageNumber, anchor: anchor})
				cur = &blocks[len(blocks)-1]
			}
			cur.lines = append(cur.lines, l)
		}
	}
	for _, b := range blocks {
		text := b.lines[0]
		for _, l := range b.lines[1:] {
			text += joinSeparator(text, l) + l
		}
		at := insertionIndex(doc, b.page, b.anchor)
		el := documentir.Element{
			Type: documentir.ElParagraph, Text: text,
			Loc:      &documentir.SourceLocation{Kind: "pdf", Page: b.page},
			Metadata: map[string]string{documentir.MetaRecovered: documentir.RecoveredTextLayer},
		}
		doc.Elements = append(doc.Elements, documentir.Element{})
		copy(doc.Elements[at+1:], doc.Elements[at:])
		doc.Elements[at] = el
	}
	if len(blocks) > 0 {
		renumberElementIDs(doc)
	}
	return len(blocks)
}

// insertionIndex is where a recovered block goes: after the element of its page that
// contains the anchor line, at the top of the page when the block opens it, else at the
// end of the page (the page order of the document is kept either way).
func insertionIndex(doc *documentir.Document, page int, anchor string) int {
	first, last := -1, -1
	for i := range doc.Elements {
		p := 0
		if doc.Elements[i].Loc != nil {
			p = doc.Elements[i].Loc.Page
		}
		if p == page {
			if first < 0 {
				first = i
			}
			last = i
		}
		if p > page && first < 0 {
			return i // the page has no elements of its own: before the next page
		}
	}
	if first < 0 {
		return len(doc.Elements)
	}
	if anchor == "" {
		return first
	}
	for i := first; i <= last; i++ {
		if strings.Contains(elementKey(doc, &doc.Elements[i]), anchor) {
			return i + 1
		}
	}
	return last + 1
}

// Anchors are compared as word sequences, not raw text: the Markdown result has lost the
// list markers, emphasis and spacing the text layer still carries. The last few words of
// a line are enough to place it and are more likely to sit inside one element.
const anchorWords = 6

func anchorOf(l string) string {
	var toks []string
	forTokens(l, func(t string) { toks = append(toks, t) })
	if len(toks) > anchorWords {
		toks = toks[len(toks)-anchorWords:]
	}
	return "\x00" + strings.Join(toks, "\x00") + "\x00"
}

func elementKey(doc *documentir.Document, el *documentir.Element) string {
	var toks []string
	add := func(s string) { forTokens(s, func(t string) { toks = append(toks, t) }) }
	if el.Type != documentir.ElTable {
		add(el.Text)
	} else {
		for i := range doc.Tables {
			if doc.Tables[i].ID == el.TableID {
				for _, row := range plainGrid(&doc.Tables[i]) {
					for _, c := range row {
						add(c)
					}
				}
			}
		}
	}
	return "\x00" + strings.Join(toks, "\x00") + "\x00"
}

// renumberElementIDs gives elements sequential ids after an insertion; nothing refers to
// element ids (tables are referenced through table_id).
func renumberElementIDs(doc *documentir.Document) {
	for i := range doc.Elements {
		doc.Elements[i].ID = fmt.Sprintf("el_%d", i+1)
	}
}

// forTokens calls fn for each word of s, lower-cased: a run of letters or of digits, or a
// single Han/kana character (those scripts do not separate words with spaces).
func forTokens(s string, fn func(string)) {
	var b strings.Builder
	kind := 0 // 1 letters, 2 digits
	flush := func() {
		if b.Len() > 0 {
			fn(b.String())
			b.Reset()
		}
		kind = 0
	}
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r):
			flush()
			fn(string(r))
		case unicode.IsLetter(r):
			if kind != 1 {
				flush()
				kind = 1
			}
			b.WriteRune(unicode.ToLower(r))
		case unicode.IsDigit(r):
			if kind != 2 {
				flush()
				kind = 2
			}
			b.WriteRune(r)
		default:
			flush()
		}
	}
	flush()
}

// lineKey identifies a line across pages: whitespace removed, digits folded (page numbers
// and dates differ between the copies of a running header).
func lineKey(l string) string {
	var b strings.Builder
	for _, r := range l {
		switch {
		case unicode.IsSpace(r):
		case unicode.IsDigit(r):
			b.WriteByte('#')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}
