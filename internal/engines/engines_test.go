package engines

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
	"parsedock.dev/parsedock/internal/jobs"
)

func TestLiftMarkdownStructure(t *testing.T) {
	md := strings.Join([]string{
		"# Title",
		"",
		"Intro paragraph.",
		"",
		"## Findings",
		"",
		"- first",
		"  - nested",
		"",
		"| Region | Pallets |",
		"| --- | --- |",
		"| North | 48120 |",
		"",
		"```go",
		"fmt.Println()",
		"```",
		"",
		"![diagram](images/x.png)",
		"",
		"---",
		"",
		"Tail paragraph.",
	}, "\n")
	doc := documentir.New()
	LiftMarkdown(doc, md)

	var types []string
	for _, el := range doc.Elements {
		types = append(types, el.Type)
	}
	want := []string{"title", "paragraph", "section", "list_item", "list_item", "table", "code", "image", "page_break", "paragraph"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("element types\n got %v\nwant %v", types, want)
	}
	if doc.Elements[3].Level != 1 || doc.Elements[4].Level != 2 {
		t.Fatalf("list levels lost: %+v %+v", doc.Elements[3], doc.Elements[4])
	}
	if len(doc.Tables) != 1 || doc.Tables[0].HeaderRows != 1 || doc.Tables[0].Rows != 2 || doc.Tables[0].Cols != 2 {
		t.Fatalf("table not lifted: %+v", doc.Tables)
	}
	if doc.Tables[0].Cells[2].Text != "North" {
		t.Fatalf("cell content wrong: %+v", doc.Tables[0].Cells)
	}
	// No fabricated geometry.
	for _, el := range doc.Elements {
		if el.Loc != nil && el.Loc.BBox != nil {
			t.Fatal("markdown lifting invented a bbox")
		}
	}
}

func TestNativeEncodings(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"utf8.txt", []byte("héllo wörld"), "héllo wörld"},
		{"bom.txt", append([]byte{0xEF, 0xBB, 0xBF}, []byte("bom text")...), "bom text"},
		{"utf16le.txt", []byte{0xFF, 0xFE, 'h', 0, 'i', 0}, "hi"},
		{"gb18030.csv", []byte{0xC7, 0xF8, 0xD3, 0xF2, ',', 'a', '\n'}, "区域,a"},
	}
	eng := &Native{}
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		if err := os.WriteFile(p, c.data, 0o600); err != nil {
			t.Fatal(err)
		}
		format := "txt"
		if strings.HasSuffix(c.name, ".csv") {
			format = "csv"
		}
		doc, err := eng.Parse(context.Background(), Request{
			Path: p, Format: format, WorkDir: dir, JobID: "job_x",
		})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !strings.Contains(doc.Elements[0].Text, c.want) && !strings.Contains(c.want, "区域") {
			t.Fatalf("%s: text = %q want %q", c.name, doc.Elements[0].Text, c.want)
		}
		if strings.Contains(c.want, "区域") {
			if len(doc.Tables) != 1 || doc.Tables[0].Cells[0].Text != "区域" {
				t.Fatalf("gb18030 header not decoded: %+v", doc.Tables)
			}
		}
	}
}

