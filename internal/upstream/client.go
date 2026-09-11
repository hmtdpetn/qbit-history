package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
)

var ErrAuth = errors.New("authentication_failed")
var Allowed = map[string]string{"/api/v2/auth/login": "POST", "/api/v2/app/version": "GET", "/api/v2/app/webapiVersion": "GET", "/api/v2/sync/maindata": "GET", "/api/v2/torrents/info": "GET"}

func Normalize(raw string) (string, error) {
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(u.Path, "\\") {
		return "", errors.New("invalid_base_url")
	}
	if u.RawPath != "" || strings.Contains(u.Path, "//") {
		return "", errors.New("invalid_root_path")
	}
	for _, s := range strings.Split(u.Path, "/") {
		if s == ".." || s == "." {
			return "", errors.New("invalid_root_path")
		}
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	if strings.HasSuffix(u.Host, ":80") && u.Scheme == "http" {
		u.Host = strings.TrimSuffix(u.Host, ":80")
	}
	if strings.HasSuffix(u.Host, ":443") && u.Scheme == "https" {
		u.Host = strings.TrimSuffix(u.Host, ":443")
	}
	if strings.Contains(u.Hostname(), "metadata") || blockedHost(u.Hostname()) {
		return "", errors.New("target_not_allowed")
	}
	u.Path = strings.TrimSuffix(path.Clean("/"+u.Path), "/")
	if u.Path == "." {
		u.Path = ""
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}
func blockedHost(h string) bool { a, e := netip.ParseAddr(h); return e == nil && blocked(a) }
func blocked(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsUnspecified() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsMulticast() || a == netip.MustParseAddr("100.100.100.200") || a == netip.MustParseAddr("168.63.129.16")
}

type guard struct {
	base *url.URL
	next http.RoundTripper
}

func (g guard) RoundTrip(r *http.Request) (*http.Response, error) {
	ep := strings.TrimPrefix(r.URL.Path, g.base.Path)
	if r.URL.Scheme != g.base.Scheme || r.URL.Host != g.base.Host || r.URL.Path != g.base.Path+ep || Allowed[ep] != r.Method {
		return nil, errors.New("upstream_not_allowed")
	}
	return g.next.RoundTrip(r)
}

type Client struct {
	base                *url.URL
	http                *http.Client
	username, password  string
	Limit               int64
	Version, APIVersion string
}

func New(base, user, password string, timeout time.Duration, limit int64) (*Client, error) {
	normalized, e := Normalize(base)
	if e != nil {
		return nil, e
	}
	u, _ := url.Parse(normalized)
	jar, _ := cookiejar.New(nil)
	tr := &http.Transport{MaxConnsPerHost: 1, MaxIdleConns: 2, MaxIdleConnsPerHost: 1, IdleConnTimeout: 90 * time.Second, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, errors.New("invalid_target")
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, errors.New("dns_failed")
		}
		for _, ip := range ips {
			if blocked(ip) {
				return nil, errors.New("target_not_allowed")
			}
		}
		var conn net.Conn
		for _, ip := range ips {
			conn, e = (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("network_unreachable")
	}}
	return &Client{base: u, http: &http.Client{Transport: guard{u, tr}, Jar: jar, Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, username: user, password: password, Limit: limit}, nil
}
func (c *Client) read(ctx context.Context, ep string, q url.Values, form url.Values) ([]byte, error) {
	b, _, e := c.request(ctx, ep, q, form)
	return b, e
}

// request also returns the response headers, which only Login needs: whether a
// session was established is decided by the Set-Cookie it received.
func (c *Client) request(ctx context.Context, ep string, q url.Values, form url.Values) ([]byte, http.Header, error) {
	method := Allowed[ep]
	if method == "" {
		return nil, nil, errors.New("upstream_not_allowed")
	}
	u := *c.base
	u.Path += ep
	u.RawQuery = q.Encode()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, e := http.NewRequestWithContext(ctx, method, u.String(), body)
	if e != nil {
		return nil, nil, errors.New("request_invalid")
	}
	origin := c.base.Scheme + "://" + c.base.Host
	req.Header.Set("Origin", origin)
	req.Header.Set("Referer", c.base.String()+"/")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	res, e := c.http.Do(req)
	if e != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, nil, ctx.Err()
		}
		return nil, nil, errors.New("network_dns_tls_or_timeout")
	}
	defer res.Body.Close()
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return nil, res.Header, fmt.Errorf("%w: upstream_http_%d body=%q", ErrAuth, res.StatusCode, limitString(readSnippet(res.Body, 160), 160))
	}
	// Redirects are never followed (a redirected login would hand the credentials
	// to another host), so report them explicitly instead of as a bare non-200.
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return nil, res.Header, fmt.Errorf("upstream_redirect_%d to %s", res.StatusCode, limitString(res.Header.Get("Location"), 120))
	}
	// qBittorrent 5.2.0 changed a successful login from 200 "Ok." to 204 with an
	// empty body (the SID cookie is still set). Callers that cannot use an empty
	// body reject it themselves; only Login accepts one.
	if res.StatusCode == 204 {
		return nil, res.Header, nil
	}
	if res.StatusCode != 200 {
		return nil, res.Header, fmt.Errorf("upstream_http_%d body=%q", res.StatusCode, limitString(readSnippet(res.Body, 160), 160))
	}
	b, e := io.ReadAll(io.LimitReader(res.Body, c.Limit+1))
	if e != nil || int64(len(b)) > c.Limit {
		return nil, res.Header, errors.New("response_incomplete_or_oversize")
	}
	return b, res.Header, nil
}

