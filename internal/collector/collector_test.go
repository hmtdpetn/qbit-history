package collector

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"qbit-history/internal/mockqb"
	"qbit-history/internal/model"
	"qbit-history/internal/security"
	"qbit-history/internal/store"
	"qbit-history/internal/upstream"
)

type harness struct {
	t    *testing.T
	st   *store.Store
	q    *store.Queue
	now  time.Time
	mock map[string]*mockqb.Server
	srv  map[string]*httptest.Server
	key  []byte
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, e := store.Open(filepath.Join(t.TempDir(), "h.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, st: st, q: store.NewQueue(), now: time.UnixMilli(1_760_000_000_000).UTC(), mock: map[string]*mockqb.Server{}, srv: map[string]*httptest.Server{}, key: []byte("0123456789abcdef0123456789abcdef")}
	return h
}
func (h *harness) instance(id string, n int, scenarios ...string) *mockqb.Server {
	m := mockqb.New()
	m.Populate(n, scenarios...)
	srv := httptest.NewServer(m.Handler())
	h.t.Cleanup(srv.Close)
	h.mock[id], h.srv[id] = m, srv
	enc, _ := security.Encrypt(h.key, id, m.Password)
	if e := h.st.SaveInstance(model.Instance{ID: id, Name: id, BaseURL: srv.URL, Username: m.Username, Secret: enc, PollEnabled: true}); e != nil {
		h.t.Fatal(e)
	}
	return m
}
func (h *harness) collector(id string) *Collector {
	m := h.mock[id]
	cl, e := upstream.New(h.srv[id].URL, m.Username, m.Password, 2*time.Second, 8<<20)
	if e != nil {
		h.t.Fatal(e)
	}
	c, e := New(model.Instance{ID: id, Name: id, BaseURL: h.srv[id].URL, Username: m.Username, PollEnabled: true}, cl, h.st, h.q)
	if e != nil {
		h.t.Fatal(e)
	}
	c.Clock = func() time.Time { return h.now }
	return c
}

// ticks advances the simulated clock one second per round and polls every collector.
func (h *harness) ticks(n int, cs ...*Collector) {
	for i := 0; i < n; i++ {
		h.now = h.now.Add(time.Second)
		for _, m := range h.mock {
			m.Step(1)
		}
		for _, c := range cs {
			c.Tick(context.Background(), h.now)
		}
	}
}
func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

func TestEveryTorrentSampledEveryRound(t *testing.T) {
	h := newHarness(t)
	m := h.instance("A", 14) // cycles through all scenarios incl. zero, paused, counter_only
	c := h.collector("A")
	h.ticks(60, c)
	st, ts := c.Snapshot()
	if st.State != "online" || len(ts) != 14 {
		t.Fatalf("state %s torrents %d err %s", st.State, len(ts), st.Error)
	}
	for _, x := range ts {
		if n := len(h.q.Tail(x.SeriesID)); n != 60 {
			t.Fatalf("torrent %s (%s) has %d samples, want 60", x.Name, x.State, n)
		}
	}
	if n := len(h.q.Tail(c.GlobalID())); n != 60 {
		t.Fatalf("global series has %d samples", n)
	}
	logins, syncs := m.Counts()
	if logins != 1 || syncs != 60 {
		t.Fatalf("logins=%d syncs=%d; want one login and one sync per round", logins, syncs)
	}
	if v := m.Violations(); len(v) != 0 {
		t.Fatalf("non-whitelisted requests: %v", v)
	}
	for _, r := range m.Requests() {
		if r != "POST /api/v2/auth/login" && r != "GET /api/v2/app/version" && r != "GET /api/v2/app/webapiVersion" && r != "GET /api/v2/sync/maindata" && r != "GET /api/v2/torrents/info" {
			t.Fatalf("unexpected upstream request %s", r)
		}
	}
	if st.Samples != 60*14 {
		t.Fatalf("logical samples %d", st.Samples)
	}
	// Counter-only torrent: zero speed but growing uploaded counter survives.
	for _, x := range ts {
		if strings.Contains(x.Name, "COUNTER_ONLY") {
			tail := h.q.Tail(x.SeriesID)
			if tail[len(tail)-1].Up != 0 || tail[len(tail)-1].Uploaded <= tail[0].Uploaded {
				t.Fatalf("counter_only torrent counter did not grow")
			}
		}
	}
}

func TestSameHashDifferentInstancesStayIsolated(t *testing.T) {
	h := newHarness(t)
	h.instance("A", 3, "steady")
	h.instance("B", 3, "zero")
	a, b := h.collector("A"), h.collector("B")
	h.ticks(10, a, b)
	_, ta := a.Snapshot()
	_, tb := b.Snapshot()
	if len(ta) != 3 || len(tb) != 3 {
		t.Fatal("torrent counts")
	}
	for _, x := range ta {
		for _, y := range tb {
			if x.Key == y.Key {
				if x.SeriesID == y.SeriesID || x.ID == y.ID {
					t.Fatal("same hash shares identity across instances")
				}
				if x.Sample.Up == 0 || y.Sample.Up != 0 {
					t.Fatalf("values leaked: A up=%d B up=%d", x.Sample.Up, y.Sample.Up)
				}
			}
		}
	}
	if e := h.q.Flush(h.st); e != nil {
		t.Fatal(e)
	}
	for _, x := range ta {
		ps, _ := h.st.Raw(context.Background(), x.SeriesID, 0, h.now.UnixMilli()+1)
		if len(ps) != 10 || ps[0].Up == 0 {
			t.Fatalf("A raw %d samples up=%d", len(ps), ps[0].Up)
		}
	}
}

func TestFailuresDoNotDeleteOrFabricate(t *testing.T) {
	h := newHarness(t)
	m := h.instance("A", 5, "steady")
	c := h.collector("A")
	h.ticks(5, c)
	for _, mode := range []string{"html", "truncated", "badjson", "error"} {
		m.SetMode(mode)
		before := len(h.q.Tail(c.GlobalID()))
		h.ticks(1, c)
		st, ts := c.Snapshot()
		if len(ts) != 5 {
			t.Fatalf("mode %s: torrent count changed to %d", mode, len(ts))
		}
		if st.State != "offline" {
			t.Fatalf("mode %s: state %s", mode, st.State)
		}
		if len(h.q.Tail(c.GlobalID())) != before {
			t.Fatalf("mode %s: a sample was fabricated from cache", mode)
		}
		if h.st.Count("tombstone") != 0 {
			t.Fatalf("mode %s: failure caused deletion", mode)
		}
		m.SetMode("")
		h.advance(40 * time.Second) // past the exponential backoff
		h.ticks(1, c)
		st, _ = c.Snapshot()
		if st.State != "online" {
			t.Fatalf("mode %s: did not recover: %s", mode, st.Error)
		}
		tail := h.q.Tail(c.GlobalID())
		if tail[len(tail)-1].Quality&model.GapBefore == 0 {
			t.Fatalf("mode %s: first sample after failure must carry a gap flag", mode)
		}
	}
	// A full update that lacks core fields is completed with one torrents/info read before sampling.
	m.SetMode("error")
	h.ticks(1, c)
	m.SetMode("missingfields")
	h.advance(40 * time.Second)
	before := len(h.q.Tail(c.GlobalID()))
	h.ticks(1, c)
	st, _ := c.Snapshot()
	if st.State != "online" || len(h.q.Tail(c.GlobalID())) != before+1 {
		t.Fatalf("missingfields should be completed via torrents/info, state=%s err=%s", st.State, st.Error)
	}
	if !contains(m.Requests(), "GET /api/v2/torrents/info") {
		t.Fatal("torrents/info was not used to complete fields")
	}
	_, ts := c.Snapshot()
	for _, x := range ts {
		if x.Sample.Valid != model.CoreValid {
			t.Fatalf("sample with incomplete core fields was recorded: %+v", x.Sample)
		}
	}
}

func TestAuthFailureCoolsDown(t *testing.T) {
	h := newHarness(t)
	m := h.instance("A", 2, "steady")
	c := h.collector("A")   // client keeps the original credential
	m.SetPassword("rotated") // qB side password changed behind our back
	h.ticks(30, c)
	st, _ := c.Snapshot()
	logins, _ := m.Counts()
	if st.State != "authentication_cooldown" || logins != 1 {
		t.Fatalf("state=%s logins=%d; want cooldown after a single attempt", st.State, logins)
	}
	m.SetPassword("adminadmin")
	h.advance(5 * time.Minute)
	h.ticks(1, c)
	if st, _ := c.Snapshot(); st.State != "online" {
		t.Fatalf("expected recovery after cooldown, got %s (%s)", st.State, st.Error)
	}
}

func TestExplicitRemovalFlow(t *testing.T) {
	h := newHarness(t)
	m := h.instance("A", 4, "steady")
	h.instance("B", 4, "steady")
	a, b := h.collector("A"), h.collector("B")
	h.ticks(3, a, b)
	h.advance(3 * time.Minute) // leave the 120 s startup protection that the first round armed
	_, ta := a.Snapshot()
	victim := ta[0]
	m.Remove(victim.Key)
	h.ticks(1, a, b)
	_, ta = a.Snapshot()
	var found *model.Torrent
	for i := range ta {
		if ta[i].Key == victim.Key {
			found = &ta[i]
		}
	}
	if found == nil || !found.Candidate {
		t.Fatal("removed torrent should be a visible deletion candidate, not deleted yet")
	}
	if n := len(h.q.Tail(victim.SeriesID)); n != 3 {
		t.Fatalf("candidate kept being sampled from cache: %d samples", n)
	}
	h.ticks(10, a, b)
	_, ta = a.Snapshot()
	for _, x := range ta {
		if x.Key == victim.Key {
			t.Fatal("torrent should be removed after confirmed absence")
		}
	}
	if !h.st.CheckBarrier(victim.SeriesID) {
		t.Fatal("tombstone missing")
	}
	if h.q.Tail(victim.SeriesID) != nil {
		t.Fatal("queued samples for the removed series were not invalidated")
	}
	_, tb := b.Snapshot()
	for _, y := range tb {
		if y.Key == victim.Key && h.st.CheckBarrier(y.SeriesID) {
			t.Fatal("same hash on instance B was affected")
		}
	}
	if len(tb) != 4 {
		t.Fatal("instance B lost a torrent")
	}
	// Re-adding the same key starts a new generation.
	m.Add(mockqb.Torrent{Key: victim.Key, Name: "readded", State: "uploading", Scenario: "steady"})
	h.ticks(2, a)
	_, ta = a.Snapshot()
	for _, x := range ta {
		if x.Key == victim.Key {
			if x.Generation != victim.Generation+1 || x.SeriesID == victim.SeriesID {
				t.Fatalf("re-added torrent generation %d series %d (old %d/%d)", x.Generation, x.SeriesID, victim.Generation, victim.SeriesID)
			}
			return
		}
	}
	t.Fatal("re-added torrent not observed")
}

func TestRestartProtectionAndBulk(t *testing.T) {
	h := newHarness(t)
	m := h.instance("A", 20, "steady")
	c := h.collector("A")
	h.ticks(3, c)
	if e := h.q.Flush(h.st); e != nil {
		t.Fatal(e)
	}
	_, ts := c.Snapshot()
	if e := h.st.Snapshots(ts); e != nil {
		t.Fatal(e)
	}
	// "Restart": a fresh collector loads 20 known torrents; qB now shows only 15 (5 vanished implicitly).
	keys := m.Keys()
	m.Remove(keys[:5]...)
	c2 := h.collector("A")
	h.ticks(1, c2)
	st, ts2 := c2.Snapshot()
	if len(ts2) != 20 || st.Candidates != 5 {
		t.Fatalf("known=%d candidates=%d; nothing may be deleted during the 120 s startup protection", len(ts2), st.Candidates)
	}
	h.advance(4 * time.Minute)
	h.ticks(1, c2)
	h.advance(31 * time.Second)
	h.ticks(1, c2)
	h.advance(31 * time.Second)
	h.ticks(1, c2)
	_, ts2 = c2.Snapshot()
	if len(ts2) != 15 || h.st.Count("tombstone") != 5 {
		t.Fatalf("after three spaced confirmations the 5 vanished torrents should be gone: known=%d tombstones=%d", len(ts2), h.st.Count("tombstone"))
	}
	// Bulk: 10 of the remaining 15 vanish at once -> protection, then explicit approval.
	m.Remove(m.Keys()[:10]...)
	h.ticks(1, c2)
	for i := 0; i < 3; i++ {
		h.advance(35 * time.Second)
		h.ticks(1, c2)
	}
	st, ts2 = c2.Snapshot()
	if !st.BulkProtection || len(ts2) != 15 {
		t.Fatalf("bulk protection expected: bulk=%v known=%d", st.BulkProtection, len(ts2))
	}
	c2.Approve()
	h.ticks(1, c2)
	st, ts2 = c2.Snapshot()
	if len(ts2) != 5 || st.BulkProtection {
		t.Fatalf("after approval: known=%d bulk=%v", len(ts2), st.BulkProtection)
	}
	if v := m.Violations(); len(v) != 0 {
		t.Fatalf("violations %v", v)
	}
}

func TestSlowUpstreamIsNotStacked(t *testing.T) {
	h := newHarness(t)
	m := h.instance("A", 2, "steady")
	m.Delay = 300 * time.Millisecond
	c := h.collector("A")
	start := time.Now()
	h.ticks(3, c)
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Fatalf("rounds are sequential; elapsed %s", el)
	}
	_, syncs := m.Counts()
	if syncs != 3 {
		t.Fatalf("syncs %d", syncs)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
