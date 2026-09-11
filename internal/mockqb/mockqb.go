// Package mockqb is a small in-process qBittorrent WebUI API simulator used by
// tests, the benchmark and local demos. It records every request so tests can
// prove that only the read-only whitelist is ever called.
package mockqb

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// sessionCookie is the name qBittorrent 5.1+ gives the WebUI session cookie
// (QBT_SID_<port>); earlier versions called it SID.
const sessionCookie = "QBT_SID_8080"

type Torrent struct {
	Key, Name, State, Category, Tags string
	Up, Down, Uploaded, Downloaded   int64
	Size                             int64
	Progress, Ratio                  float64
	Peers, Seeds                     int
	Added, Completed                 int64
	Scenario                         string
	dirty                            map[string]bool
	phase                            float64
}

// Scenarios: zero | steady | ramp | spike | random | counter_only | paused.
type Server struct {
	mu          sync.Mutex
	torrents    map[string]*Torrent
	order       []string
	rid         int64
	removed     []string
	requests    []string
	violations  []string
	Version     string
	APIVersion  string
	Username    string
	Password    string
	Mode        string // "" | html | truncated | error | forbidden | hang | badjson | missingfields
	Delay       time.Duration
	AlltimeUL   int64
	AlltimeDL   int64
	rng         *rand.Rand
	tick        int64
	loginCount  int
	syncCount   int
	incremental bool
}

func New() *Server {
	return &Server{torrents: map[string]*Torrent{}, Version: "v5.0.4", APIVersion: "2.11.2", Username: "admin", Password: "adminadmin", rng: rand.New(rand.NewSource(7)), incremental: true}
}

// Populate adds n torrents cycling through the given scenarios.
func (s *Server) Populate(n int, scenarios ...string) {
	if len(scenarios) == 0 {
		scenarios = []string{"steady", "zero", "ramp", "spike", "random", "counter_only", "paused"}
	}
	for i := 0; i < n; i++ {
		sc := scenarios[i%len(scenarios)]
		s.Add(Torrent{Key: fmt.Sprintf("%040x", i+1), Name: fmt.Sprintf("Demo.%s.%03d.2026.WEB-DL.1080p.H264-GRP", strings.ToUpper(sc), i+1), State: "uploading", Category: []string{"movies", "tv", "music"}[i%3], Tags: []string{"pt,keep", "pt", ""}[i%3], Size: 4 << 30, Progress: 1, Ratio: 1.5, Peers: 3, Seeds: 9, Added: 1757000000, Completed: 1757003600, Scenario: sc, Uploaded: int64(i+1) * 1 << 30, Downloaded: 4 << 30})
	}
}
func (s *Server) Add(t Torrent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t.dirty = map[string]bool{"all": true}
	if t.Scenario == "paused" {
		t.State = "pausedUP"
	}
	tt := t
	s.torrents[t.Key] = &tt
	s.order = append(s.order, t.Key)
	s.rid++
}
func (s *Server) Remove(keys ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if _, ok := s.torrents[k]; ok {
			delete(s.torrents, k)
			s.removed = append(s.removed, k)
		}
	}
	s.rid++
}
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for _, k := range s.order {
		if _, ok := s.torrents[k]; ok {
			out = append(out, k)
		}
	}
	return out
}
func (s *Server) Get(key string) (Torrent, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.torrents[key]
	if !ok {
		return Torrent{}, false
	}
	return *t, true
}
func (s *Server) Set(key string, f func(*Torrent)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.torrents[key]; ok {
		f(t)
		t.dirty["all"] = true
		s.rid++
	}
}

// Step advances every scenario by dt seconds; call it once per simulated second.
func (s *Server) Step(dt float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tick++
	var up, down int64
	for _, t := range s.torrents {
		prevUp, prevDown := t.Up, t.Down
		mib := 1048576.0
		switch t.Scenario {
		case "zero", "paused":
			t.Up, t.Down = 0, 0
		case "steady":
			t.phase += dt
			t.Up = int64(mib * (1 + 0.05*math.Sin(t.phase/7)))
			t.Down = int64(0.5 * mib * (1 + 0.04*math.Cos(t.phase/11)))
		case "ramp":
			t.phase += dt
			t.Up = int64(200*1024 + t.phase*512)
			t.Down = int64(math.Max(0, 2*mib-t.phase*1024))
		case "spike":
			t.phase += dt
			t.Up = 300 * 1024
			if int64(t.phase)%97 == 0 {
				t.Up = int64(20 * mib)
			}
			t.Down = 0
		case "random":
			t.Up = int64(s.rng.Float64() * 8 * mib)
			t.Down = int64(s.rng.Float64() * 3 * mib)
		case "counter_only":
			t.Up, t.Down = 0, 0
			t.Uploaded += int64(4096 * dt)
		}
		t.Uploaded += int64(float64(t.Up) * dt)
		t.Downloaded += int64(float64(t.Down) * dt)
		if t.Up != prevUp {
			t.dirty["upspeed"] = true
		}
		if t.Down != prevDown {
			t.dirty["dlspeed"] = true
		}
		if t.Up > 0 || t.Scenario == "counter_only" {
			t.dirty["uploaded"] = true
		}
		if t.Down > 0 {
			t.dirty["downloaded"] = true
		}
		up += t.Up
		down += t.Down
	}
	s.AlltimeUL += int64(float64(up) * dt)
	s.AlltimeDL += int64(float64(down) * dt)
	s.rid++
}
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.requests...)
}
func (s *Server) Violations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.violations...)
}
func (s *Server) Counts() (logins, syncs int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loginCount, s.syncCount
}
func (s *Server) SetMode(m string) { s.mu.Lock(); s.Mode = m; s.mu.Unlock() }

