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