// readSnippet reads at most n bytes of a body for an error message. qBittorrent
// error bodies are short plain text; nothing sensitive is echoed back to the UI
// because only status lines and error text reach this path.
func readSnippet(r io.Reader, n int) string {
	b, _ := io.ReadAll(io.LimitReader(r, int64(n)))
	return strings.TrimSpace(string(b))
}
func limitString(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
func (c *Client) Login(ctx context.Context) error {
	b, h, e := c.request(ctx, "/api/v2/auth/login", nil, url.Values{"username": {c.username}, "password": {c.password}})
	if e != nil {
		return e
	}
	// qBittorrent <5.2 answers "Ok." with 200, 5.2+ answers 204 with no body.
	// Accept both; in either case the session exists only if a SID came with it.
	if s := strings.TrimSpace(string(b)); s != "Ok." && s != "" {
		return fmt.Errorf("%w: login_rejected body=%q", ErrAuth, limitString(s, 80))
	}
	// Check the jar for the URL the next call will actually use, so a session
	// is only considered established if the SID will really be sent.
	next := *c.base
	next.Path += "/api/v2/sync/maindata"
	for _, cookie := range c.http.Jar.Cookies(&next) {
		if isSessionCookie(cookie.Name) && cookie.Value != "" {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrAuth, describeMissingSID(h))
}

// isSessionCookie recognises both names qBittorrent has used for the WebUI
// session cookie: plain SID up to 5.0, and QBT_SID_<port> from 5.1 on, which
// keeps instances on different ports of one host from overwriting each other.
func isSessionCookie(name string) bool {
	return name == "SID" || strings.HasPrefix(name, "QBT_SID_")
}

// describeMissingSID says why no session cookie will be sent with the next
// request: either
// the upstream set no cookie at all, or it set one the jar will not replay (a
// mismatched path, a foreign domain, or Secure over plain HTTP). Cookie names
// and attributes are reported; cookie values are session secrets and are not.
func describeMissingSID(h http.Header) string {
	set := (&http.Response{Header: h}).Cookies()
	if len(set) == 0 {
		return "no_set_cookie (login accepted but no cookie was returned)"
	}
	var parts []string
	for _, c := range set {
		attrs := []string{"path=" + c.Path}
		if c.Domain != "" {
			attrs = append(attrs, "domain="+c.Domain)
		}
		if c.Secure {
			attrs = append(attrs, "secure")
		}
		if c.MaxAge < 0 {
			attrs = append(attrs, "deleted")
		}
		parts = append(parts, c.Name+"["+strings.Join(attrs, " ")+"]")
	}
	return "sid_not_usable (upstream set: " + limitString(strings.Join(parts, ", "), 200) + ")"
}
func (c *Client) Versions(ctx context.Context) error {
	for _, ep := range []string{"/api/v2/app/version", "/api/v2/app/webapiVersion"} {
		b, e := c.read(ctx, ep, nil, nil)
		if e != nil {
			return e
		}
		s := strings.TrimSpace(string(b))
		if s == "" || len(s) > 64 || strings.ContainsAny(s, "<>\r\n") {
			return errors.New("invalid_version")
		}
		if strings.HasSuffix(ep, "/version") {
			c.Version = s
		} else {
			c.APIVersion = s
		}
	}
	return nil
}

type Patch struct {
	Name, State, Category, Tags                            *string
	Up, Down, Uploaded, Downloaded, Size, Added, Completed *int64
	Progress, Ratio, Availability                          *float64
	Peers, Seeds                                           *int
}

func (p *Patch) UnmarshalJSON(b []byte) error {
	var x struct {
		Name         *string  `json:"name"`
		State        *string  `json:"state"`
		Category     *string  `json:"category"`
		Tags         *string  `json:"tags"`
		Up           *int64   `json:"upspeed"`
		Down         *int64   `json:"dlspeed"`
		Uploaded     *int64   `json:"uploaded"`
		Downloaded   *int64   `json:"downloaded"`
		Size         *int64   `json:"size"`
		Added        *int64   `json:"added_on"`
		Completed    *int64   `json:"completion_on"`
		Progress     *float64 `json:"progress"`
		Ratio        *float64 `json:"ratio"`
		Availability *float64 `json:"availability"`
		Peers        *int     `json:"num_leechs"`
		Seeds        *int     `json:"num_seeds"`
	}
	if e := json.Unmarshal(b, &x); e != nil {
		return errors.New("invalid_torrent_fields")
	}
	for _, v := range []*int64{x.Up, x.Down, x.Uploaded, x.Downloaded} {
		if v != nil && *v < 0 {
			return errors.New("invalid_counter_or_speed")
		}
	}
	*p = Patch{x.Name, x.State, x.Category, x.Tags, x.Up, x.Down, x.Uploaded, x.Downloaded, x.Size, x.Added, x.Completed, x.Progress, x.Ratio, x.Availability, x.Peers, x.Seeds}
	return nil
}

type Server struct {
	Up         *int64 `json:"up_info_speed"`
	Down       *int64 `json:"dl_info_speed"`
	Uploaded   *int64 `json:"alltime_ul"`
	Downloaded *int64 `json:"alltime_dl"`
}
type Sync struct {
	RID      *int64           `json:"rid"`
	Full     *bool            `json:"full_update"`
	Torrents map[string]Patch `json:"torrents"`
	Removed  []string         `json:"torrents_removed"`
	Server   Server           `json:"server_state"`
}

func (c *Client) Sync(ctx context.Context, rid int64) (Sync, error) {
	var s Sync
	b, e := c.read(ctx, "/api/v2/sync/maindata", url.Values{"rid": {strconv.FormatInt(rid, 10)}}, nil)
	if e != nil {
		return s, e
	}
	if e = json.Unmarshal(b, &s); e != nil || s.RID == nil || *s.RID < 0 {
		return Sync{}, errors.New("invalid_sync_response")
	}
	// qBittorrent only emits full_update on a full update and omits it entirely
	// on incremental ones, so an absent field means false. Callers dereference
	// it, so it is normalised here rather than left nil.
	full := s.Full != nil && *s.Full
	s.Full = &full
	// rid=0 asks for a baseline; anything less cannot start a series.
	if rid == 0 && !full {
		return Sync{}, errors.New("invalid_sync_baseline")
	}
	for _, v := range []*int64{s.Server.Up, s.Server.Down, s.Server.Uploaded, s.Server.Downloaded} {
		if v != nil && *v < 0 {
			return Sync{}, errors.New("invalid_server_fields")
		}
	}
	return s, nil
}
func (c *Client) Info(ctx context.Context) (map[string]Patch, error) {
	b, e := c.read(ctx, "/api/v2/torrents/info", nil, nil)
	if e != nil {
		return nil, e
	}
	if !bytes.HasPrefix(bytes.TrimSpace(b), []byte("[")) {
		return nil, errors.New("invalid_full_list")
	}
	var rows []json.RawMessage
	if e = json.Unmarshal(b, &rows); e != nil {
		return nil, errors.New("invalid_full_list")
	}
	out := map[string]Patch{}
	for _, r := range rows {
		var key struct {
			Hash string `json:"hash"`
		}
		var p Patch
		if json.Unmarshal(r, &key) != nil || json.Unmarshal(r, &p) != nil || key.Hash == "" {
			return nil, errors.New("invalid_full_list")
		}
		out[key.Hash] = p
	}
	return out, nil
}