// SetPassword changes what the server accepts, without touching what an
// already-built client holds (used to simulate a rotated qB password).
func (s *Server) SetPassword(p string) { s.mu.Lock(); s.Password = p; s.mu.Unlock() }

var allowed = map[string]string{"/api/v2/auth/login": "POST", "/api/v2/app/version": "GET", "/api/v2/app/webapiVersion": "GET", "/api/v2/sync/maindata": "GET", "/api/v2/torrents/info": "GET"}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.Method+" "+r.URL.Path)
		if allowed[r.URL.Path] != r.Method {
			s.violations = append(s.violations, r.Method+" "+r.URL.Path)
		}
		mode, delay := s.Mode, s.Delay
		s.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		if mode == "hang" {
			time.Sleep(5 * time.Second)
			return
		}
		if r.URL.Path != "/api/v2/auth/login" {
			c, e := r.Cookie(sessionCookie)
			if e != nil || c.Value != "mock-sid" || mode == "forbidden" {
				w.WriteHeader(403)
				fmt.Fprint(w, "Forbidden")
				return
			}
		}
		switch r.URL.Path {
		case "/api/v2/auth/login":
			s.mu.Lock()
			s.loginCount++
			user, pass := s.Username, s.Password
			s.mu.Unlock()
			r.ParseForm()
			if r.FormValue("username") != user || r.FormValue("password") != pass {
				w.WriteHeader(401)
				fmt.Fprint(w, "Unauthorized")
				return
			}
			// Mirrors qBittorrent 5.2: 204 with no body, session cookie named
			// QBT_SID_<port> rather than the SID used up to 5.0.
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "mock-sid", Path: "/"})
			w.WriteHeader(204)
		case "/api/v2/app/version":
			fmt.Fprint(w, s.Version)
		case "/api/v2/app/webapiVersion":
			fmt.Fprint(w, s.APIVersion)
		case "/api/v2/sync/maindata":
			s.maindata(w, r, mode)
		case "/api/v2/torrents/info":
			s.info(w)
		default:
			w.WriteHeader(404)
		}
	})
}
func (s *Server) row(t *Torrent, full bool) map[string]any {
	if full || t.dirty["all"] {
		return map[string]any{"name": t.Name, "state": t.State, "category": t.Category, "tags": t.Tags, "upspeed": t.Up, "dlspeed": t.Down, "uploaded": t.Uploaded, "downloaded": t.Downloaded, "size": t.Size, "progress": t.Progress, "ratio": t.Ratio, "num_leechs": t.Peers, "num_seeds": t.Seeds, "added_on": t.Added, "completion_on": t.Completed, "availability": 1.0}
	}
	m := map[string]any{}
	for k := range t.dirty {
		switch k {
		case "upspeed":
			m[k] = t.Up
		case "dlspeed":
			m[k] = t.Down
		case "uploaded":
			m[k] = t.Uploaded
		case "downloaded":
			m[k] = t.Downloaded
		}
	}
	return m
}
func (s *Server) maindata(w http.ResponseWriter, r *http.Request, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncCount++
	rid, _ := strconv.ParseInt(r.URL.Query().Get("rid"), 10, 64)
	full := rid == 0 || !s.incremental
	switch mode {
	case "html":
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body>login required</body></html>")
		return
	case "error":
		w.WriteHeader(500)
		return
	case "badjson":
		fmt.Fprint(w, `{"rid":`)
		return
	}
	torrents := map[string]any{}
	for k, t := range s.torrents {
		row := s.row(t, full)
		if mode == "missingfields" {
			delete(row, "upspeed")
			delete(row, "uploaded")
		}
		if len(row) > 0 {
			torrents[k] = row
		}
		t.dirty = map[string]bool{}
	}
	out := map[string]any{"rid": s.rid, "torrents": torrents, "server_state": map[string]any{"up_info_speed": s.totalUp(), "dl_info_speed": s.totalDown(), "alltime_ul": s.AlltimeUL, "alltime_dl": s.AlltimeDL}}
	// Like the real qBittorrent, full_update is present only on a full update
	// and omitted on incremental ones.
	if full {
		out["full_update"] = true
	}
	if !full && len(s.removed) > 0 {
		out["torrents_removed"] = s.removed
	}
	s.removed = nil
	b, _ := json.Marshal(out)
	if mode == "truncated" {
		b = b[:len(b)/2]
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
func (s *Server) totalUp() int64 {
	var n int64
	for _, t := range s.torrents {
		n += t.Up
	}
	return n
}
func (s *Server) totalDown() int64 {
	var n int64
	for _, t := range s.torrents {
		n += t.Down
	}
	return n
}
func (s *Server) info(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	arr := []any{}
	for k, t := range s.torrents {
		row := s.row(t, true)
		row["hash"] = k
		arr = append(arr, row)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(arr)
}
