package upstream

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNormalizeAccepts(t *testing.T) {
	// Private and Docker-internal targets are the normal case and must stay allowed.
	cases := map[string]string{
		"http://qbittorrent:8080":            "http://qbittorrent:8080",
		"http://qbittorrent-seedbox-1:8080/": "http://qbittorrent-seedbox-1:8080",
		"HTTP://QB.local:8080":               "http://qb.local:8080",
		"http://10.0.0.5:18081":              "http://10.0.0.5:18081",
		"http://192.168.1.10":                "http://192.168.1.10",
		"http://172.17.0.2:8080":             "http://172.17.0.2:8080",
		"http://127.0.0.1:28637":             "http://127.0.0.1:28637",
		"https://qb.example.com":             "https://qb.example.com",
		"http://qb.example.com:80":           "http://qb.example.com",
		"https://qb.example.com:443":         "https://qb.example.com",
		"http://qb:8080/qbt":                 "http://qb:8080/qbt",
		"http://qb:8080/reverse/proxy/path/": "http://qb:8080/reverse/proxy/path",
		"  http://qb:8080  ":                 "http://qb:8080",
	}
	for in, want := range cases {
		got, e := Normalize(in)
		if e != nil {
			t.Errorf("Normalize(%q) unexpected error %v", in, e)
			continue
		}
		if got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	bad := []string{
		"", "   ", "qb:8080", "//qb:8080",
		"file:///etc/passwd", "gopher://qb:70", "ftp://qb", "unix:///var/run/docker.sock",
		"http://user:pass@qb:8080",         // credentials in the URL
		"http://qb:8080?x=1",               // query
		"http://qb:8080#frag",              // fragment
		"http://qb:8080/a/../../etc",       // traversal
		"http://qb:8080/./x",               // dot segment
		"http://qb:8080//double",           // empty segment
		"http://qb:8080/a%2f..%2fb",        // encoded traversal
		"http://qb:8080/back\\slash",       // backslash
		"http://169.254.169.254/latest",    // AWS/GCP link-local metadata
		"http://[fe80::1]:8080",            // IPv6 link-local
		"http://224.0.0.1:8080",            // multicast
		"http://0.0.0.0:8080",              // unspecified
		"http://100.100.100.200/",          // Alibaba metadata
		"http://168.63.129.16/",            // Azure wireserver
		"http://metadata.google.internal/", // metadata by name
		"http://metadata:8080/",            // metadata by name
	}
	for _, in := range bad {
		if got, e := Normalize(in); e == nil {
			t.Errorf("Normalize(%q) should have failed, got %q", in, got)
		}
	}
}

func TestOnlyWhitelistedEndpoints(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/api/v2/auth/login":
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s", Path: "/"})
			w.Write([]byte("Ok."))
		case "/api/v2/app/version":
			w.Write([]byte("v5.0.4"))
		case "/api/v2/app/webapiVersion":
			w.Write([]byte("2.11.2"))
		case "/api/v2/sync/maindata":
			w.Write([]byte(`{"rid":1,"full_update":true,"torrents":{},"server_state":{}}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, e := New(srv.URL, "admin", "adminadmin", 2*time.Second, 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	if e = c.Login(ctx); e != nil {
		t.Fatal(e)
	}
	if e = c.Versions(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Sync(ctx, 0); e != nil {
		t.Fatal(e)
	}
	// Anything outside the whitelist is refused before a request is made.
	for _, ep := range []string{"/api/v2/torrents/pause", "/api/v2/torrents/delete", "/api/v2/app/setPreferences", "/api/v2/torrents/addTrackers", "/api/v2/auth/logout", "/"} {
		if _, e := c.read(ctx, ep, nil, nil); e == nil {
			t.Fatalf("endpoint %s was allowed", ep)
		}
	}
	// Only GET is possible for read endpoints; login is the sole POST.
	if Allowed["/api/v2/sync/maindata"] != "GET" || Allowed["/api/v2/auth/login"] != "POST" || len(Allowed) != 5 {
		t.Fatalf("unexpected whitelist: %v", Allowed)
	}
	for _, r := range seen {
		if Allowed[strings.TrimPrefix(r, strings.Split(r, " ")[0]+" ")] == "" {
			t.Fatalf("non-whitelisted request reached the server: %s", r)
		}
	}
}

func TestLoginRequiresOkAndSID(t *testing.T) {
	for _, tc := range []struct {
		name, body, cookie string
		status             int
		wantErr            bool
	}{
		// qBittorrent <5.2 signals success with 200 "Ok.".
		{"ok with SID", "Ok.", "SID", 200, false},
		{"ok without SID", "Ok.", "", 200, true},
		{"fails", "Fails.", "SID", 200, true},
		{"login page", "<html>login</html>", "SID", 200, true},
		// qBittorrent 5.2+ signals success with 204 and an empty body.
		{"204 with SID", "", "SID", 204, false},
		{"204 without SID", "", "", 204, true},
		// qBittorrent 5.1+ renamed the cookie to QBT_SID_<port>.
		{"204 with QBT_SID", "", "QBT_SID_18081", 204, false},
		{"200 with QBT_SID", "Ok.", "QBT_SID_8080", 200, false},
		// A cookie that is not a session cookie must not be mistaken for one.
		{"unrelated cookie only", "", "cf_clearance", 204, true},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if tc.cookie != "" {
				http.SetCookie(w, &http.Cookie{Name: tc.cookie, Value: "s", Path: "/"})
			}
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		c, _ := New(srv.URL, "u", "p", time.Second, 1<<20)
		e := c.Login(context.Background())
		if tc.wantErr != errors.Is(e, ErrAuth) {
			t.Errorf("%s: got %v, wantErr=%v", tc.name, e, tc.wantErr)
		}
		srv.Close()
	}
}

// Every login failure is an ErrAuth, but "the upstream refused us", "the
// credentials are wrong" and "we were let in without a usable session" need
// different fixes, so the error must say which one happened — without ever
// echoing the cookie value, which is a session secret.
func TestAuthFailuresAreDistinguishable(t *testing.T) {
	const secret = "supersecretsessionvalue"
	for _, tc := range []struct {
		name, want string
		h          http.HandlerFunc
	}{
		{"upstream refuses", "upstream_http_403", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			w.Write([]byte("Forbidden"))
		}},
		{"credentials rejected", "login_rejected", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Fails."))
		}},
		{"no cookie at all", "no_set_cookie", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(204)
		}},
		{"cookie the jar will not replay", "sid_not_usable", func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: secret, Path: "/elsewhere"})
			w.WriteHeader(204)
		}},
	} {
		srv := httptest.NewServer(tc.h)
		c, _ := New(srv.URL, "u", "p", 2*time.Second, 1<<20)
		e := c.Login(context.Background())
		switch {
		case !errors.Is(e, ErrAuth):
			t.Errorf("%s: %v is not an ErrAuth", tc.name, e)
		case !strings.Contains(e.Error(), tc.want):
			t.Errorf("%s: %v does not mention %q", tc.name, e, tc.want)
		case strings.Contains(e.Error(), secret):
			t.Errorf("%s: the cookie value leaked into %v", tc.name, e)
		}
		srv.Close()
	}
}

// A 204 is only meaningful for login; an empty version string must not be
// accepted as a version, and an empty sync body must not become a baseline.
func TestEmptyBodyIsNotAValidReadResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s", Path: "/"})
			w.WriteHeader(204)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "u", "p", 2*time.Second, 1<<20)
	ctx := context.Background()
	if e := c.Login(ctx); e != nil {
		t.Fatalf("a 5.2-style 204 login must succeed: %v", e)
	}
	if e := c.Versions(ctx); e == nil || c.Version != "" {
		t.Fatalf("an empty version must be rejected, got %q err=%v", c.Version, e)
	}
	if _, e := c.Sync(ctx, 0); e == nil {
		t.Fatal("an empty sync body must be rejected")
	}
	if _, e := c.Info(ctx); e == nil {
		t.Fatal("an empty torrent list must be rejected")
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: "stolen", Path: "/"})
		w.Write([]byte("Ok."))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/api/v2/auth/login", 302)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "u", "p", 2*time.Second, 1<<20)
	e := c.Login(context.Background())
	if e == nil {
		t.Fatal("a redirected login must not succeed")
	}
	// The error must name the status and target so the UI can tell the user what to change.
	if !strings.Contains(e.Error(), "upstream_redirect_302") || !strings.Contains(e.Error(), target.URL) {
		t.Fatalf("redirect error is not diagnosable: %v", e)
	}
	if reached {
		t.Fatal("credentials were forwarded to the redirect target")
	}
}

// A non-200/401/403 answer must carry the status code and a short body snippet,
// otherwise a misconfigured reverse proxy or alternative WebUI is undiagnosable.
func TestNonOKStatusIsDiagnosable(t *testing.T) {
	for _, code := range []int{400, 404, 405, 500, 502} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte("upstream said no\n"))
		}))
		c, _ := New(srv.URL, "u", "p", 2*time.Second, 1<<20)
		e := c.Login(context.Background())
		if e == nil {
			t.Fatalf("status %d was accepted", code)
		}
		if !strings.Contains(e.Error(), fmt.Sprintf("upstream_http_%d", code)) || !strings.Contains(e.Error(), "upstream said no") {
			t.Fatalf("status %d error lacks code or body: %v", code, e)
		}
		srv.Close()
	}
}

func TestOversizeAndInvalidResponses(t *testing.T) {
	big := strings.Repeat("x", 4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s", Path: "/"})
			w.Write([]byte("Ok."))
			return
		}
		w.Write([]byte(big))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "u", "p", 2*time.Second, 1024) // 1 KiB limit
	ctx := context.Background()
	if e := c.Login(ctx); e != nil {
		t.Fatal(e)
	}
	if e := c.Versions(ctx); e == nil {
		t.Fatal("an oversize response must fail, not be truncated")
	}
	if _, e := c.Sync(ctx, 0); e == nil {
		t.Fatal("an oversize sync must fail")
	}
}

func TestSyncValidation(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s", Path: "/"})
			w.Write([]byte("Ok."))
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "u", "p", 2*time.Second, 1<<20)
	ctx := context.Background()
	c.Login(ctx)
	for _, tc := range []struct {
		name, json string
		rid        int64
		ok         bool
	}{
		{"baseline must be full", `{"rid":1,"full_update":false,"torrents":{}}`, 0, false},
		{"missing rid", `{"full_update":true,"torrents":{}}`, 0, false},
		{"negative rid", `{"rid":-1,"full_update":true}`, 0, false},
		{"negative speed", `{"rid":1,"full_update":true,"server_state":{"up_info_speed":-5}}`, 0, false},
		{"negative counter", `{"rid":1,"full_update":true,"torrents":{"a":{"uploaded":-1}}}`, 0, false},
		{"html", `<html>login</html>`, 0, false},
		{"truncated", `{"rid":1,"full_`, 0, false},
		{"valid full", `{"rid":7,"full_update":true,"torrents":{},"server_state":{"alltime_ul":10}}`, 0, true},
		{"valid incremental", `{"rid":8,"full_update":false,"torrents":{}}`, 7, true},
		// qBittorrent omits full_update entirely on incremental updates; an
		// absent field means false and must not be treated as a broken response.
		{"incremental without the field", `{"rid":9,"torrents":{}}`, 7, true},
		{"baseline without the field", `{"rid":9,"torrents":{}}`, 0, false},
	} {
		body = tc.json
		s, e := c.Sync(ctx, tc.rid)
		if tc.ok != (e == nil) {
			t.Errorf("%s: err=%v want ok=%v", tc.name, e, tc.ok)
		}
		if tc.ok && tc.name == "valid full" && (*s.RID != 7 || s.Server.Uploaded == nil || *s.Server.Uploaded != 10) {
			t.Errorf("valid full parsed wrong: %+v", s)
		}
		// Callers dereference Full, so a successful Sync must never leave it nil.
		if tc.ok && (s.Full == nil || *s.Full != strings.Contains(tc.json, `"full_update":true`)) {
			t.Errorf("%s: Full=%v is wrong or nil", tc.name, s.Full)
		}
	}
}

func TestMissingFieldIsNotZero(t *testing.T) {
	var p Patch
	if e := p.UnmarshalJSON([]byte(`{"upspeed":0}`)); e != nil {
		t.Fatal(e)
	}
	if p.Up == nil || *p.Up != 0 {
		t.Fatal("an explicit 0 must be recorded as 0")
	}
	if p.Down != nil || p.Uploaded != nil || p.Name != nil {
		t.Fatal("absent fields must stay nil, never default to 0")
	}
}