func TestNativeCSVDelimiterAndTypes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "eu.csv")
	if err := os.WriteFile(p, []byte("region;pallets\nNorth;48120\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := (&Native{}).Parse(context.Background(), Request{Path: p, Format: "csv", WorkDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	tbl := doc.Tables[0]
	if tbl.Cols != 2 || tbl.Rows != 2 {
		t.Fatalf("delimiter not sniffed: %+v", tbl)
	}
	if tbl.Cells[3].DataType != "number" {
		t.Fatalf("numeric cell typed %q", tbl.Cells[3].DataType)
	}
}

func TestMockDeterminismAndFailure(t *testing.T) {
	dir := t.TempDir()
	eng := &Mock{}
	doc, err := eng.Parse(context.Background(), Request{JobID: "job_m", Format: "pdf", WorkDir: dir, Timeout: 5e9})
	if err != nil {
		t.Fatal(err)
	}
	if doc.ParserEngine != "mock" || len(doc.Elements) < 2 {
		t.Fatalf("bad mock output %+v", doc)
	}

	t.Setenv("PARSEDOCK_MOCK_FAIL", "malformed_file")
	t.Setenv("PARSEDOCK_MOCK_RETRY", "0")
	_, err = eng.Parse(context.Background(), Request{JobID: "job_m", Format: "pdf", WorkDir: dir, Timeout: 5e9})
	var ee *Error
	if err == nil || !asEngineError(err, &ee) || ee.Code != jobs.ErrMalformed || ee.Retryable {
		t.Fatalf("mock failure not classified: %v", err)
	}

	t.Setenv("PARSEDOCK_MOCK_FAIL", "internal_error")
	t.Setenv("PARSEDOCK_MOCK_RETRY", "1")
	_, err = eng.Parse(context.Background(), Request{JobID: "job_m", Format: "pdf", WorkDir: dir, Timeout: 5e9})
	if err == nil || !asEngineError(err, &ee) || !ee.Retryable {
		t.Fatalf("retryable classification lost: %v", err)
	}
}

func asEngineError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func TestRegistryPick(t *testing.T) {
	reg := NewRegistry(
		&Native{},
		&Mock{NameOverride: "fast-pdf", VersionStr: "1", DeviceStr: "cpu"},
	)
	// native handles text; pdf falls to the only engine that declares "*"
	if e, err := reg.Pick("auto", "csv", "fast"); err != nil || e.Name() != "native" {
		t.Fatalf("csv pick = %v, %v", e, err)
	}
	if e, err := reg.Pick("fast-pdf", "pdf", "auto"); err != nil || e.Name() != "fast-pdf" {
		t.Fatalf("pdf pick = %v, %v", e, err)
	}
	reg2 := NewRegistry(&Native{})
	if _, err := reg2.Pick("auto", "pdf", "fast"); err == nil {
		t.Fatal("unsupported format was accepted")
	}
}

func TestSandboxUnavailableIsExplicit(t *testing.T) {
	if err := checkUnshare(context.Background()); err != nil {
		// Expected in restricted containers; the error must be actionable.
		if !strings.Contains(err.Error(), "unshare") && !strings.Contains(err.Error(), "operation not permitted") {
			t.Fatalf("unexpected sandbox error: %v", err)
		}
	}
}

// Real-corpus failure messages: the classifier must surface the cause, not the
// last 400 bytes of log noise (which hid "Archive too large" behind a WARN line).
func TestClassifyCLIError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		code string
		want string
	}{
		{
			name: "xberg archive too large, behind a warn line",
			in: "2026-10-30T10:04:03Z  WARN xberg::concurrency: detected 24 CPU cores but no max_threads set; capping at 8\n" +
				"Error: Failed to extract input 'x.xlsx'\n\nCaused by:\n    0: Security violation: Archive too large: 743948692 bytes (max: 524288000 bytes)\n    1: Archive too large: 743948692 bytes (max: 524288000 bytes)\n",
			code: jobs.ErrExpandedTooLarge,
			want: "Archive too large",
		},
		{
			name: "docling-rs skipped every sheet",
			in:   "docling: sheet \"Planilha1\": used area 709570×19 cells (13481830) exceeds DOCLING_RS_SHEET_MAX_CELLS (10000000); sheet skipped\n",
			code: jobs.ErrExpandedTooLarge,
			want: "DOCLING_RS_SHEET_MAX_CELLS",
		},
		{
			name: "timeout",
			in:   "Error: timed out after 900s",
			code: jobs.ErrTimeout,
			want: "timed out",
		},
		{
			name: "encrypted",
			in:   "Error: document is password protected",
			code: jobs.ErrEncrypted,
			want: "password",
		},
		{
			name: "generic failure keeps the error text",
			in:   "Traceback (most recent call last):\n  ...\nValueError: unexpected end of data",
			code: jobs.ErrMalformed,
			want: "unexpected end of data",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, msg := ClassifyCLIError(c.in)
			if code != c.code {
				t.Fatalf("code = %s, want %s", code, c.code)
			}
			if !strings.Contains(msg, c.want) {
				t.Fatalf("message %q does not contain %q", msg, c.want)
			}
			if strings.Contains(msg, "capping at 8") {
				t.Fatalf("log noise leaked into the error: %q", msg)
			}
			if len(msg) > 700 {
				t.Fatalf("message not truncated: %d bytes", len(msg))
			}
		})
	}
}

