package storage

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
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
	loaded, err := s.ReadManifest("job_test")
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
