package engines

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"parsedock.dev/parsedock/internal/jobs"
)

func engineCode(t *testing.T, err error) *Error {
	t.Helper()
	var ee *Error
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *engines.Error", err)
	}
	return ee
}

func slowServer(t *testing.T) *httptest.Server {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	return srv
}

// Review finding: a parse deadline was reported as engine_unavailable.
func TestTikaDeadlineIsTimeout(t *testing.T) {
	srv := slowServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := (&Tika{BaseURL: srv.URL}).Parse(ctx, Request{
		Path: writeTemp(t, "a.txt", []byte("hello")), Format: "txt", WorkDir: t.TempDir(),
	})
	if e := engineCode(t, err); e.Code != jobs.ErrTimeout {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, jobs.ErrTimeout)
	}
}

func TestTikaCancelIsCancelled(t *testing.T) {
	srv := slowServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(150 * time.Millisecond); cancel() }()
	_, err := (&Tika{BaseURL: srv.URL}).Parse(ctx, Request{
		Path: writeTemp(t, "a.txt", []byte("hello")), Format: "txt", WorkDir: t.TempDir(),
	})
	if e := engineCode(t, err); e.Code != jobs.ErrCancelled {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, jobs.ErrCancelled)
	}
}

func TestDoclingServeDeadlineIsTimeout(t *testing.T) {
	srv := slowServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err := (&DoclingServe{BaseURL: srv.URL}).Parse(ctx, Request{
		Path: writeTemp(t, "a.pdf", []byte("%PDF-1.7\n%%EOF\n")), Format: "pdf", WorkDir: t.TempDir(),
	})
	if e := engineCode(t, err); e.Code != jobs.ErrTimeout {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, jobs.ErrTimeout)
	}
}

// A refused connection is still an unavailable engine.
func TestTikaUnreachableIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	_, err := (&Tika{BaseURL: url}).Parse(context.Background(), Request{
		Path: writeTemp(t, "a.txt", []byte("hello")), Format: "txt", WorkDir: t.TempDir(),
	})
	if e := engineCode(t, err); e.Code != jobs.ErrEngineUnavailable {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, jobs.ErrEngineUnavailable)
	}
}

// Review finding: output above the ceiling was cut and published as complete.
func TestTikaOversizedOutputIsRejected(t *testing.T) {
	old := tikaMaxOutput
	tikaMaxOutput = 1024
	t.Cleanup(func() { tikaMaxOutput = old })
	srv := fakeTika(t, strings.Repeat("word ", 1000), `{}`)
	_, err := (&Tika{BaseURL: srv.URL}).Parse(context.Background(), Request{
		Path: writeTemp(t, "a.txt", []byte("hello")), Format: "txt", WorkDir: t.TempDir(),
	})
	if e := engineCode(t, err); e.Code != jobs.ErrExpandedTooLarge {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, jobs.ErrExpandedTooLarge)
	}
}

// Review finding: the page count came from /rmeta/text, which embeds the full text and
// overflowed a 4 MiB decode limit on sizeable PDFs. /meta carries only metadata.
func TestTikaPageCountUsesMetadataOnlyEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/tika", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Body text without page breaks.\n")
	})
	mux.HandleFunc("/meta", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"Content-Type":"application/pdf","xmpTPg:NPages":["412"]}`)
	})
	mux.HandleFunc("/rmeta/text", func(w http.ResponseWriter, r *http.Request) {
		t.Error("full-text metadata endpoint must not be used for the page count")
		http.Error(w, "no", http.StatusGone)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	doc, err := (&Tika{BaseURL: srv.URL}).Parse(context.Background(), Request{
		Path: writeTemp(t, "a.pdf", []byte("%PDF-1.7\n%%EOF\n")), Format: "pdf", WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.PageCount != 412 {
		t.Fatalf("page count = %d, want 412", doc.PageCount)
	}
}
