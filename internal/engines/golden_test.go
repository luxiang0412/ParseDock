package engines

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"parsedock.dev/parsedock/internal/documentir"
)

// PR-05: the Docling -> IR mapping is pinned by a golden file. Timings and
// other volatile fields are zeroed before comparison; anything else changing is a
// contract change that must be reviewed (and the schema version bumped if it
// breaks compatibility).
func TestDoclingMappingGolden(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "docling_sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	doc := documentir.New()
	if err := mapDocling(doc, raw, "pdf"); err != nil {
		t.Fatal(err)
	}
	doc.Finalize()
	scrubVolatile(doc)

	got, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("testdata", "docling_sample.golden.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(goldenPath, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("golden file updated")
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden file (run UPDATE_GOLDEN=1 go test ./internal/engines -run TestDoclingMappingGolden): %v", err)
	}
	if string(got)+"\n" != string(want) {
		t.Fatalf("IR mapping drifted from the golden file.\nGot:\n%s\nWant:\n%s", got, want)
	}
}

// scrubVolatile removes fields that legitimately differ per run.
func scrubVolatile(doc *documentir.Document) {
	doc.Metrics.ParseMs = 0
	doc.Metrics.InputBytes = 0
	doc.Metrics.PeakRSSMB = nil
	doc.Metrics.PeakVRAMMB = nil
	doc.Metrics.ModelLoadMs = nil
	doc.Metrics.StartedAt = nil
	doc.Metrics.FinishedAt = nil
}