// Audit (P1): a plain-text file with a single very long line used to be truncated silently
// at the bufio.Scanner token limit.
func TestLongLineIsNotTruncated(t *testing.T) {
	long := strings.Repeat("x", 2<<20) // 2 MiB, above the old 1 MiB token limit
	doc, err := (&Native{}).Parse(context.Background(), Request{
		Format: "txt", Path: writeTemp(t, "long.txt", []byte(long)),
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var got int
	for _, e := range doc.Elements {
		got += len(e.Text)
	}
	if got != len(long) {
		t.Fatalf("text truncated: kept %d of %d bytes", got, len(long))
	}
}

// Audit (P2): the output limiter must keep draining past the ceiling and report truncation,
// instead of returning a short write that stalls the child on a full pipe.
func TestLimitedWriterDrainsPastLimit(t *testing.T) {
	res, err := RunCLI(context.Background(), CLISpec{Bin: "/bin/sh", MaxOutput: 1024},
		t.TempDir(), "-c", "head -c 100000 /dev/zero | tr '\\0' 'a'")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !res.Truncated {
		t.Fatal("truncation was not reported")
	}
	if len(res.Stdout) != 1024 {
		t.Fatalf("kept %d bytes, want 1024", len(res.Stdout))
	}
}

// Audit (P1): two Markdown tables with the same row count used to receive the same table ID;
// the text renderer resolves a duplicate to the first table and Markdown to the last, so one
// table's content disappeared from one of the two outputs.
func TestMarkdownTablesGetUniqueIDs(t *testing.T) {
	md := "| a | b |\n| --- | --- |\n| 1 | 2 |\n\n" +
		"| c | d |\n| --- | --- |\n| 3 | 4 |\n"
	doc := documentir.New()
	LiftMarkdown(doc, md)
	if len(doc.Tables) != 2 {
		t.Fatalf("tables = %d", len(doc.Tables))
	}
	if doc.Tables[0].ID == doc.Tables[1].ID {
		t.Fatalf("duplicate table id %q", doc.Tables[0].ID)
	}
	for _, out := range []string{documentir.RenderText(doc), documentir.RenderMarkdown(doc)} {
		for _, want := range []string{"a", "1", "c", "3"} {
			if !strings.Contains(out, want) {
				t.Fatalf("rendered output lost %q:\n%s", want, out)
			}
		}
	}
}

// Audit (P1): merging a fragment must rename its tables and every reference to them.
func TestRemapTablesRewritesReferences(t *testing.T) {
	frag := documentir.New()
	frag.Tables = append(frag.Tables, documentir.Table{ID: "tbl_1", Rows: 1, Cols: 1,
		Cells: []documentir.Cell{{Row: 0, Col: 0, RowSpan: 1, ColSpan: 1, Text: "x"}}})
	frag.Elements = append(frag.Elements,
		documentir.Element{ID: "el_1", Type: documentir.ElTable, TableID: "tbl_1"},
		documentir.Element{ID: "el_2", Type: documentir.ElParagraph, Text: "after"})
	remapTables(frag, 5, 10) // five tables, ten elements already in the document
	if frag.Tables[0].ID != "tbl_6" {
		t.Fatalf("table id = %q", frag.Tables[0].ID)
	}
	if frag.Elements[0].TableID != "tbl_6" {
		t.Fatalf("element reference = %q", frag.Elements[0].TableID)
	}
	if frag.Elements[0].ID != "el_11" {
		t.Fatalf("element id = %q, want el_11 (document namespace)", frag.Elements[0].ID)
	}
}
