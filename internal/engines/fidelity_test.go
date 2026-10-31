package engines

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
)

// TestMarkdownFidelityReport drives the real engine adapters over the synthetic
// fixtures and compares the structure of the result with the structure the fixture
// generator wrote (benchmark/scripts/gen_fixtures.py). It is a measurement, not a
// unit test: it is skipped unless PARSEDOCK_FIDELITY=1 and the engines are installed
// under runtime/engines (scripts/ci-install-engines.sh).
//
//	PARSEDOCK_FIDELITY=1 go test ./internal/engines -run TestMarkdownFidelityReport -v
//
// PARSEDOCK_FIDELITY_OUT=<dir> also writes each rendered Markdown file there.
//
// The comparison is deliberately coarse and engine-neutral: the element sequence is
// reduced to tokens (H<level>, P, LI, T, CODE, QUOTE), consecutive paragraphs are
// collapsed into one P (engines legitimately disagree on paragraph splitting), and
// the result is compared with the expected sequence by edit distance.
func TestMarkdownFidelityReport(t *testing.T) {
	if os.Getenv("PARSEDOCK_FIDELITY") != "1" {
		t.Skip("set PARSEDOCK_FIDELITY=1 to run the fidelity report against installed engines")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	engineDir := filepath.Join(root, "runtime", "engines")
	xb := &Xberg{
		Bin: filepath.Join(engineDir, "xberg-cli", "xberg"), LibDir: filepath.Join(engineDir, "xberg-cli"),
		VersionS: "xberg", OCR: true, OCRLang: "eng", Timeout: 5 * time.Minute,
	}
	dr := &DoclingRS{
		Bin: filepath.Join(engineDir, "docling-rs", "docling-rs"), ModelRoot: filepath.Join(engineDir, "docling-rs"),
		VersionS: "docling-rs", OCR: true, OCRLang: "en", Timeout: 5 * time.Minute,
	}
	for _, e := range []Engine{xb, dr} {
		if err := e.Probe(context.Background()); err != nil {
			t.Skipf("%s not installed: %v", e.Name(), err)
		}
	}
	nat := &Native{}
	out := os.Getenv("PARSEDOCK_FIDELITY_OUT")

	type fixture struct {
		file, format string
		want         string // expected collapsed token sequence
		paras        int    // paragraphs the source defines
		absent       string // text that must not reach the Markdown body (furniture)
		order        []string
		inline       []string // Markdown snippets that must appear verbatim
	}
	fixtures := []fixture{
		{file: "docx_report.docx", format: "docx", want: "H1 P H2 LI LI LI LI H2 T P", paras: 2, absent: "ParseDock benchmark fixture"},
		{file: "pdf_text_en_3p.pdf", format: "pdf", want: "H1 H2 P T H2 P H2 P", paras: 6},
		{file: "pdf_text_zh_2p.pdf", format: "pdf", want: "H1 H2 P T H2 P", paras: 4},
		{file: "pdf_two_column_en.pdf", format: "pdf", want: "H1 P", paras: 4,
			order: []string{"Left column first", "Left column second", "Right column first", "Right column second"}},
		{file: "xlsx_workbook.xlsx", format: "xlsx", want: "H2 T H2 T"},
		{file: "md_notes.md", format: "md", want: "H1 P H2 T H2 LI LI", paras: 1},
		{file: "csv_utf8.csv", format: "csv", want: "T"},
		{file: "legacy_word6.doc", format: "doc", want: "P", paras: 1},
		{file: "legacy_simple.xls", format: "xls", want: "H2 T H2 H2"},
	}
	if extra := os.Getenv("PARSEDOCK_FIDELITY_EXTRA"); extra != "" {
		// Ad-hoc probes: path=format=paragraphs=expected tokens, separated by ';'.
		for _, spec := range strings.Split(extra, ";") {
			parts := strings.SplitN(spec, "=", 4)
			if len(parts) == 4 {
				n := 0
				_, _ = fmt.Sscanf(parts[2], "%d", &n)
				var inline []string
				if m := os.Getenv("PARSEDOCK_FIDELITY_EXTRA_INLINE"); m != "" {
					inline = strings.Split(m, "|")
				}
				fixtures = append(fixtures, fixture{file: parts[0], format: parts[1], paras: n, want: parts[3], inline: inline})
			}
		}
	}

	type row struct {
		fixture, mode, engine   string
		headings, lists, tables string
		paras                   string
		order                   string
		similarity              float64
		leak                    string
		inline                  string
		got                     string
	}
	var rows []row
	for _, fx := range fixtures {
		path := fx.file
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, "benchmark", "fixtures", "generated", fx.file)
		}
		type run struct {
			mode string
			eng  Engine
		}
		var runs []run
		switch fx.format {
		case "md", "csv", "tsv", "txt":
			// The coordinator routes text formats to native in every mode.
			runs = []run{{"fast/auto/accurate", nat}}
		default:
			// fast and auto both route PDFs and Office files to xberg (auto escalates
			// only on OCR evidence, which a text-layer fixture never produces).
			runs = []run{{"fast/auto", xb}, {"accurate", dr}}
		}
		for _, r := range runs {
			doc, err := r.eng.Parse(context.Background(), Request{
				Path: path, Format: fx.format, Mode: strings.Split(r.mode, "/")[0], WorkDir: t.TempDir(),
				Timeout: 5 * time.Minute,
			})
			if err != nil {
				rows = append(rows, row{fixture: filepath.Base(fx.file), mode: r.mode, engine: r.eng.Name(), got: "ERROR " + err.Error()})
				continue
			}
			doc.DropTableShadows()
			doc.Finalize()
			md := documentir.RenderMarkdown(doc)
			if out != "" {
				_ = os.MkdirAll(out, 0o750)
				_ = os.WriteFile(filepath.Join(out, filepath.Base(fx.file)+"."+r.eng.Name()+".md"), []byte(md), 0o600)
			}
			got := fidelityTokens(doc)
			want := strings.Fields(fx.want)
			hg, hw := countPrefix(got, "H"), countPrefix(want, "H")
			lg, lw := countTok(got, "LI"), countTok(want, "LI")
			tg, tw := countTok(got, "T"), countTok(want, "T")
			sim := 1 - float64(tokenDistance(got, want))/float64(maxLen(len(got), len(want)))
			ord := "exact"
			if strings.Join(got, " ") != fx.want {
				ord = "differs"
			}
			if len(fx.order) > 0 {
				if inOrder(md, fx.order) && !interleaved(md) {
					ord += "; markers in order"
				} else {
					ord += "; markers interleaved"
				}
			}
			leak := "-"
			if fx.absent != "" {
				leak = "no"
				if strings.Contains(md, fx.absent) {
					leak = "YES"
				}
			}
			inl := "-"
			if len(fx.inline) > 0 {
				found := 0
				for _, m := range fx.inline {
					if strings.Contains(md, m) {
						found++
					}
				}
				inl = fmt.Sprintf("%d/%d", found, len(fx.inline))
			}
			rows = append(rows, row{
				inline: inl, fixture: filepath.Base(fx.file), mode: r.mode, engine: r.eng.Name(),
				headings: fmt.Sprintf("%d/%d", hg, hw), lists: fmt.Sprintf("%d/%d", lg, lw),
				tables: fmt.Sprintf("%d/%d", tg, tw), paras: fmt.Sprintf("%d/%d", countParagraphs(doc), fx.paras), order: ord, similarity: sim, leak: leak,
				got: strings.Join(got, " "),
			})
		}
	}
	var b strings.Builder
	b.WriteString("| fixture | mode | engine | headings | list items | tables | paragraphs | sequence | similarity | furniture in body | inline markup | got |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, r := range rows {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %.2f | %s | %s | `%s` |\n",
			r.fixture, r.mode, r.engine, r.headings, r.lists, r.tables, r.paras, r.order, r.similarity, r.leak, r.inline, r.got)
	}
	t.Log("\n" + b.String())
}

