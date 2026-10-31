package api

import (
	"net/http"
	"time"
)

const (
	// DefaultBodyReadTimeout bounds reading one upload body. 10 minutes moves the
	// default 64 MiB upload at ~110 KiB/s, so only a stalled or deliberately trickling
	// client hits it.
	DefaultBodyReadTimeout = 10 * time.Minute
	// smallBodyTimeout applies when the declared body is small (ACK/cancel JSON,
	// worker heartbeats): such a body has no reason to take minutes.
	smallBodyTimeout = time.Minute
	smallBodyBytes   = 1 << 20
)

// BodyReadDeadline bounds how long a client may take to deliver a request body. The
// servers set only ReadHeaderTimeout (a WriteTimeout would cut SSE streams and large
// downloads, and a server-wide ReadTimeout would also count the time spent writing the
// response), so without this a client could hold a connection and a handler open
// indefinitely by trickling an upload.
//
// The deadline is set per request through http.ResponseController. net/http clears
// the connection read deadline itself when the handler reaches the end of the body
// (it starts its background read then), so a long response after a complete upload —
// the synchronous POST /v1/parse waits up to two minutes — is not affected;
// TestBodyReadDeadlineClearedAfterBody pins that behaviour.
//
// timeout <= 0 disables the deadline. Requests without a body are not touched.
func BodyReadDeadline(next http.Handler, timeout time.Duration) http.Handler {
	if timeout <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
			next.ServeHTTP(w, r)
			return
		}
		d := timeout
		if r.ContentLength > 0 && r.ContentLength <= smallBodyBytes && d > smallBodyTimeout {
			d = smallBodyTimeout
		}
		// An unsupported writer (tests, exotic wrappers) leaves nothing to bound.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(d))
		next.ServeHTTP(w, r)
	})
}
