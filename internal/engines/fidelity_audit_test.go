package engines

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
)

// TestXbergMarkdownAudit measures two decisions on every synthetic PDF
// (benchmark/fixtures/generated + benchmark/fixtures/corpus) against xberg's own plain
// output. Like TestMarkdownFidelityReport it is a measurement, skipped unless
// PARSEDOCK_FIDELITY=1 and xberg is installed.
//
//  1. joinSplitParagraphs: every adjacent paragraph pair of the Markdown content is a
//     candidate; the ground truth is whether the plain content keeps the two in one
//     block (no blank line between them). Reports true/false joins and misses.
//  2. Multi-column reading order: whether xberg keeps the left column before the right
//     one on the two-column fixtures, and how often a cheap overlap signal (a lower-case
//     word glued to a capitalised one, "movedLeft") fires on every other PDF.
func TestXbergMarkdownAudit(t *testing.T) {
	if os.Getenv("PARSEDOCK_FIDELITY") != "1" {
		t.Skip("set PARSEDOCK_FIDELITY=1 to run the audit against an installed xberg")
	}
	root, _ := filepath.Abs("../..")
	engineDir := filepath.Join(root, "runtime", "engines")
	x := &Xberg{Bin: filepath.Join(engineDir, "xberg-cli", "xberg"), LibDir: filepath.Join(engineDir, "xberg-cli")}
	if err := x.Probe(context.Background()); err != nil {
		t.Skipf("xberg not installed: %v", err)
	}
	var pdfs []string
	for _, dir := range []string{"generated", "corpus"} {
		m, _ := filepath.Glob(filepath.Join(root, "benchmark", "fixtures", dir, "*.pdf"))
		pdfs = append(pdfs, m...)
	}
	sort.Strings(pdfs)
	run := func(path, contentFormat string) *xbergOutput {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		args := []string{"extract", "--no-config-discovery", "--format", "json", "--content-format", contentFormat,
			"--no-cache", "true", "--ocr", "false", path}
		res, err := RunCLI(ctx, x.spec(), t.TempDir(), args...)
		if err != nil {
			t.Fatalf("xberg %s: %v", filepath.Base(path), err)
		}
		var out xbergOutput
		if err := json.Unmarshal(res.Stdout, &out); err != nil {
			t.Fatal(err)
		}
		return &out
	}
	var tp, fp, fn, tn, pairs int
	var fpExamples, fnExamples []string
	glue := regexp.MustCompile(`\b[a-z]{2,}[A-Z][a-z]{2,}\b`)
	type colResult struct{ name, order, glued string }
	var cols []colResult
	signalFires, signalOther := 0, 0
	for _, path := range pdfs {
		md, plain := run(path, "markdown"), run(path, "plain")
		name := filepath.Base(path)
		if len(md.Result.Pages) != len(plain.Result.Pages) {
			t.Fatalf("%s: page count differs between content formats", name)
		}
		for pi, page := range md.Result.Pages {
			if page.OCRConfidence.Backend != "" || page.OCRConfidence.Score != nil {
				continue
			}
			scratch := documentir.New()
			LiftMarkdown(scratch, page.Content)
			var plainBlocks []string
			for _, b := range strings.Split(plain.Result.Pages[pi].Content, "\n\n") {
				plainBlocks = append(plainBlocks, normCell(b))
			}
			for i := 0; i+1 < len(scratch.Elements); i++ {
				a, b := &scratch.Elements[i], &scratch.Elements[i+1]
				if a.Type != documentir.ElParagraph || b.Type != documentir.ElParagraph {
					continue
				}
				pairs++
				junction := tailWords(normCell(a.Text), 4) + " " + headWords(normCell(b.Text), 4)
				together := false
				for _, pb := range plainBlocks {
					if strings.Contains(pb, junction) {
						together = true
						break
					}
				}
				joined := canJoinParagraphs(a, b)
				switch {
				case joined && together:
					tp++
				case joined && !together:
					fp++
					fpExamples = append(fpExamples, name+": "+junction)
				case !joined && together:
					fn++
					fnExamples = append(fnExamples, name+": "+junction)
				default:
					tn++
				}
			}
		}
		plainText := plain.Result.Content
		fires := glue.MatchString(plainText)
		if strings.Contains(name, "two_column") {
			order := "left->right"
			if !inOrder(plainText, []string{"Left column first", "Right column first"}) {
				order = "INTERLEAVED/right-first"
			}
			g := "no"
			if fires {
				g = "yes: " + glue.FindString(plainText)
				signalFires++
			}
			cols = append(cols, colResult{name, order, g})
		} else if fires {
			signalOther++
			t.Logf("overlap signal false positive on %s: %q", name, glue.FindString(plainText))
		}
	}
	t.Logf("paragraph repair over %d PDFs, %d adjacent paragraph pairs: joined correctly %d, joined wrongly %d, missed %d, kept apart correctly %d",
		len(pdfs), pairs, tp, fp, fn, tn)
	for _, e := range fpExamples {
		t.Logf("  wrong join: %q", e)
	}
	for i, e := range fnExamples {
		if i >= 10 {
			t.Logf("  ... %d more misses", len(fnExamples)-10)
			break
		}
		t.Logf("  missed: %q", e)
	}
	twoCol := 0
	for _, c := range cols {
		twoCol++
		t.Logf("two-column %s: xberg column order %s; overlap signal %s", c.name, c.order, c.glued)
	}
	t.Logf("overlap signal: fires on %d/%d two-column PDFs and on %d/%d other PDFs", signalFires, twoCol, signalOther, len(pdfs)-twoCol)
	if fp > 0 {
		t.Errorf("paragraph repair joined %d pairs that xberg's plain output keeps apart", fp)
	}
}

func tailWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[len(f)-n:]
	}
	return strings.Join(f, " ")
}

func headWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}
