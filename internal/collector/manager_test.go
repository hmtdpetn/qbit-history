package collector

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"qbit-history/internal/mockqb"
	"qbit-history/internal/model"
	"qbit-history/internal/security"
	"qbit-history/internal/store"
)

var testKey = []byte("0123456789abcdef0123456789abcdef")

// managerEnv wires a real Manager (collectors + writer loop) onto a mock qB.
func managerEnv(t *testing.T, torrents int) (*Manager, *store.Store, *mockqb.Server, func()) {
	t.Helper()
	db, e := store.Open(filepath.Join(t.TempDir(), "m.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	m := mockqb.New()
	m.Populate(torrents, "steady", "zero")
	srv := httptest.NewServer(m.Handler())
	go func() {
		for range time.Tick(200 * time.Millisecond) {
			m.Step(0.2)
		}
	}()
	enc, _ := security.Encrypt(testKey, "I", m.Password)
	if e := db.SaveInstance(model.Instance{ID: "I", Name: "I", BaseURL: srv.URL, Username: m.Username, Secret: enc, PollEnabled: true}); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	mgr := NewManager(ctx, db, testKey)
	return mgr, db, m, func() { cancel(); mgr.StopCollectors(); srv.Close(); db.Close() }
}

// TestManagerWriterPersistsAndSeals drives the real writer schedule: collectors
// poll, Step(30) commits the queue as frames, later Steps seal them into blocks
// and rollups, and list statistics are refreshed from the summaries.
func TestManagerWriterPersistsAndSeals(t *testing.T) {
	mgr, db, mock, done := managerEnv(t, 6)
	defer done()
	if e := mgr.Reload(); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if sts, ts := mgr.Snapshots(); len(sts) == 1 && sts[0].State == "online" && len(ts) == 6 && sts[0].Rounds >= 3 {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	sts, ts := mgr.Snapshots()
	if len(sts) != 1 || sts[0].State != "online" || len(ts) != 6 {
		t.Fatalf("collector did not come up: %+v (%d torrents)", sts, len(ts))
	}
	if db.Count("ingest_frame") != 0 {
		t.Fatal("nothing should be persisted before the 30 s flush tick")
	}
	// n%30 == 0 triggers Flush; every Step also attempts sealing.
	mgr.Step(ctx, 30)
	frames := db.Count("ingest_frame")
	if frames == 0 {
		t.Fatal("Step(30) did not commit the queue")
	}
	// Snapshots are persisted too, so a restart keeps the known torrent set.
	stored, e := db.Torrents()
	if e != nil || len(stored) != 6 {
		t.Fatalf("torrents persisted: %d (%v)", len(stored), e)
	}
	for _, x := range stored {
		if x.Name == "" {
			t.Fatal("snapshot metadata was not written")
		}
	}
	// Sealing only fires once a five-minute window is old enough; force it with a shifted clock.
	mgr.Clock = func() time.Time { return time.Now().Add(20 * time.Minute) }
	mgr.Step(ctx, 31)
	if db.Count("series_block") == 0 || db.Count("rollup_open") == 0 {
		t.Fatalf("sealing produced no blocks: raw=%d rollup=%d frames=%d", db.Count("series_block"), db.Count("rollup_open"), db.Count("ingest_frame"))
	}
	if db.Count("ingest_frame") >= frames {
		t.Fatal("sealed frames were not removed")
	}
	// Statistics come from the summaries plus the unsealed tail, without decoding a whole day.
	for i := 0; i < 40; i++ {
		mgr.refreshStats(ctx, time.Now().UnixMilli(), 8)
	}
	_, ts = mgr.Snapshots()
	filled := 0
	for _, x := range ts {
		if x.Upload24h != nil {
			filled++
		}
	}
	if filled != 6 {
		t.Fatalf("only %d/6 torrents have cached statistics", filled)
	}
	if v := mock.Violations(); len(v) != 0 {
		t.Fatalf("upstream violations: %v", v)
	}
	if mgr.Error() != "" {
		t.Fatalf("manager reported %q", mgr.Error())
	}
}

// TestManagerStopsCollectorForDisabledInstance covers the non-running states the
// UI shows (monitoring stopped, credentials unusable) and the removal path.
func TestManagerStopsCollectorForDisabledInstance(t *testing.T) {
	mgr, db, _, done := managerEnv(t, 3)
	defer done()
	if e := mgr.Reload(); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if sts, _ := mgr.Snapshots(); len(sts) == 1 && sts[0].State == "online" {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}
	mgr.Step(context.Background(), 30)
	i, e := db.FindInstance("I")
	if e != nil {
		t.Fatal(e)
	}
	i.PollEnabled = false
	if e = db.SaveInstanceKeepingSecret(i, true); e != nil {
		t.Fatal(e)
	}
	if e = mgr.Reload(); e != nil {
		t.Fatal(e)
	}
	sts, ts := mgr.Snapshots()
	if len(sts) != 1 || sts[0].State != "monitoring_stopped" {
		t.Fatalf("expected monitoring_stopped, got %+v", sts)
	}
	if len(ts) != 3 {
		t.Fatalf("stopping collection must keep the last known torrents visible, got %d", len(ts))
	}
	if mgr.Collector("I") != nil {
		t.Fatal("collector still running for a disabled instance")
	}
	// An unusable credential is reported, not silently ignored.
	i.Secret = []byte("not-a-valid-ciphertext")
	i.PollEnabled = true
	if e = db.SaveInstanceKeepingSecret(i, false); e != nil {
		t.Fatal(e)
	}
	if e = mgr.Reload(); e != nil {
		t.Fatal(e)
	}
	sts, _ = mgr.Snapshots()
	if len(sts) != 1 || sts[0].State != "credential_error" || mgr.Error() == "" {
		t.Fatalf("expected credential_error with a message, got %+v / %q", sts, mgr.Error())
	}
	// Removing the connection stops it and marks its history for deletion.
	if e = mgr.Remove("I"); e != nil {
		t.Fatal(e)
	}
	if sts, ts = mgr.Snapshots(); len(sts) != 0 || len(ts) != 0 {
		t.Fatalf("removed instance still present: %d instances, %d torrents", len(sts), len(ts))
	}
	if db.Count("tombstone") == 0 {
		t.Fatal("removal did not create tombstones")
	}
	for i := 0; i < 6; i++ {
		db.Cleanup(time.Now().UnixMilli())
	}
	if db.Count("instance") != 0 || db.Count("ingest_frame") != 0 {
		t.Fatalf("cleanup left instance=%d frames=%d", db.Count("instance"), db.Count("ingest_frame"))
	}
}
