package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Review finding: a text file that merely mentions "%PDF-" was identified as a PDF and
// rejected as an extension mismatch.
func TestSniffTextMentioningPDFHeader(t *testing.T) {
	for _, name := range []string{"notes.txt", "notes.md", "data.csv"} {
		head := []byte("This note explains that every PDF starts with %PDF-1.7 in its header.\n")
		f, err := SniffFormat(name, head, lim())
		if err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
		if f.Name == "pdf" {
			t.Fatalf("%s identified as a PDF", name)
		}
	}
	// A real PDF named .txt still does not pass as text.
	if _, err := SniffFormat("a.txt", []byte("%PDF-1.7\n"), lim()); err == nil {
		t.Fatal("PDF content accepted under a .txt name")
	}
}

func TestValidJobID(t *testing.T) {
	for _, ok := range []string{"job_01J9ZK3V8Q4M7XN2PRT5WYHB6C", "job_test", "a", "job-1"} {
		if !ValidJobID(ok) {
			t.Errorf("%q rejected", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "../x", "a/b", `a\b`, "-rf", "job\x00", "job id", strings.Repeat("a", 129)} {
		if ValidJobID(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// Review finding: DeleteJob rejected separators but not ".." or "", and the path helpers
// accepted anything, so "..": inputs/.. is the data root.
func TestPathHelpersNeverEscapeTheirDirectory(t *testing.T) {
	root := t.TempDir()
	s := &Store{Root: root}
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(root, "inputs", "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"..", ".", "", "../inputs"} {
		if err := s.DeleteJob(bad); err == nil {
			t.Errorf("DeleteJob(%q) accepted", bad)
		}
		for _, p := range []string{s.InputDir(bad), s.ResultDir(bad), s.InputPath(bad)} {
			if !strings.Contains(p, "\x00") {
				t.Errorf("path helper for %q returned a usable path %q", bad, p)
			}
		}
		if err := os.RemoveAll(s.ResultDir(bad)); err == nil {
			t.Errorf("RemoveAll on the helper path for %q succeeded", bad)
		}
		if _, err := s.NewInputWriter(bad, 10); err == nil {
			t.Errorf("NewInputWriter(%q) accepted", bad)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("data root damaged: %v", err)
	}
	// An empty version still names the job's result directory (unchanged behaviour).
	if s.ResultDirAt("job_test", "") != s.ResultDir("job_test") {
		t.Fatal("empty version no longer maps to the job result directory")
	}
	if got := s.ResultDirAt("job_test", "sha256:../../x"); !strings.Contains(got, "\x00") {
		t.Fatalf("traversing version produced %q", got)
	}
}
