package api

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A client that stops sending its body is cut off by the read deadline instead of
// holding the handler open forever.
func TestBodyReadDeadlineCutsStalledUpload(t *testing.T) {
	result := make(chan error, 1)
	srv := httptest.NewServer(BodyReadDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		result <- err
	}), 200*time.Millisecond))
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Declare 10 MiB (a "large" upload, so the full timeout applies) and send 5 bytes.
	fmt.Fprintf(conn, "POST /v1/jobs HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\nhello", 10<<20)

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("a stalled body must fail to read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stalled upload was never cut off")
	}
}

// Once the body is read, the deadline is cleared: a slow response (the sync parse waits
// up to two minutes after the upload) must not be cancelled by it.
func TestBodyReadDeadlineClearedAfterBody(t *testing.T) {
	srv := httptest.NewServer(BodyReadDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		time.Sleep(400 * time.Millisecond) // well past the 100 ms deadline
		if r.Context().Err() != nil {
			http.Error(w, "context cancelled", 500)
			return
		}
		_, _ = io.WriteString(w, strings.ToUpper(string(b)))
	}), 100*time.Millisecond))
	defer srv.Close()

	resp, err := http.Post(srv.URL, "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(body) != "HELLO" {
		t.Fatalf("slow response after a complete body: %d %q", resp.StatusCode, body)
	}

	// The connection stays usable for a second request (keep-alive).
	resp2, err := http.Post(srv.URL, "text/plain", strings.NewReader("again"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("second request: %d", resp2.StatusCode)
	}
}

// GETs without a body are untouched, and a disabled timeout is a no-op.
func TestBodyReadDeadlineSkipsBodylessAndDisabled(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
	if h := BodyReadDeadline(inner, 0); fmt.Sprintf("%p", h) != fmt.Sprintf("%p", inner) {
		t.Fatal("timeout 0 must return the handler unchanged")
	}
	srv := httptest.NewServer(BodyReadDeadline(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		if r.Context().Err() != nil {
			http.Error(w, "cancelled", 500)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}), 50*time.Millisecond))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("GET with a long handler: %d", resp.StatusCode)
	}
}

// The gateway's header wrapper must expose the connection to ResponseController.
func TestGatewayHandlerSupportsReadDeadline(t *testing.T) {
	h := &headerWriter{ResponseWriter: httptest.NewRecorder()}
	if h.Unwrap() == nil {
		t.Fatal("headerWriter must unwrap")
	}
}
