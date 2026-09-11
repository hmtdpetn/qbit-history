package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"qbit-history/internal/collector"
	"qbit-history/internal/mockqb"
	"qbit-history/internal/store"
)

type env struct {
	t      *testing.T
	srv    *httptest.Server
	mock   *mockqb.Server
	mockUL string
	client *http.Client
	csrf   string
	mgr    *collector.Manager
	db     *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, e := store.Open(filepath.Join(dir, "api.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	if e := db.EnsureAdmin("admin", "correct-horse-battery"); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>ui</html>"), 0600)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	key := []byte("0123456789abcdef0123456789abcdef")
	mgr := collector.NewManager(ctx, db, key)
	t.Cleanup(mgr.StopCollectors)
	app := New(db, mgr, key, dir, log.New(io.Discard, "", 0))
	srv := httptest.NewServer(app.Handler())
	t.Cleanup(srv.Close)
	m := mockqb.New()
	m.Populate(6)
	ms := httptest.NewServer(m.Handler())
	t.Cleanup(ms.Close)
	jar, _ := cookiejar.New(nil)
	return &env{t: t, srv: srv, mock: m, mockUL: ms.URL, client: &http.Client{Jar: jar}, mgr: mgr, db: db}
}

func (e *env) do(method, path string, body any, headers map[string]string) (int, map[string]any, string) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var out map[string]any
	json.Unmarshal(raw, &out)
	return res.StatusCode, out, string(raw)
}
func (e *env) login() {
	code, body, raw := e.do("POST", "/api/v1/auth/login", map[string]string{"name": "admin", "password": "correct-horse-battery"}, nil)
	if code != 200 {
		e.t.Fatalf("login %d %s", code, raw)
	}
	e.csrf = body["csrf"].(string)
}
func (e *env) mutate(method, path string, body any) (int, map[string]any, string) {
	return e.do(method, path, body, map[string]string{"X-CSRF-Token": e.csrf, "Origin": e.srv.URL})
}

func TestAuthRequiredEverywhere(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/api/v1/torrents", "/api/v1/instances", "/api/v1/settings", "/api/v1/status", "/api/v1/storage/usage", "/api/v1/overview/series?start=1&end=2"} {
		if code, _, _ := e.do("GET", p, nil, nil); code != 401 {
			t.Fatalf("%s returned %d without auth", p, code)
		}
	}
	if code, _, _ := e.do("POST", "/api/v1/instances/test", map[string]string{"base_url": e.mockUL, "username": "a", "password": "b"}, nil); code != 401 {
		t.Fatalf("connection test allowed without auth: %d", code)
	}
	if code, _, _ := e.do("GET", "/healthz", nil, nil); code != 200 {
		t.Fatal("healthz should be public")
	}
	if code, _, raw := e.do("GET", "/some/client/route", nil, nil); code != 200 || !strings.Contains(raw, "ui") {
		t.Fatalf("SPA fallback: %d %s", code, raw)
	}
}

