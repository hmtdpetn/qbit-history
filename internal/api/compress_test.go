package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/klauspost/compress/gzip"
)

// serve runs one request through the compression middleware and returns the
// response together with the body exactly as it went over the wire.
func serve(t *testing.T, accept, contentType string, status, size int) (*http.Response, []byte) {
	t.Helper()
	body := strings.Repeat(`{"at":1789113593516,"value":"1024","kind":"sample"},`, size)
	h := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		if status != 200 {
			w.WriteHeader(status)
		}
		io.WriteString(w, body)
	}))
	req := httptest.NewRequest("GET", "/api/v1/torrents/1/series", nil)
	if accept != "" {
		req.Header.Set("Accept-Encoding", accept)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	raw, _ := io.ReadAll(res.Body)
	return res, raw
}

func TestLargeJSONIsCompressed(t *testing.T) {
	res, raw := serve(t, "gzip, deflate, br", "application/json", 200, 200)
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("a large JSON response was not compressed: %v", res.Header)
	}
	if !strings.Contains(res.Header.Get("Vary"), "Accept-Encoding") {
		t.Error("a compressed response must vary on Accept-Encoding or a cache can serve it to a client that cannot decode it")
	}
	if res.Header.Get("Content-Length") != "" {
		t.Error("Content-Length must not survive compression")
	}
	zr, e := gzip.NewReader(strings.NewReader(string(raw)))
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(zr)
	if e != nil {
		t.Fatal(e)
	}
	// The payload must survive intact, and the whole point is that it shrinks.
	if len(got) != 200*len(`{"at":1789113593516,"value":"1024","kind":"sample"},`) {
		t.Fatalf("decompressed length %d is wrong", len(got))
	}
	if len(raw)*4 > len(got) {
		t.Errorf("compression saved almost nothing: %d -> %d", len(got), len(raw))
	}
}

func TestResponsesThatMustNotBeCompressed(t *testing.T) {
	for _, tc := range []struct {
		name, accept, contentType string
		status, size              int
	}{
		// Small replies gain nothing, and this keeps the CSRF token and session
		// responses out of the compressed path entirely.
		{"small body", "gzip", "application/json", 200, 5},
		// A client that did not ask for gzip must never receive it.
		{"no gzip offered", "", "application/json", 200, 200},
		{"identity only", "identity", "application/json", 200, 200},
		// Already-compressed payloads only get bigger.
		{"png", "gzip", "image/png", 200, 200},
		// Non-200 bodies are passed through untouched.
		{"error", "gzip", "application/json", 500, 200},
	} {
		res, raw := serve(t, tc.accept, tc.contentType, tc.status, tc.size)
		if res.Header.Get("Content-Encoding") != "" {
			t.Errorf("%s: unexpectedly compressed", tc.name)
		}
		if res.StatusCode != tc.status {
			t.Errorf("%s: status %d, want %d", tc.name, res.StatusCode, tc.status)
		}
		if want := tc.size * len(`{"at":1789113593516,"value":"1024","kind":"sample"},`); len(raw) != want {
			t.Errorf("%s: body is %d bytes, want %d", tc.name, len(raw), want)
		}
	}
}
