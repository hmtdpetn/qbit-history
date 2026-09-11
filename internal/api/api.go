// Package api serves the same-origin HTTP API and static UI. It authenticates
// with its own single administrator account and never proxies arbitrary
// requests to qBittorrent.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/klauspost/compress/gzip"
	"qbit-history/internal/collector"
	"qbit-history/internal/model"
	"qbit-history/internal/query"
	"qbit-history/internal/security"
	"qbit-history/internal/store"
	"qbit-history/internal/upstream"
)

const Version = "2.0.0"

type App struct {
	DB           *store.Store
	Manager      *collector.Manager
	Key          []byte
	WebDir       string
	Logger       *log.Logger
	SecureCookie bool
	Started      time.Time
	mu           sync.Mutex
	attempts     map[string][]time.Time
	confirmed    map[string]map[string]time.Time
}

func New(db *store.Store, mgr *collector.Manager, key []byte, webDir string, l *log.Logger) *App {
	return &App{DB: db, Manager: mgr, Key: key, WebDir: webDir, Logger: l, Started: time.Now(), attempts: map[string][]time.Time{}, confirmed: map[string]map[string]time.Time{}, SecureCookie: os.Getenv("HISTORY_SECURE_COOKIE") == "1"}
}
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", a.health)
	mux.HandleFunc("/api/v1/auth/login", a.login)
	mux.HandleFunc("/api/v1/auth/logout", a.logout)
	mux.HandleFunc("/api/v1/auth/session", a.session)
	mux.Handle("/api/v1/", a.require(http.HandlerFunc(a.api)))
	mux.Handle("/", http.HandlerFunc(a.static))
	return a.headers(compress(mux))
}

// compressThreshold keeps small responses uncompressed. It saves nothing on a
// few hundred bytes, and it keeps the CSRF token and session replies — the only
// responses carrying a secret — out of the compressed path entirely, so their
// size can never leak anything about their content.
const compressThreshold = 1400

// compress gzips large responses. Chart payloads are long runs of repeated JSON
// keys and are typically reduced by more than 90 %, which matters because the
// only way in is a high-latency SSH tunnel where bytes, not CPU, are the limit.
func compress(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Accept-Encoding")
		cw := &gzipWriter{ResponseWriter: w}
		defer cw.Close()
		next.ServeHTTP(cw, r)
	})
}

// gzipWriter buffers the first bytes of a response so the decision to compress
// can be made on the actual size rather than on a Content-Length that handlers
// do not set.
type gzipWriter struct {
	http.ResponseWriter
	buf    []byte
	gz     *gzip.Writer
	status int
	done   bool
}

func (g *gzipWriter) WriteHeader(status int) {
	if g.status == 0 {
		g.status = status
	}
}
func (g *gzipWriter) Write(b []byte) (int, error) {
	if g.gz != nil {
		return g.gz.Write(b)
	}
	if g.done {
		return g.ResponseWriter.Write(b)
	}
	g.buf = append(g.buf, b...)
	if len(g.buf) < compressThreshold {
		return len(b), nil
	}
	if g.compressible() {
		g.start()
	} else {
		g.Close()
	}
	return len(b), nil
}

// compressible excludes partial and not-modified responses, whose bodies must
// reach the client byte for byte, and payloads that are already compressed.
func (g *gzipWriter) compressible() bool {
	if g.statusOrOK() != 200 {
		return false
	}
	switch t, _, _ := strings.Cut(g.Header().Get("Content-Type"), ";"); strings.TrimSpace(t) {
	case "application/json", "text/html", "text/css", "text/javascript", "application/javascript", "image/svg+xml", "text/plain":
		return true
	}
	return false
}

