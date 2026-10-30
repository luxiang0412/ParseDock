package storage

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lim() Limits { return DefaultLimits() }

func TestSniffFormat(t *testing.T) {
	cases := []struct {
		name string
		head []byte
		want string
		code string
	}{
		{"a.pdf", []byte("%PDF-1.7\n..."), "pdf", ""},
		{"a.docx", []byte{'P', 'K', 3, 4, 0, 0}, "docx", ""},
		{"a.xlsx", []byte{'P', 'K', 3, 4, 0, 0}, "xlsx", ""},
		{"a.png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, "png", ""},
		{"a.jpg", []byte{0xFF, 0xD8, 0xFF, 0xE0}, "jpeg", ""},
		{"a.webp", append([]byte("RIFF0000WEBP"), 0), "webp", ""},
		{"a.csv", []byte("region,pallets\nNorth,1\n"), "csv", ""},
		{"a.txt", []byte("hello world\n"), "txt", ""},
		// extension/content mismatches must not be trusted
		{"a.pdf", []byte{'P', 'K', 3, 4}, "pdf", "unsupported_type"},
		{"a.docx", []byte("%PDF-1.7"), "docx", "unsupported_type"},
		{"a.zip", []byte{'P', 'K', 3, 4}, "zip", "unsupported_type"},
		{"a.csv", []byte{0x00, 0x01, 0x02, 0x03}, "csv", "unsupported_type"},
	}
	for _, c := range cases {
		got, err := SniffFormat(c.name, c.head, lim())
		if c.code == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
				continue
			}
			if got.Name != c.want {
				t.Errorf("%s: got %s want %s", c.name, got.Name, c.want)
			}
			continue
		}
		var ve *ValidationError
		if err == nil {
			t.Errorf("%s: expected error %s, got format %s", c.name, c.code, got.Name)
			continue
		}
		if !asValidation(err, &ve) || ve.Code != c.code {
			t.Errorf("%s: got error %v want code %s", c.name, err, c.code)
		}
	}
}

func asValidation(err error, target **ValidationError) bool {
	v, ok := err.(*ValidationError)
	if ok {
		*target = v
	}
	return ok
}