func TestLoginRateLimitAndCSRF(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 10; i++ {
		if code, _, _ := e.do("POST", "/api/v1/auth/login", map[string]string{"name": "admin", "password": "wrong"}, nil); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code, _, _ := e.do("POST", "/api/v1/auth/login", map[string]string{"name": "admin", "password": "correct-horse-battery"}, nil); code != 429 {
		t.Fatalf("expected 429 after 10 failures, got %d", code)
	}
	// Successful logins must not consume the budget, or a user with several tabs locks themselves out.
	e3 := newEnv(t)
	for i := 0; i < 25; i++ {
		if code, _, _ := e3.do("POST", "/api/v1/auth/login", map[string]string{"name": "admin", "password": "correct-horse-battery"}, nil); code != 200 {
			t.Fatalf("successful login %d was rate limited (%d)", i, code)
		}
	}
	// A success also clears earlier failures.
	e4 := newEnv(t)
	for i := 0; i < 9; i++ {
		e4.do("POST", "/api/v1/auth/login", map[string]string{"name": "admin", "password": "wrong"}, nil)
	}
	if code, _, _ := e4.do("POST", "/api/v1/auth/login", map[string]string{"name": "admin", "password": "correct-horse-battery"}, nil); code != 200 {
		t.Fatalf("login after 9 failures should still work, got %d", code)
	}
	for i := 0; i < 9; i++ {
		if code, _, _ := e4.do("POST", "/api/v1/auth/login", map[string]string{"name": "admin", "password": "wrong"}, nil); code != 401 {
			t.Fatalf("failure budget was not reset by the success (attempt %d got %d)", i, code)
		}
	}
	e2 := newEnv(t)
	e2.login()
	if code, _, _ := e2.do("GET", "/api/v1/settings", nil, nil); code != 200 {
		t.Fatal("authenticated GET failed")
	}
	if code, _, _ := e2.do("PATCH", "/api/v1/settings", map[string]any{"retention_days": 14}, map[string]string{"Origin": e2.srv.URL}); code != 403 {
		t.Fatalf("PATCH without CSRF token should be 403, got %d", code)
	}
	if code, _, _ := e2.do("PATCH", "/api/v1/settings", map[string]any{"retention_days": 14}, map[string]string{"X-CSRF-Token": e2.csrf, "Origin": "http://evil.example"}); code != 403 {
		t.Fatalf("cross-origin PATCH should be 403, got %d", code)
	}
	if code, _, raw := e2.mutate("PATCH", "/api/v1/settings", map[string]any{"retention_days": 14}); code != 200 {
		t.Fatalf("valid PATCH failed: %d %s", code, raw)
	}
	if code, _, _ := e2.mutate("PATCH", "/api/v1/settings", map[string]any{"retention_days": 7}); code != 409 {
		t.Fatal("shortening retention must require confirmation")
	}
	if code, _, _ := e2.do("PATCH", "/api/v1/settings", map[string]any{"retention_days": 7}, map[string]string{"X-CSRF-Token": e2.csrf, "Origin": e2.srv.URL, "X-Confirm-Retention": "yes"}); code != 200 {
		t.Fatal("confirmed shortening failed")
	}
	if code, _, _ := e2.mutate("PATCH", "/api/v1/settings", map[string]any{"retention_days": 9}); code != 400 {
		t.Fatal("invalid retention accepted")
	}
	if code, _, _ := e2.mutate("POST", "/api/v1/auth/logout", nil); code != 200 {
		t.Fatal("logout")
	}
	if code, _, _ := e2.do("GET", "/api/v1/settings", nil, nil); code != 401 {
		t.Fatal("session survived logout")
	}
}

func TestInstanceLifecycleWithMock(t *testing.T) {
	e := newEnv(t)
	e.login()
	code, body, raw := e.mutate("POST", "/api/v1/instances/test", map[string]string{"base_url": e.mockUL, "username": "admin", "password": "adminadmin"})
	if code != 200 {
		t.Fatalf("test: %d %s", code, raw)
	}
	if code, _, raw = e.mutate("POST", "/api/v1/instances", map[string]string{"name": "qB-Mock", "base_url": e.mockUL, "username": "admin", "password": "wrongpass"}); code != 400 {
		t.Fatalf("wrong password accepted: %d %s", code, raw)
	}
	if code, _, _ = e.mutate("POST", "/api/v1/instances", map[string]string{"name": "x", "base_url": "http://169.254.169.254/latest", "username": "a", "password": "b"}); code != 400 {
		t.Fatal("link-local target accepted")
	}
	if code, _, _ = e.mutate("POST", "/api/v1/instances", map[string]string{"name": "x", "base_url": "file:///etc/passwd", "username": "a", "password": "b"}); code != 400 {
		t.Fatal("file scheme accepted")
	}
	code, body, raw = e.mutate("POST", "/api/v1/instances", map[string]string{"name": "qB-Mock", "base_url": e.mockUL, "username": "admin", "password": "adminadmin"})
	if code != 201 {
		t.Fatalf("create: %d %s", code, raw)
	}
	id := body["instance_id"].(string)
	if code, _, _ = e.mutate("POST", "/api/v1/instances", map[string]string{"name": "dup", "base_url": e.mockUL + "/", "username": "admin", "password": "adminadmin"}); code != 409 {
		t.Fatal("duplicate normalized address accepted")
	}
	deadline := time.Now().Add(6 * time.Second)
	var items []any
	for time.Now().Before(deadline) {
		e.mock.Step(1)
		_, body, _ = e.do("GET", "/api/v1/torrents?limit=500&sort=up&order=desc", nil, nil)
		items, _ = body["items"].([]any)
		if len(items) == 6 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if len(items) != 6 {
		t.Fatalf("torrents after collection: %d", len(items))
	}
	if v := e.mock.Violations(); len(v) != 0 {
		t.Fatalf("upstream violations %v", v)
	}
	_, _, raw = e.do("GET", "/api/v1/status", nil, nil)
	for _, secret := range []string{"adminadmin", "mock-sid", "SID=", "wrongpass"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("status leaks %q", secret)
		}
	}
	_, _, raw = e.do("GET", "/api/v1/instances", nil, nil)
	if strings.Contains(raw, "adminadmin") || !strings.Contains(raw, "credentials_saved") {
		t.Fatal("instances response must not contain the password")
	}
	first := items[0].(map[string]any)
	tid := int64(first["torrent_id"].(float64))
	code, body, raw = e.do("GET", "/api/v1/torrents/"+itoa(tid)+"/series?start="+itoa(time.Now().UnixMilli()-60000)+"&end="+itoa(time.Now().UnixMilli()), nil, nil)
	if code != 200 || body["up"] == nil {
		t.Fatalf("series: %d %s", code, raw)
	}
	code, _, _ = e.do("GET", "/api/v1/instances/"+id+"/torrent-by-key/"+first["qb_key"].(string), nil, nil)
	if code != 200 {
		t.Fatal("deep link lookup failed")
	}
	// Editing with an empty password keeps the stored credential.
	if code, _, raw = e.mutate("PATCH", "/api/v1/instances/"+id, map[string]any{"name": "Renamed", "password": ""}); code != 200 {
		t.Fatalf("patch: %d %s", code, raw)
	}
	if code, _, _ = e.mutate("PATCH", "/api/v1/instances/"+id, map[string]any{"poll_enabled": false}); code != 200 {
		t.Fatal("disable")
	}
	_, body, _ = e.do("GET", "/api/v1/instances", nil, nil)
	inst := body["instances"].([]any)[0].(map[string]any)
	if inst["connection_status"] != "monitoring_stopped" || inst["instance"].(map[string]any)["name"] != "Renamed" || inst["instance"].(map[string]any)["credentials_saved"] != true {
		t.Fatalf("after patch: %v", inst)
	}
	if code, _, _ = e.mutate("DELETE", "/api/v1/instances/"+id, nil); code != 409 {
		t.Fatal("removal without confirmation")
	}
	if code, _, _ = e.mutate("POST", "/api/v1/instances/"+id+"/confirm-history-purge", nil); code != 200 {
		t.Fatal("confirm")
	}
	if code, _, raw = e.mutate("DELETE", "/api/v1/instances/"+id, nil); code != 200 {
		t.Fatalf("delete: %d %s", code, raw)
	}
	_, body, _ = e.do("GET", "/api/v1/instances", nil, nil)
	if len(body["instances"].([]any)) != 0 {
		t.Fatal("instance still listed")
	}
	if v := e.mock.Violations(); len(v) != 0 {
		t.Fatalf("upstream violations %v", v)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