// start commits to compression and replays the buffered prefix.
func (g *gzipWriter) start() {
	g.Header().Del("Content-Length")
	g.Header().Set("Content-Encoding", "gzip")
	g.ResponseWriter.WriteHeader(g.statusOrOK())
	g.gz = gzip.NewWriter(g.ResponseWriter)
	g.gz.Write(g.buf)
	g.buf = nil
	g.done = true
}
func (g *gzipWriter) statusOrOK() int {
	if g.status == 0 {
		return 200
	}
	return g.status
}
func (g *gzipWriter) Close() {
	switch {
	case g.gz != nil:
		g.gz.Close()
	case !g.done:
		g.ResponseWriter.WriteHeader(g.statusOrOK())
		g.ResponseWriter.Write(g.buf)
		g.done = true
	}
}
func (a *App) headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// static serves the built UI; unknown non-file paths fall back to index.html for client routing.
func (a *App) static(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	clean := filepath.Clean("/" + r.URL.Path)
	full := filepath.Join(a.WebDir, filepath.FromSlash(clean))
	if st, e := os.Stat(full); e == nil && !st.IsDir() {
		if strings.HasPrefix(clean, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		http.ServeFile(w, r, full)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, filepath.Join(a.WebDir, "index.html"))
}
func (a *App) health(w http.ResponseWriter, r *http.Request) {
	var one int
	if e := a.DB.Read.QueryRow("SELECT 1").Scan(&one); e != nil {
		writeJSON(w, 503, map[string]any{"ok": false, "reason": "database unavailable"})
		return
	}
	u := a.DB.Usage()
	// Storage pauses and offline qB instances are reported, not treated as container failure.
	writeJSON(w, 200, map[string]any{"ok": true, "version": Version, "sqlite": u.SQLite, "storage_paused": u.Paused, "reason": u.Reason, "uptime_s": int64(time.Since(a.Started).Seconds())})
}
func (a *App) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		csrf, ok := a.auth(r)
		if !ok {
			jsonError(w, 401, "未认证")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if !a.sameOrigin(r) {
				jsonError(w, 403, "来源校验失败")
				return
			}
			if r.Header.Get("X-CSRF-Token") != csrf {
				jsonError(w, 403, "CSRF 令牌无效")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
func (a *App) auth(r *http.Request) (string, bool) {
	c, e := r.Cookie("qh_session")
	if e != nil || c.Value == "" {
		return "", false
	}
	return a.DB.Session(security.Digest(c.Value))
}
func (a *App) cookie(value string, maxAge int) *http.Cookie {
	// Lax (not Strict) so a top-level deep link from another local origin (e.g. VueTorrent) still carries the session; writes are protected by Origin + CSRF token.
	return &http.Cookie{Name: "qh_session", Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge, Secure: a.SecureCookie}
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	ip := clientIP(r)
	if !a.allow(ip) {
		jsonError(w, 429, "登录尝试过于频繁，请 15 分钟后再试")
		return
	}
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if e := decode(r, &body); e != nil || body.Name == "" || body.Password == "" {
		jsonError(w, 400, "用户名或密码格式错误")
		return
	}
	if !a.DB.Authenticate(body.Name, body.Password) {
		a.recordFailure(ip)
		jsonError(w, 401, "用户名或密码错误")
		return
	}
	a.clearFailures(ip)
	token := security.Random(32)
	csrf := security.Random(24)
	if e := a.DB.CreateSession(security.Digest(token), csrf, time.Now().Add(12*time.Hour)); e != nil {
		jsonError(w, 500, "无法建立会话")
		return
	}
	http.SetCookie(w, a.cookie(token, 43200))
	writeJSON(w, 200, map[string]any{"ok": true, "user": body.Name, "csrf": csrf})
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(405)
		return
	}
	if c, e := r.Cookie("qh_session"); e == nil {
		a.DB.DeleteSession(security.Digest(c.Value))
	}
	http.SetCookie(w, a.cookie("", -1))
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (a *App) session(w http.ResponseWriter, r *http.Request) {
	csrf, ok := a.auth(r)
	if !ok {
		jsonError(w, 401, "未认证")
		return
	}
	writeJSON(w, 200, map[string]any{"authenticated": true, "user": a.DB.UserName(), "csrf": csrf, "version": Version})
}

// allow reports whether an IP may still attempt a login. Only *failed*
// attempts count, and a success clears them: otherwise a legitimate user
// opening several tabs would lock themselves out.
func (a *App) allow(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cut := time.Now().Add(-15 * time.Minute)
	xs := a.attempts[ip][:0]
	for _, t := range a.attempts[ip] {
		if t.After(cut) {
			xs = append(xs, t)
		}
	}
	if len(xs) == 0 {
		delete(a.attempts, ip)
	} else {
		a.attempts[ip] = xs
	}
	return len(xs) < 10
}
func (a *App) recordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attempts[ip] = append(a.attempts[ip], time.Now())
}
func (a *App) clearFailures(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.attempts, ip)
}
func (a *App) api(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	switch {
	case p == "auth/password" && r.Method == "POST":
		a.changePassword(w, r)
	case p == "instances" && r.Method == "GET":
		a.instances(w)
	case p == "instances" && r.Method == "POST":
		a.createInstance(w, r)
	case p == "instances/test" && r.Method == "POST":
		a.testInstance(w, r)
	case strings.HasPrefix(p, "instances/"):
		a.instance(w, r, p)
	case p == "torrents" && r.Method == "GET":
		a.torrents(w, r)
	case strings.HasPrefix(p, "torrents/"):
		a.torrent(w, r, p)
	case p == "overview/series" && r.Method == "GET":
		a.overviewSeries(w, r)
	case p == "settings" && r.Method == "GET":
		writeJSON(w, 200, a.DB.Settings())
	case p == "settings" && r.Method == "PATCH":
		a.patchSettings(w, r)
	case p == "storage/usage" && r.Method == "GET":
		writeJSON(w, 200, a.usage())
	case p == "storage/forecast" && r.Method == "GET":
		writeJSON(w, 200, map[string]any{"forecast": a.DB.Forecast(), "model": "raw 24h + 60s 72h + 300s D days, layered; not net-growth extrapolation"})
	case p == "status" && r.Method == "GET":
		a.status(w)
	case p == "events" && r.Method == "GET":
		a.events(w, r)
	default:
		jsonError(w, 404, "接口不存在")
	}
}
func (a *App) usage() map[string]any {
	u := a.DB.Usage()
	qb, oldest, dropped := a.Manager.Queue.Stats()
	return map[string]any{"main_bytes": u.Main, "wal_bytes": u.WAL, "shm_bytes": u.SHM, "directory_bytes": u.Directory, "used_pages": u.UsedPages, "free_pages": u.FreePages, "page_size": u.PageSize, "payload_raw_bytes": u.PayloadRaw, "raw_samples": u.RawSamples, "payload_60_bytes": u.Payload60, "buckets_60": u.Buckets60, "payload_300_bytes": u.Payload300, "buckets_300": u.Buckets300, "shared_bytes": u.Shared, "host_free_bytes": u.HostFree, "budget_bytes": u.Budget, "instances": u.Instances, "torrents": u.Torrents, "sqlite": u.SQLite, "paused": u.Paused, "reason": u.Reason, "last_commit_at": u.LastCommit, "last_tx_ms": u.LastTxMS, "clock_hold": u.ClockHold, "stats_at": u.StatsAt, "attributions": u.Attributions, "queue_bytes": qb, "queue_oldest_at": oldest, "queue_dropped": dropped}
}
func (a *App) instances(w http.ResponseWriter) {
	sts, _ := a.Manager.Snapshots()
	writeJSON(w, 200, map[string]any{"instances": sts})
}
func (a *App) status(w http.ResponseWriter) {
	sts, _ := a.Manager.Snapshots()
	writeJSON(w, 200, map[string]any{"version": Version, "instances": sts, "storage": a.usage(), "manager_error": a.Manager.Error(), "settings": a.DB.Settings(), "uptime_s": int64(time.Since(a.Started).Seconds()), "allowed_upstream": upstream.Allowed})
}
func (a *App) events(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UnixMilli()
	start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
	if end == 0 {
		end = now + 1
	}
	if start == 0 {
		start = now - 86400000
	}
	writeJSON(w, 200, map[string]any{"events": a.DB.Events(r.Context(), r.URL.Query().Get("instance"), 0, start, end)})
}
func (a *App) changePassword(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Current string `json:"current_password"`
		New     string `json:"new_password"`
	}
	if e := decode(r, &b); e != nil || !a.DB.Authenticate(a.DB.UserName(), b.Current) {
		jsonError(w, 400, "当前密码错误")
		return
	}
	if e := a.DB.ChangePassword(b.New); e != nil {
		jsonError(w, 400, "新密码需 12–72 个字符")
		return
	}
	http.SetCookie(w, a.cookie("", -1))
	writeJSON(w, 200, map[string]any{"ok": true, "message": "密码已更新，请重新登录"})
}

