package engines

import (
	"archive/zip"
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/documentir"
)

// TestXLSXStreamScale parses a synthetic workbook shaped like the bank ledgers that
// motivated the engine: one sheet, 780k rows x 19 columns, two header rows, a ~20 MiB
// shared-string table and ~550 MiB of worksheet XML. Opt-in (it writes ~60 MB and runs
// for tens of seconds):
//
//	PARSEDOCK_XLSX_STREAM_SCALE=1 go test ./internal/engines/ -run TestXLSXStreamScale -v
func TestXLSXStreamScale(t *testing.T) {
	if os.Getenv("PARSEDOCK_XLSX_STREAM_SCALE") == "" {
		t.Skip("set PARSEDOCK_XLSX_STREAM_SCALE=1 to run the scale check")
	}
	const rows, uniq = 780_000, 400_000
	// bufio errors are sticky: Flush reports any failed write, so the writes are unchecked.
	sst := func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 1<<20)
		fmt.Fprintf(bw, `<sst count="%d" uniqueCount="%d">`, rows*4, uniq)
		for i := 0; i < uniq; i++ {
			fmt.Fprintf(bw, `<si><t>REF-%08d-%s</t></si>`, i, strings.Repeat("x", i%17))
		}
		_, _ = bw.WriteString(`</sst>`)
		return bw.Flush()
	}
	sheet := func(w io.Writer) error {
		bw := bufio.NewWriterSize(w, 1<<20)
		_, _ = bw.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><dimension ref="A1:S780002"/><sheetData>`)
		for h := 1; h <= 2; h++ {
			fmt.Fprintf(bw, `<row r="%d">`, h)
			for c := 0; c < 19; c++ {
				fmt.Fprintf(bw, `<c r="%s%d" t="inlineStr"><is><t>Header %d-%d</t></is></c>`, columnName(c), h, h, c)
			}
			_, _ = bw.WriteString(`</row>`)
		}
		for i := 0; i < rows; i++ {
			r := i + 3
			fmt.Fprintf(bw, `<row r="%d" spans="1:19">`, r)
			for c := 0; c < 19; c++ {
				ref := columnName(c)
				switch c % 5 {
				case 0, 3:
					fmt.Fprintf(bw, `<c r="%s%d" t="s"><v>%d</v></c>`, ref, r, (i*7+c)%uniq)
				case 1:
					fmt.Fprintf(bw, `<c r="%s%d" s="1"><v>%d</v></c>`, ref, r, 44562+i%365)
				case 2:
					fmt.Fprintf(bw, `<c r="%s%d"><v>%d.%02d</v></c>`, ref, r, (i*31+c)%1000000, i%100)
				default:
					if i%4 != 0 {
						fmt.Fprintf(bw, `<c r="%s%d" t="s"><v>%d</v></c>`, ref, r, (i+c)%uniq)
					}
				}
			}
			_, _ = bw.WriteString(`</row>`)
		}
		_, _ = bw.WriteString(`</sheetData></worksheet>`)
		return bw.Flush()
	}
	start := time.Now()
	p := writeXLSX(t,
		xlsxPart{"xl/workbook.xml", literal(`<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="Planilha1" sheetId="1" r:id="rId1"/></sheets></workbook>`)},
		xlsxPart{"xl/_rels/workbook.xml.rels", literal(`<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`)},
		xlsxPart{"xl/styles.xml", literal(testStyles)},
		xlsxPart{"xl/sharedStrings.xml", sst},
		xlsxPart{"xl/worksheets/sheet1.xml", sheet},
	)
	fi, _ := os.Stat(p)
	var expanded uint64
	if zr, err := zip.OpenReader(p); err == nil {
		for _, f := range zr.File {
			expanded += f.UncompressedSize64
		}
		zr.Close()
	}
	t.Logf("fixture: %d MiB zip, %d MiB expanded, written in %s", fi.Size()>>20, expanded>>20, time.Since(start).Round(time.Second))

	runtime.GC()
	start = time.Now()
	doc, err := (&XLSXStream{}).Parse(context.Background(), Request{Path: p, Format: "xlsx"})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	if err := documentir.DefaultLimits.Validate(doc); err != nil {
		t.Fatalf("IR limits: %v", err)
	}
	md := documentir.RenderMarkdown(doc)
	t.Logf("parse: %s, heap in use %d MiB, sys %d MiB, peak RSS %s, markdown %d KiB, tables %d",
		elapsed.Round(time.Millisecond), ms.HeapInuse>>20, ms.Sys>>20, peakRSS(), len(md)>>10, len(doc.Tables))
	if len(doc.Tables) != 2 || !strings.Contains(md, "780,002 rows x 19 columns") {
		t.Fatalf("unexpected summary:\n%s", md[:min(len(md), 2000)])
	}
}

func peakRSS() string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "n/a"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "VmHWM:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "VmHWM:"))
		}
	}
	return "n/a"
}