func TestVerifyZipContainer(t *testing.T) {
	dir := t.TempDir()

	writeZip := func(name string, entries map[string][]byte) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		for n, b := range entries {
			w, err := zw.Create(n)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write(b); err != nil {
				t.Fatal(err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}

	good := writeZip("good.docx", map[string][]byte{"word/document.xml": []byte("<w:document/>")})
	if err := VerifyZipContainer(good, MustLookupByName("docx"), lim()); err != nil {
		t.Fatalf("valid docx rejected: %v", err)
	}

	wrongPart := writeZip("wrong.docx", map[string][]byte{"xl/workbook.xml": []byte("<x/>")})
	if err := VerifyZipContainer(wrongPart, MustLookupByName("docx"), lim()); err == nil {
		t.Fatal("docx without word/document.xml was accepted")
	}

	bomb := writeZip("bomb.docx", map[string][]byte{
		"word/document.xml":   []byte("<w:document/>"),
		"word/media/blob.bin": bytes.Repeat([]byte{0}, 8<<20),
	})
	if err := VerifyZipContainer(bomb, MustLookupByName("docx"), lim()); err == nil {
		t.Fatal("compression bomb was accepted")
	}
}

func TestCheckPDF(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	ok := write("ok.pdf", []byte("%PDF-1.7\n1 0 obj << /Type /Page >> endobj\n2 0 obj << /Type /Pages /Count 1 >> endobj\ntrailer << /Root 1 0 R >>\n%%EOF\n"))
	pages, enc, err := CheckPDF(ok, lim())
	if err != nil {
		t.Fatalf("valid pdf rejected: %v", err)
	}
	if pages != 1 {
		t.Fatalf("page count = %d, want 1 (must not count /Type /Pages)", pages)
	}
	if enc {
		t.Fatal("unexpected encrypted flag")
	}

	garbage := write("garbage.pdf", []byte("%PDF-1.7\n"+string(bytes.Repeat([]byte("x"), 100))))
	if _, _, err := CheckPDF(garbage, lim()); err == nil {
		t.Fatal("PDF without structure markers was accepted")
	}

	many := write("many.pdf", append([]byte("%PDF-1.7\n%%EOF\n"),
		bytes.Repeat([]byte("/Type /Page\n"), 600)...))
	small := lim()
	small.MaxPages = 500
	if _, _, err := CheckPDF(many, small); err == nil {
		t.Fatal("page limit was not enforced")
	}
}

func TestStoreAtomicCommitAndDelete(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root, MinFree: 0}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	w, err := s.NewInputWriter("job_test", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	size, sum, err := s.CommitInput(w, "job_test", map[string]string{"name": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if size != 7 || sum == "" {
		t.Fatalf("size=%d sum=%q", size, sum)
	}
	if _, err := os.Stat(s.InputPath("job_test")); err != nil {
		t.Fatalf("input not published: %v", err)
	}
	// No *.part files left behind.
	parts, _ := filepath.Glob(filepath.Join(root, "tmp", "*"))
	if len(parts) != 0 {
		t.Fatalf("staging files left behind: %v", parts)
	}

	m, err := s.CommitResult("job_test", "mock", "1.0", map[string]io.Reader{
		"result.md":  bytes.NewReader([]byte("# hi")),
		"result.txt": bytes.NewReader([]byte("hi")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version == "" || len(m.Files) != 2 {
		t.Fatalf("bad manifest %+v", m)
	}
	loaded, err := s.ReadManifestAt("job_test", m.Version)
	if err != nil || loaded.Version != m.Version {
		t.Fatalf("manifest round trip failed: %v %v", loaded, err)
	}

	if err := s.DeleteJob("job_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.InputDir("job_test")); !os.IsNotExist(err) {
		t.Fatal("input dir still exists")
	}
	// Idempotent.
	if err := s.DeleteJob("job_test"); err != nil {
		t.Fatalf("second delete not idempotent: %v", err)
	}
}

func TestInputWriterLimit(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	w, err := s.NewInputWriter("job_limit", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("abcd")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("e")); err == nil {
		t.Fatal("writer exceeded its limit silently")
	}
	w.Abort()
}

// Regression from the test_data run: a PDF's trailer markers live at the *end*.
// The Phase 0 pre-flight only looked at the first MiB and rejected 30 % of a real
// corpus as "PDF structure markers are missing" (168 of 554 files).
func TestCheckPDFFindsTrailerInTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "trailer-in-tail.pdf")
	head := []byte("%PDF-1.7\n1 0 obj << /Type /Page >> endobj\n")
	filler := bytes.Repeat([]byte("stream-data-"), 200000) // ~2.4 MiB
	tail := []byte("\ntrailer << /Root 1 0 R >>\nstartxref\n12345\n%%EOF\n")
	body := append(append(append([]byte{}, head...), filler...), tail...)
	if err := os.WriteFile(p, body, 0o600); err != nil {
		t.Fatal(err)
	}
	pages, encrypted, err := CheckPDF(p, lim())
	if err != nil {
		t.Fatalf("pre-flight rejected a valid PDF: %v", err)
	}
	if pages < 1 {
		t.Fatalf("page count = %d", pages)
	}
	if encrypted {
		t.Fatal("unexpected encrypted flag")
	}
}

// Real files sometimes carry a short preamble before %PDF- (Adobe tolerates up to
// 1024 bytes). Two files in the test corpus did; they must not be rejected.
func TestPDFHeaderWithinFirst1024(t *testing.T) {
	head := append(bytes.Repeat([]byte("JUNK"), 20), []byte("%PDF-1.7\n%%EOF\n")...)
	f, err := SniffFormat("wrapped.pdf", head, lim())
	if err != nil {
		t.Fatalf("prefixed PDF rejected: %v", err)
	}
	if f.Name != "pdf" {
		t.Fatalf("format = %s", f.Name)
	}
	// A header beyond 1024 bytes stays rejected: that is not a PDF in practice.
	far := append(bytes.Repeat([]byte("x"), 2000), []byte("%PDF-1.7")...)
	if _, err := SniffFormat("far.pdf", far, lim()); err == nil {
		t.Fatal("PDF header beyond 1024 bytes accepted")
	}
}

// A legitimate large Office file must not be classified as a compression bomb,
// while the deflate bomb fixture still must be.
func TestZipBombVersusLegitLargeFile(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, payload []byte) string {
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		zw := zip.NewWriter(f)
		if _, err := zw.Create("word/document.xml"); err != nil {
			t.Fatal(err)
		}
		z, err := zw.Create("word/media/blob.bin")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := z.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// Legit: 100 MiB of pseudo-random data compresses ~1:1 → ratio check passes.
	random := make([]byte, 4<<20)
	for i := range random {
		random[i] = byte((i*2654435761 + 12345) >> 13)
	}
	legit := write("legit.docx", bytes.Repeat(random, 25))
	if err := VerifyZipContainer(legit, MustLookupByName("docx"), lim()); err != nil {
		t.Fatalf("legitimate large archive rejected: %v", err)
	}

	// Bomb: 40 MiB of zeros at ~1000:1.
	bomb := write("bomb.docx", make([]byte, 40<<20))
	if err := VerifyZipContainer(bomb, MustLookupByName("docx"), lim()); err == nil {
		t.Fatal("compression bomb accepted")
	}

	// Expanded-too-large: real file (low ratio) whose total expansion exceeds the
	// configured limit must be reported as a size problem, not as a bomb.
	small := lim()
	small.MaxZipUncompressed = 1 << 20 // 1 MiB
	bombErr := VerifyZipContainer(legit, MustLookupByName("docx"), small)
	if bombErr == nil {
		t.Fatal("expansion limit not enforced")
	}
	var ve *ValidationError
	if !asValidation(bombErr, &ve) || ve.Code != "expanded_too_large" {
		t.Fatalf("error = %v, want expanded_too_large", bombErr)
	}
}

// PR-04 / RC-1: legacy OLE2 Office is accepted by magic and pre-flighted; a
// non-CFB file with a .doc extension and a corrupt CFB header are rejected.
func TestCompoundFileFormats(t *testing.T) {
	ole2 := []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}
	for _, name := range []string{"a.doc", "a.dot", "a.xls", "a.xlt", "a.ppt", "a.pot"} {
		f, err := SniffFormat(name, ole2, lim())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		want := map[string]string{".doc": "doc", ".dot": "doc", ".xls": "xls", ".xlt": "xls",
			".ppt": "ppt", ".pot": "ppt"}[strings.ToLower(filepath.Ext(name))]
		if f.Name != want {
			t.Fatalf("%s: format = %s, want %s", name, f.Name, want)
		}
	}
	// OOXML bytes with a legacy extension are rejected (container mismatch).
	if _, err := SniffFormat("fake.doc", []byte{'P', 'K', 3, 4}, lim()); err == nil {
		t.Fatal("zip content accepted as .doc")
	}
	// Text bytes with a legacy extension are rejected.
	if _, err := SniffFormat("fake.xls", []byte("hello,world\n"), lim()); err == nil {
		t.Fatal("text accepted as .xls")
	}
}

func TestVerifyCompoundFile(t *testing.T) {
	dir := t.TempDir()
	header := func(major, sectorShift byte, byteOrder []byte) []byte {
		h := make([]byte, 512)
		copy(h, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1})
		h[26], h[27] = major, 0
		copy(h[28:30], byteOrder)
		h[30], h[31] = sectorShift, 0
		return h
	}
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("good.doc", header(3, 9, []byte{0xFE, 0xFF}))
	if err := VerifyCompoundFile(good); err != nil {
		t.Fatalf("valid CFB rejected: %v", err)
	}
	good4 := write("good4.doc", header(4, 12, []byte{0xFE, 0xFF}))
	if err := VerifyCompoundFile(good4); err != nil {
		t.Fatalf("v4 CFB rejected: %v", err)
	}
	badOrder := write("badorder.doc", header(3, 9, []byte{0xFF, 0xFE}))
	if err := VerifyCompoundFile(badOrder); err == nil {
		t.Fatal("bad byte order accepted")
	}
	badVersion := write("badver.doc", header(5, 9, []byte{0xFE, 0xFF}))
	if err := VerifyCompoundFile(badVersion); err == nil {
		t.Fatal("unknown CFB major version accepted")
	}
	short := write("short.doc", []byte{0xD0, 0xCF})
	if err := VerifyCompoundFile(short); err == nil {
		t.Fatal("truncated header accepted")
	}
	notCFB := write("plain.doc", bytes.Repeat([]byte("A"), 512))
	if err := VerifyCompoundFile(notCFB); err == nil {
		t.Fatal("non-CFB file accepted")
	}
}
