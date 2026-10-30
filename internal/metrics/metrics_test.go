package metrics

import (
	"bytes"
	"strings"
	"sync"
	"testing"
)

func TestExpositionFormat(t *testing.T) {
	r := New()
	jobs := r.Counter("parsedock_jobs_created_total", "Jobs accepted.", "mode", "format")
	jobs.With("auto", "pdf").Inc()
	jobs.With("fast", "pdf").Add(3)
	q := r.Gauge("parsedock_queue_depth", "Queued jobs.")
	q.With().Set(7)
	h := r.Histogram("parsedock_parse_seconds", "Parse time.")
	h.With().Observe(0.2)
	h.With().Observe(5)

	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"# HELP parsedock_jobs_created_total Jobs accepted.",
		"# TYPE parsedock_jobs_created_total counter",
		`parsedock_jobs_created_total{mode="auto",format="pdf"} 1`,
		`parsedock_jobs_created_total{mode="fast",format="pdf"} 3`,
		"# TYPE parsedock_queue_depth gauge",
		"parsedock_queue_depth 7",
		"# TYPE parsedock_parse_seconds histogram",
		`parsedock_parse_seconds_bucket{le="0.25"} 1`,
		`parsedock_parse_seconds_bucket{le="5"} 2`,
		`parsedock_parse_seconds_bucket{le="+Inf"} 2`,
		"parsedock_parse_seconds_count 2",
		"parsedock_parse_seconds_sum 5.2",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("exposition missing %q\n%s", want, out)
		}
	}
	// Families are emitted in name order: a stable contract for diffing.
	if strings.Index(out, "parsedock_jobs_created_total") > strings.Index(out, "parsedock_queue_depth") {
		t.Fatalf("families not sorted:\n%s", out)
	}
}

func TestLabelEscaping(t *testing.T) {
	r := New()
	c := r.Counter("c_total", "help", "engine")
	c.With(`weird"engine`).Inc()
	var buf bytes.Buffer
	if err := r.WriteText(&buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `engine="weird\"engine"`) {
		t.Fatalf("label not escaped: %s", buf.String())
	}
}

func TestConcurrentUpdates(t *testing.T) {
	r := New()
	c := r.Counter("c_total", "help", "l")
	g := r.Gauge("g", "help")
	h := r.Histogram("h", "help")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c.With("x").Inc()
			g.With().Add(1)
			h.With().Observe(0.1)
		}(i)
	}
	wg.Wait()
	var buf bytes.Buffer
	_ = r.WriteText(&buf)
	out := buf.String()
	if !strings.Contains(out, "c_total{l=\"x\"} 50") || !strings.Contains(out, "g 50") ||
		!strings.Contains(out, "h_count 50") {
		t.Fatalf("lost updates:\n%s", out)
	}
}

func TestWrongLabelCountPanics(t *testing.T) {
	r := New()
	c := r.Counter("c", "help", "a", "b")
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for the wrong label count")
		}
	}()
	c.With("only-one")
}