type instanceBody struct {
	Name        string `json:"name"`
	BaseURL     string `json:"base_url"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	PollEnabled *bool  `json:"poll_enabled"`
}

func (a *App) createInstance(w http.ResponseWriter, r *http.Request) {
	var b instanceBody
	if e := decode(r, &b); e != nil || strings.TrimSpace(b.Name) == "" || b.Username == "" || b.Password == "" {
		jsonError(w, 400, "连接名称、地址、用户名和密码均必填")
		return
	}
	base, e := upstream.Normalize(b.BaseURL)
	if e != nil {
		jsonError(w, 400, "地址不符合安全规则："+e.Error())
		return
	}
	if dup := a.duplicate(base, ""); dup != "" {
		jsonError(w, 409, "已有连接「"+dup+"」使用相同的标准化地址；如需监控另一台 qB 请使用不同地址")
		return
	}
	if e = a.testCredentials(r.Context(), base, b.Username, b.Password); e != nil {
		jsonError(w, 400, "连接测试失败："+diagnose(e))
		return
	}
	id := uuid.NewString()
	enc, e := security.Encrypt(a.Key, id, b.Password)
	if e != nil {
		jsonError(w, 500, "凭据加密失败")
		return
	}
	enabled := true
	if b.PollEnabled != nil {
		enabled = *b.PollEnabled
	}
	if e = a.DB.SaveInstance(model.Instance{ID: id, Name: limit(b.Name, 120), BaseURL: base, Username: limit(b.Username, 256), Secret: enc, PollEnabled: enabled}); e != nil {
		jsonError(w, 500, "保存连接失败")
		return
	}
	now := time.Now().UnixMilli()
	a.DB.AddEvent(model.Event{InstanceID: id, At: now, End: now, Kind: "composition_change", Detail: "新增监控连接；总览组成变化"})
	a.Manager.Reload()
	writeJSON(w, 201, map[string]any{"instance_id": id, "credentials_saved": true})
}
func (a *App) duplicate(base, except string) string {
	is, _ := a.DB.Instances()
	for _, i := range is {
		if i.BaseURL == base && i.ID != except {
			return i.Name
		}
	}
	return ""
}
func (a *App) testInstance(w http.ResponseWriter, r *http.Request) {
	var b instanceBody
	if e := decode(r, &b); e != nil {
		jsonError(w, 400, "请求格式错误")
		return
	}
	base, e := upstream.Normalize(b.BaseURL)
	if e != nil {
		jsonError(w, 400, "地址不符合安全规则："+e.Error())
		return
	}
	cfg := a.DB.Settings()
	c, e := upstream.New(base, b.Username, b.Password, time.Duration(cfg.TimeoutMS)*time.Millisecond, cfg.ResponseBytes)
	if e != nil {
		jsonError(w, 400, "地址不可用")
		return
	}
	if e = c.Login(r.Context()); e != nil {
		jsonError(w, 400, "连接测试失败："+diagnose(e))
		return
	}
	if e = c.Versions(r.Context()); e != nil {
		jsonError(w, 400, "版本探测失败："+diagnose(e))
		return
	}
	if _, e = c.Sync(r.Context(), 0); e != nil {
		jsonError(w, 400, "同步接口失败："+diagnose(e))
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "登录、版本与只读同步接口通过", "qb_version": c.Version, "webapi_version": c.APIVersion, "normalized_url": base})
}
func diagnose(e error) string {
	switch {
	case errors.Is(e, upstream.ErrAuth):
		return "认证失败（" + e.Error() + "）：用户名/密码错误、IP 被 qB 封禁，或 Host/CSRF 校验拒绝"
	case strings.Contains(e.Error(), "dns_failed"):
		return "DNS 解析失败：容器内无法解析该主机名，请检查是否加入了 qB 所在网络"
	case strings.Contains(e.Error(), "target_not_allowed"):
		return "目标地址属于禁止访问的范围（链路本地/多播/云元数据）"
	case strings.Contains(e.Error(), "network"):
		return "网络不可达或超时：请确认地址、端口和 Docker 网络（容器内 127.0.0.1 是 history 自己）"
	case strings.Contains(e.Error(), "upstream_redirect"):
		return "上游返回重定向（" + e.Error() + "）。本应用按设计不跟随重定向，以免把凭据发到别的主机。若 qB 启用了 HTTPS，请把地址改成 https://；若是反代造成的跳转，请填写跳转后的最终地址"
	case strings.Contains(e.Error(), "upstream_http_"):
		return "上游返回 " + strings.TrimPrefix(e.Error(), "upstream_") + "：可能是路径前缀不对、替代 WebUI 拦截了请求，或该地址不是 qB 的 WebUI"
	default:
		return e.Error()
	}
}
func (a *App) testCredentials(ctx context.Context, base, user, pw string) error {
	cfg := a.DB.Settings()
	c, e := upstream.New(base, user, pw, time.Duration(cfg.TimeoutMS)*time.Millisecond, cfg.ResponseBytes)
	if e != nil {
		return errors.New("地址不可用")
	}
	if e = c.Login(ctx); e != nil {
		return e
	}
	return c.Versions(ctx)
}
func (a *App) instance(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(strings.TrimPrefix(p, "instances/"), "/")
	id := parts[0]
	if id == "" {
		jsonError(w, 404, "实例不存在")
		return
	}
	switch {
	case len(parts) == 2 && parts[1] == "series" && r.Method == "GET":
		sid, e := a.DB.GlobalSeries(id)
		if e != nil {
			jsonError(w, 404, "实例不存在")
			return
		}
		a.series(w, r, sid, id)
	case len(parts) == 3 && parts[1] == "torrent-by-key" && r.Method == "GET":
		if t, ok := a.Manager.TorrentByKey(id, parts[2]); ok {
			writeJSON(w, 200, t)
			return
		}
		jsonError(w, 404, "任务不存在或已移除")
	case len(parts) == 2 && parts[1] == "reconnect" && r.Method == "POST":
		a.Manager.Reload()
		writeJSON(w, 200, map[string]any{"ok": true})
	case len(parts) == 2 && parts[1] == "confirm-bulk-removal" && r.Method == "POST":
		if !a.Manager.Confirm(id) {
			jsonError(w, 409, "该实例当前没有运行中的采集器")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "message": "已确认：只清除此实例已消失任务的历史"})
	case len(parts) == 2 && parts[1] == "confirm-history-purge" && r.Method == "POST":
		csrf, _ := a.auth(r)
		a.mu.Lock()
		if a.confirmed[csrf] == nil {
			a.confirmed[csrf] = map[string]time.Time{}
		}
		a.confirmed[csrf][id] = time.Now()
		a.mu.Unlock()
		writeJSON(w, 200, map[string]any{"ok": true, "message": "已确认仅清除本应用中的该实例历史；请在 5 分钟内执行移除"})
	case len(parts) == 1 && r.Method == "PATCH":
		a.patchInstance(w, r, id)
	case len(parts) == 1 && r.Method == "DELETE":
		csrf, _ := a.auth(r)
		a.mu.Lock()
		at, ok := a.confirmed[csrf][id]
		if ok {
			delete(a.confirmed[csrf], id)
		}
		a.mu.Unlock()
		if !ok || time.Since(at) > 5*time.Minute {
			jsonError(w, 409, "请先明确确认清除本应用中的实例历史")
			return
		}
		if _, e := a.DB.FindInstance(id); e != nil {
			jsonError(w, 404, "实例不存在")
			return
		}
		if e := a.Manager.Remove(id); e != nil {
			jsonError(w, 500, "移除失败")
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true})
	default:
		jsonError(w, 405, "不支持的操作")
	}
}
func (a *App) patchInstance(w http.ResponseWriter, r *http.Request, id string) {
	old, e := a.DB.FindInstance(id)
	if e != nil {
		jsonError(w, 404, "实例不存在")
		return
	}
	var b instanceBody
	if e = decode(r, &b); e != nil {
		jsonError(w, 400, "请求格式错误")
		return
	}
	if strings.TrimSpace(b.Name) != "" {
		old.Name = limit(b.Name, 120)
	}
	addressChanged := false
	if b.BaseURL != "" {
		base, e := upstream.Normalize(b.BaseURL)
		if e != nil {
			jsonError(w, 400, "地址不符合安全规则："+e.Error())
			return
		}
		if dup := a.duplicate(base, id); dup != "" {
			jsonError(w, 409, "已有连接「"+dup+"」使用相同的标准化地址")
			return
		}
		addressChanged = base != old.BaseURL
		old.BaseURL = base
	}
	if b.Username != "" {
		old.Username = limit(b.Username, 256)
	}
	if b.PollEnabled != nil {
		old.PollEnabled = *b.PollEnabled
	}
	keep := strings.TrimSpace(b.Password) == ""
	if !keep {
		if e = a.testCredentials(r.Context(), old.BaseURL, old.Username, b.Password); e != nil {
			jsonError(w, 400, "连接测试失败："+diagnose(e))
			return
		}
		old.Secret, e = security.Encrypt(a.Key, id, b.Password)
		if e != nil {
			jsonError(w, 500, "凭据加密失败")
			return
		}
	}
	if e = a.DB.SaveInstanceKeepingSecret(old, keep); e != nil {
		jsonError(w, 500, "保存连接失败")
		return
	}
	now := time.Now().UnixMilli()
	if addressChanged {
		a.DB.AddEvent(model.Event{InstanceID: id, At: now, End: now, Kind: "address_change", Detail: "连接地址已修改：应指向同一台 qB；若是另一台客户端请新建连接"})
	}
	a.Manager.Reload()
	writeJSON(w, 200, map[string]any{"ok": true, "credentials_saved": old.CredentialsSaved || !keep, "address_changed": addressChanged})
}

// torrents lists the current torrents with search, filters, sorting and
// offset-cursor pagination, served entirely from memory snapshots.
func (a *App) torrents(w http.ResponseWriter, r *http.Request) {
	_, ts := a.Manager.Snapshots()
	q := r.URL.Query()
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))
	iid, cat, tag, state := q.Get("instance"), q.Get("category"), q.Get("tag"), q.Get("state")
	cats, tags, states := map[string]int{}, map[string]int{}, map[string]int{}
	xs := ts[:0]
	for _, t := range ts {
		if iid != "" && t.InstanceID != iid {
			continue
		}
		cats[t.Category]++
		states[t.State]++
		for _, x := range strings.Split(t.Tags, ",") {
			if x = strings.TrimSpace(x); x != "" {
				tags[x]++
			}
		}
		if cat != "" && t.Category != cat || state != "" && t.State != state || tag != "" && !hasTag(t.Tags, tag) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(t.Name), search) && !strings.Contains(strings.ToLower(t.Key), search) {
			continue
		}
		xs = append(xs, t)
	}
	sortKey, desc := q.Get("sort"), q.Get("order") != "asc"
	if sortKey == "" {
		sortKey, desc = "name", false
	}
	less := func(x, y model.Torrent) bool {
		switch sortKey {
		case "up":
			return x.Sample.Up < y.Sample.Up
		case "down":
			return x.Sample.Down < y.Sample.Down
		case "uploaded":
			return x.Sample.Uploaded < y.Sample.Uploaded
		case "downloaded":
			return x.Sample.Downloaded < y.Sample.Downloaded
		case "ratio":
			return x.Ratio < y.Ratio
		case "size":
			return x.Size < y.Size
		case "upload_1h":
			return numStr(x.Upload1h) < numStr(y.Upload1h)
		case "upload_24h":
			return numStr(x.Upload24h) < numStr(y.Upload24h)
		case "last_upload":
			return x.LastUpload < y.LastUpload
		case "added":
			return x.Added < y.Added
		case "state":
			return x.State < y.State
		default:
			return strings.ToLower(x.Name) < strings.ToLower(y.Name)
		}
	}
	sort.SliceStable(xs, func(i, j int) bool {
		if desc {
			return less(xs[j], xs[i])
		}
		return less(xs[i], xs[j])
	})
	limit := 100
	if v, _ := strconv.Atoi(q.Get("limit")); v >= 1 && v <= 1000 {
		limit = v
	}
	offset, _ := strconv.Atoi(q.Get("cursor"))
	if offset < 0 || offset > len(xs) {
		offset = 0
	}
	page := xs[offset:min(offset+limit, len(xs))]
	next := ""
	if offset+limit < len(xs) {
		next = strconv.Itoa(offset + limit)
	}
	writeJSON(w, 200, map[string]any{"items": page, "next_cursor": next, "total": len(xs), "categories": cats, "tags": tags, "states": states})
}
func hasTag(tags, want string) bool {
	for _, x := range strings.Split(tags, ",") {
		if strings.TrimSpace(x) == want {
			return true
		}
	}
	return false
}
func numStr(s *string) int64 {
	if s == nil {
		return -1
	}
	v, _ := strconv.ParseInt(*s, 10, 64)
	return v
}
func (a *App) torrent(w http.ResponseWriter, r *http.Request, p string) {
	parts := strings.Split(strings.TrimPrefix(p, "torrents/"), "/")
	id, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil {
		jsonError(w, 400, "任务 ID 错误")
		return
	}
	t, ok := a.Manager.Torrent(id)
	if !ok {
		jsonError(w, 404, "任务不存在或已移除")
		return
	}
	switch {
	case len(parts) == 1 && r.Method == "GET":
		writeJSON(w, 200, t)
	case len(parts) == 2 && parts[1] == "series" && r.Method == "GET":
		a.series(w, r, t.SeriesID, t.InstanceID)
	default:
		jsonError(w, 404, "接口不存在")
	}
}
func (a *App) series(w http.ResponseWriter, r *http.Request, sid int64, instance string) {
	now := time.Now().UnixMilli()
	q, e := query.Parse(r.URL.Query().Get("start"), r.URL.Query().Get("end"), r.URL.Query().Get("metric"), r.URL.Query().Get("counter"), r.URL.Query().Get("max_points"), now)
	if e != nil {
		jsonError(w, 400, e.Error())
		return
	}
	out, e := query.Series(r.Context(), a.DB, a.Manager.Queue, sid, instance, q)
	if e != nil {
		jsonError(w, 409, "该时间序列暂不可用："+e.Error())
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) overviewSeries(w http.ResponseWriter, r *http.Request) {
	now := time.Now().UnixMilli()
	q, e := query.Parse(r.URL.Query().Get("start"), r.URL.Query().Get("end"), r.URL.Query().Get("metric"), r.URL.Query().Get("counter"), r.URL.Query().Get("max_points"), now)
	if e != nil {
		jsonError(w, 400, e.Error())
		return
	}
	sts, _ := a.Manager.Snapshots()
	want := map[string]bool{}
	for _, id := range strings.Split(r.URL.Query().Get("instances"), ",") {
		if id = strings.TrimSpace(id); id != "" {
			want[id] = true
		}
	}
	ids := map[string]int64{}
	for _, st := range sts {
		if (len(want) == 0 || want[st.Instance.ID]) && st.GlobalSeries > 0 {
			ids[st.Instance.ID] = st.GlobalSeries
		}
	}
	out, e := query.Overview(r.Context(), a.DB, a.Manager.Queue, ids, q)
	if e != nil {
		jsonError(w, 409, "总览序列暂不可用："+e.Error())
		return
	}
	writeJSON(w, 200, out)
}
func (a *App) patchSettings(w http.ResponseWriter, r *http.Request) {
	old := a.DB.Settings()
	v := old
	if e := decode(r, &v); e != nil {
		jsonError(w, 400, "设置格式错误")
		return
	}
	if e := store.ValidateSettings(v); e != nil {
		jsonError(w, 400, "设置值不在允许范围内")
		return
	}
	shorten := v.RetentionDays < old.RetentionDays
	if shorten && r.Header.Get("X-Confirm-Retention") != "yes" {
		jsonError(w, 409, "缩短保留期会不可逆地删除超出范围的历史，需要确认")
		return
	}
	if e := a.DB.SaveSettings(v, shorten); e != nil {
		jsonError(w, 400, e.Error())
		return
	}
	if v.Interval != old.Interval || v.TimeoutMS != old.TimeoutMS || v.ResponseBytes != old.ResponseBytes {
		a.Manager.Reload()
	}
	writeJSON(w, 200, v)
}
func (a *App) sameOrigin(r *http.Request) bool {
	source := r.Header.Get("Origin")
	if source == "" {
		source = r.Header.Get("Referer")
	}
	if source == "" {
		return false
	}
	u, e := url.Parse(source)
	if e != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host) && (u.Scheme == "http" || u.Scheme == "https")
}
func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func jsonError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": msg})
}
func clientIP(r *http.Request) string {
	h, _, e := net.SplitHostPort(r.RemoteAddr)
	if e == nil {
		return h
	}
	return r.RemoteAddr
}
func limit(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n])
	}
	return string(r)
}