// fidelityTokens reduces a document to its structural skeleton. Furniture (page
// headers/footers) is excluded: it is not part of the body the source defines.
func fidelityTokens(d *documentir.Document) []string {
	var toks []string
	push := func(tok string) {
		if tok == "P" && len(toks) > 0 && toks[len(toks)-1] == "P" {
			return
		}
		toks = append(toks, tok)
	}
	for _, el := range d.Elements {
		if el.Metadata["content_layer"] == "furniture" {
			continue
		}
		switch el.Type {
		case documentir.ElTitle, documentir.ElSection:
			push(fmt.Sprintf("H%d", maxInt(el.Level, 1)))
		case documentir.ElListItem:
			push("LI")
		case documentir.ElTable:
			push("T")
		case documentir.ElCode:
			push("CODE")
		case documentir.ElHeader, documentir.ElFooter, documentir.ElPageBreak, documentir.ElList:
		default:
			if el.Metadata["markdown"] == "blockquote" {
				push("QUOTE")
				continue
			}
			if strings.TrimSpace(el.Text) != "" {
				push("P")
			}
		}
	}
	return toks
}

func countParagraphs(d *documentir.Document) int {
	n := 0
	for _, el := range d.Elements {
		if el.Type == documentir.ElParagraph && el.Metadata["content_layer"] != "furniture" && strings.TrimSpace(el.Text) != "" {
			n++
		}
	}
	return n
}

func countTok(toks []string, tok string) int {
	n := 0
	for _, t := range toks {
		if t == tok {
			n++
		}
	}
	return n
}

func countPrefix(toks []string, p string) int {
	n := 0
	for _, t := range toks {
		if strings.HasPrefix(t, p) {
			n++
		}
	}
	return n
}

func maxLen(a, b int) int {
	if a > b {
		return a
	}
	if b == 0 {
		return 1
	}
	return b
}

func tokenDistance(a, b []string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(minInt(prev[j]+1, cur[j-1]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func inOrder(s string, markers []string) bool {
	pos := -1
	for _, m := range markers {
		i := strings.Index(s, m)
		if i < 0 || i < pos {
			return false
		}
		pos = i
	}
	return true
}

var interleaveRe = regexp.MustCompile(`program\s+moved\s+128,400`)

// interleaved reports the overlap symptom of the two-column fixture: the first
// sentence of the left column is split by text drawn on top of it.
func interleaved(s string) bool {
	return !interleaveRe.MatchString(s)
}
