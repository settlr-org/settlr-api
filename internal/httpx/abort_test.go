package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func chain(h http.Handler) http.Handler {
	return Recover(Logger(RequestID(h)))
}

// A handler that aborts after sending data must fail at the client: Recover may not
// turn the abort into a JSON 500 appended to a 200 body.
func TestRecoverPropagatesAbortHandler(t *testing.T) {
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "a,b\n1,2\n")
		_ = http.NewResponseController(w).Flush()
		panic(http.ErrAbortHandler)
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		return // connection reset before headers: also a failure the client sees
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err == nil {
		t.Fatalf("truncated body read cleanly (status %d): %q", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "INTERNAL") {
		t.Fatalf("JSON 500 appended to the stream: %q", body)
	}
}

func TestRecoverStillReturns500ForOtherPanics(t *testing.T) {
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom") })))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// Flush and write deadlines must reach the real connection through the middleware chain.
func TestFlushAndDeadlineThroughMiddlewareChain(t *testing.T) {
	var flushErr, deadlineErr error
	released := make(chan struct{})
	srv := httptest.NewServer(chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		deadlineErr = rc.SetWriteDeadline(time.Now().Add(time.Minute))
		_, _ = io.WriteString(w, "first\n")
		flushErr = rc.Flush()
		<-released // the client must already have the first chunk
		_, _ = io.WriteString(w, "second\n")
	})))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 6)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || string(buf) != "first\n" {
		t.Fatalf("first chunk not flushed: %q %v", buf, err)
	}
	close(released)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "second\n" {
		t.Fatalf("rest: %q", rest)
	}
	if flushErr != nil || deadlineErr != nil {
		t.Fatalf("flush=%v deadline=%v", flushErr, deadlineErr)
	}
}
