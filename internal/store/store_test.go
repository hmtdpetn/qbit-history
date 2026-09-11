package store

import (
	"context"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"qbit-history/internal/codec"
	"qbit-history/internal/model"
)

const t0 = int64(1_760_000_000_000) / Window * Window

func open(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "t.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	if e := s.SaveInstance(model.Instance{ID: "I", Name: "I", BaseURL: "http://qb:8080", Username: "u", Secret: []byte{1}, PollEnabled: true}); e != nil {
		t.Fatal(e)
	}
	return s
}
func mk(n int, start int64, epoch uint64, seq0 uint64, up func(i int) int64) []model.Sample {
	out := make([]model.Sample, n)
	var upl int64 = 1 << 30
	for i := range out {
		u := up(i)
		upl += u
		out[i] = model.Sample{At: start + int64(i)*1000, Epoch: epoch, Seq: seq0 + uint64(i), Up: u, Down: 0, Uploaded: upl, Downloaded: 5, Valid: model.CoreValid, StepMS: 1000}
	}
	return out
}

// feed appends samples in 30-second frames, like the writer does.
func feed(t *testing.T, s *Store, sid int64, ps []model.Sample) {
	for i := 0; i < len(ps); i += 30 {
		if e := s.Append([]model.Batch{{SeriesID: sid, Samples: ps[i:min(i+30, len(ps))]}}); e != nil {
			t.Fatal(e)
		}
	}
}

func TestAppendSealQuery(t *testing.T) {
	s := open(t)
	tor, _ := s.EnsureTorrent("I", "aaaa")
	other, _ := s.EnsureTorrent("I", "bbbb")
	r := rand.New(rand.NewSource(1))
	ps := mk(40*60, t0, 1, 0, func(i int) int64 { return 1<<20 + r.Int63n(50000) })
	feed(t, s, tor.SeriesID, ps)
	feed(t, s, other.SeriesID, mk(40*60, t0, 1, 0, func(int) int64 { return 0 }))
	if s.Count("ingest_frame") != 2*80 {
		t.Fatalf("frames %d", s.Count("ingest_frame"))
	}
	ctx := context.Background()
	now := t0 + 40*60000 + 4*60000
	n, e := s.SealRaw(ctx, now, 1000)
	if e != nil {
		t.Fatal(e)
	}
	// All 8 windows ended at least 4 min ago (> SealDelay of 3 min): 8 blocks per series.
	if n != 16 || s.Count("series_block") != 16 {
		t.Fatalf("sealed %d blocks=%d", n, s.Count("series_block"))
	}
	if s.Count("ingest_frame") != 0 {
		t.Fatalf("frames left %d", s.Count("ingest_frame"))
	}
	got, e := s.Raw(ctx, tor.SeriesID, 0, now)
	if e != nil || len(got) != len(ps) {
		t.Fatalf("raw %d/%d %v", len(got), len(ps), e)
	}
	for i := range ps {
		if got[i] != ps[i] {
			t.Fatalf("sample %d differs", i)
		}
	}
	bs, _ := s.Summaries(ctx, tor.SeriesID, 60, 0, now)
	if len(bs) != 40 {
		t.Fatalf("60s buckets %d", len(bs))
	}
	var delta int64
	for _, b := range bs {
		delta += b.Uploaded.Delta
	}
	if delta != ps[len(ps)-1].Uploaded-ps[0].Uploaded {
		t.Fatalf("summed counter delta %d != %d", delta, ps[len(ps)-1].Uploaded-ps[0].Uploaded)
	}
	five, _ := s.Summaries(ctx, tor.SeriesID, 300, 0, now)
	if len(five) != 8 {
		t.Fatalf("300s buckets %d", len(five))
	}
	// Re-running seal is a no-op; late samples for sealed windows are dropped, not duplicated.
	if n, _ := s.SealRaw(ctx, now, 1000); n != 0 {
		t.Fatal("second seal did work")
	}
	if e := s.Append([]model.Batch{{SeriesID: tor.SeriesID, Samples: ps[:30]}}); e != nil {
		t.Fatal(e)
	}
	if s.Count("ingest_frame") != 0 {
		t.Fatal("late samples for a sealed window were stored")
	}
	// Hour sealing keeps the same buckets.
	hourNow := t0 + 3*3600000
	if _, e := s.SealSummaries(ctx, hourNow, 1000); e != nil {
		t.Fatal(e)
	}
	if s.Count("rollup_open") != 0 {
		t.Fatalf("rollup_open left %d", s.Count("rollup_open"))
	}
	bs2, _ := s.Summaries(ctx, tor.SeriesID, 60, 0, now)
	if len(bs2) != len(bs) {
		t.Fatalf("after hour seal %d buckets", len(bs2))
	}
	for i := range bs {
		if bs[i] != bs2[i] {
			t.Fatalf("bucket %d changed by hour seal", i)
		}
	}
	// Zero series produced tiny raw blocks.
	var zeroBytes int64
	s.Read.QueryRow("SELECT sum(length(data)) FROM series_block WHERE series_id=? AND tier=0", other.SeriesID).Scan(&zeroBytes)
	t.Logf("zero series: %d bytes for 40 min raw (%.2f B/sample)", zeroBytes, float64(zeroBytes)/2400)
	if zeroBytes > 8*120 {
		t.Fatalf("zero-speed raw blocks too large: %d", zeroBytes)
	}
	var steadyBytes int64
	s.Read.QueryRow("SELECT sum(length(data)) FROM series_block WHERE series_id=? AND tier=0", tor.SeriesID).Scan(&steadyBytes)
	t.Logf("steady series: %d bytes for 40 min raw (%.2f B/sample)", steadyBytes, float64(steadyBytes)/2400)
}

func TestDuplicateFrameAndBlockDedup(t *testing.T) {
	s := open(t)
	tor, _ := s.EnsureTorrent("I", "aaaa")
	ps := mk(600, t0, 1, 0, func(i int) int64 { return 100 })
	feed(t, s, tor.SeriesID, ps)
	ctx := context.Background()
	if _, e := s.SealRaw(ctx, t0+20*60000, 100); e != nil {
		t.Fatal(e)
	}
	// Simulate a frame that survived a crash next to its final block.
	data, _ := codec.Encode(ps[:30])
	if _, e := s.DB.Exec("INSERT INTO ingest_frame VALUES(?,?,?,?,?,?,?)", tor.SeriesID, 1, 0, ps[0].At, ps[29].At+1, 30, data); e != nil {
		t.Fatal(e)
	}
	got, _ := s.Raw(ctx, tor.SeriesID, 0, t0+20*60000)
	if len(got) != 600 {
		t.Fatalf("dedup failed: %d", len(got))
	}
}

func TestEpochChangeMidWindow(t *testing.T) {
	s := open(t)
	tor, _ := s.EnsureTorrent("I", "aaaa")
	a := mk(120, t0, 1, 0, func(int) int64 { return 10 })
	b := mk(120, t0+130000, 2, 0, func(int) int64 { return 20 })
	b[0].Quality = model.GapBefore
	feed(t, s, tor.SeriesID, a)
	feed(t, s, tor.SeriesID, b)
	ctx := context.Background()
	if _, e := s.SealRaw(ctx, t0+20*60000, 100); e != nil {
		t.Fatal(e)
	}
	if s.Count("series_block") != 2 {
		t.Fatalf("two epochs in one window need two blocks, got %d", s.Count("series_block"))
	}
	got, _ := s.Raw(ctx, tor.SeriesID, 0, t0+20*60000)
	if len(got) != 240 {
		t.Fatalf("%d samples", len(got))
	}
	bs, _ := s.Summaries(ctx, tor.SeriesID, 60, 0, t0+20*60000)
	for _, x := range bs {
		if x.Start == t0+120000 && x.Epoch == 2 && x.Quality&model.GapBefore == 0 {
			t.Fatal("gap flag lost across epoch change")
		}
	}
}

func TestTombstoneBarrier(t *testing.T) {
	s := open(t)
	tor, _ := s.EnsureTorrent("I", "aaaa")
	keep, _ := s.EnsureTorrent("I", "bbbb")
	ps := mk(600, t0, 1, 0, func(int) int64 { return 100 })
	feed(t, s, tor.SeriesID, ps)
	feed(t, s, keep.SeriesID, ps)
	ctx := context.Background()
	s.SealRaw(ctx, t0+20*60000, 100)
	if e := s.Barrier(tor.SeriesID); e != nil {
		t.Fatal(e)
	}
	if !s.CheckBarrier(tor.SeriesID) || s.CheckBarrier(keep.SeriesID) {
		t.Fatal("barrier state wrong")
	}
	// Late data is refused after the barrier.
	s.Append([]model.Batch{{SeriesID: tor.SeriesID, Samples: mk(30, t0+3600000, 1, 5000, func(int) int64 { return 1 })}})
	var frames int
	s.Read.QueryRow("SELECT count(*) FROM ingest_frame WHERE series_id=?", tor.SeriesID).Scan(&frames)
	if frames != 0 {
		t.Fatal("late write got through the barrier")
	}
	for i := 0; i < 5; i++ {
		s.Cleanup(t0 + 3600000)
	}
	var left int
	s.Read.QueryRow("SELECT count(*) FROM series_block WHERE series_id=?", tor.SeriesID).Scan(&left)
	if left != 0 {
		t.Fatalf("payload not cleaned: %d", left)
	}
	var cleaned int
	s.Read.QueryRow("SELECT cleaned FROM tombstone WHERE series_id=?", tor.SeriesID).Scan(&cleaned)
	if cleaned != 1 {
		t.Fatal("tombstone not marked cleaned")
	}
	s.Read.QueryRow("SELECT count(*) FROM series_block WHERE series_id=?", keep.SeriesID).Scan(&left)
	if left == 0 {
		t.Fatal("other series lost data")
	}
	// The same key gets a new generation afterwards.
	again, _ := s.EnsureTorrent("I", "aaaa")
	if again.Generation != 2 || again.SeriesID == tor.SeriesID {
		t.Fatalf("generation %d series %d", again.Generation, again.SeriesID)
	}
}

func TestRetentionCleanup(t *testing.T) {
	s := open(t)
	tor, _ := s.EnsureTorrent("I", "aaaa")
	ctx := context.Background()
	now := t0 + 10*86400000
	// Data 8 days old and 2 days old and 1 hour old.
	for _, age := range []int64{8 * 86400000, 2 * 86400000, 3600000} {
		feed(t, s, tor.SeriesID, mk(600, now-age, uint64(age), 0, func(int) int64 { return 1 }))
	}
	if _, e := s.SealRaw(ctx, now, 100); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SealSummaries(ctx, now, 100); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 5; i++ {
		s.Cleanup(now)
	}
	// Sum of bucket/sample counts across sealed blocks and open rows.
	count := func(tier int) int {
		var n, m int
		s.Read.QueryRow("SELECT COALESCE(sum(count),0) FROM series_block WHERE tier=?", tier).Scan(&n)
		s.Read.QueryRow("SELECT COALESCE(sum(count),0) FROM rollup_open WHERE tier=?", tier).Scan(&m)
		return n + m
	}
	if count(0) != 600 {
		t.Fatalf("raw samples after cleanup: %d (only the 1 h old window should remain)", count(0))
	}
	if count(60) != 20 {
		t.Fatalf("60s buckets: %d (2-day-old and 1-hour-old remain, 8-day-old removed)", count(60))
	}
	if count(300) != 4 {
		t.Fatalf("300s buckets: %d (8-day-old beyond 7-day retention removed)", count(300))
	}
	// Retention edge: a block whose end is still inside the window survives.
	s.HoldClock()
	feed(t, s, tor.SeriesID, mk(600, now-9*86400000, 99, 0, func(int) int64 { return 1 }))
	s.SealRaw(ctx, now, 100)
	before := s.Count("series_block")
	s.Cleanup(now)
	if s.Count("series_block") != before {
		t.Fatal("clock hold must pause retention deletion")
	}
	s.ConfirmClock()
	for i := 0; i < 3; i++ {
		s.Cleanup(now)
	}
	if s.Count("series_block") >= before {
		t.Fatal("retention did not resume")
	}
}

func TestRemoveInstanceEventuallyPurges(t *testing.T) {
	s := open(t)
	tor, _ := s.EnsureTorrent("I", "aaaa")
	feed(t, s, tor.SeriesID, mk(300, t0, 1, 0, func(int) int64 { return 1 }))
	if e := s.RemoveInstance("I"); e != nil {
		t.Fatal(e)
	}
	if is, _ := s.Instances(); len(is) != 0 {
		t.Fatal("removed instance still listed")
	}
	for i := 0; i < 5; i++ {
		s.Cleanup(t0 + 3600000)
	}
	if s.Count("instance") != 0 || s.Count("torrent") != 0 || s.Count("series") != 0 || s.Count("ingest_frame") != 0 {
		t.Fatalf("leftovers: instance=%d torrent=%d series=%d frames=%d", s.Count("instance"), s.Count("torrent"), s.Count("series"), s.Count("ingest_frame"))
	}
}

func TestSpacePauseAndResume(t *testing.T) {
	s := open(t)
	tor, _ := s.EnsureTorrent("I", "aaaa")
	r := rand.New(rand.NewSource(2))
	for i := 0; i < 40; i++ {
		feed(t, s, tor.SeriesID, mk(600, t0+int64(i)*600000, 1, uint64(i)*600, func(int) int64 { return r.Int63n(1 << 40) }))
	}
	s.Checkpoint(true)
	u := s.quick()
	if u.Main < 100<<10 {
		t.Fatalf("expected a few hundred KiB of data, got %d", u.Main)
	}
	s.mu.Lock()
	s.settings.Budget = u.Main // tiny budget: directory now exceeds 95 %
	s.mu.Unlock()
	if s.CheckSpace() {
		t.Fatal("should pause when directory exceeds the budget line")
	}
	s.mu.Lock()
	s.settings.Budget = u.Main * 2 // still above the 85 % resume line -> stays paused (hysteresis)
	s.mu.Unlock()
	if s.CheckSpace() {
		t.Fatal("hysteresis: must not resume just below the stop line")
	}
	s.mu.Lock()
	s.settings.Budget = 2 << 30
	s.mu.Unlock()
	if !s.CheckSpace() {
		t.Fatal("should resume once well below the budget")
	}
}

func TestQueueBounds(t *testing.T) {
	q := NewQueue()
	now := int64(1000)
	q.now = func() int64 { return now }
	q.MaxBytes = 96 * 10
	ok := 0
	for i := 0; i < 15; i++ {
		if q.Push(model.Batch{SeriesID: 1, Samples: []model.Sample{{At: int64(i)}}}) {
			ok++
		}
	}
	if ok != 10 || q.Dropped != 5 {
		t.Fatalf("accepted %d dropped %d", ok, q.Dropped)
	}
	if len(q.Tail(1)) != 10 {
		t.Fatal("tail")
	}
	q.Invalidate(1)
	if q.Tail(1) != nil || q.Push(model.Batch{SeriesID: 1, Samples: []model.Sample{{}}}) {
		t.Fatal("invalidated series accepted data")
	}
	q.Push(model.Batch{SeriesID: 2, Samples: []model.Sample{{}}})
	now += 200000
	q.Push(model.Batch{SeriesID: 2, Samples: []model.Sample{{At: 1}}})
	if n := len(q.Tail(2)); n != 1 {
		t.Fatalf("stale pending data should be discarded after MaxSpan, tail=%d", n)
	}
}

func TestOpenRejectsCodecMismatch(t *testing.T) {
	dir := t.TempDir()
	s, e := Open(filepath.Join(dir, "x.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	s.DB.Exec("UPDATE maintenance_state SET value='QHR1' WHERE key='codec:raw'")
	s.Close()
	if _, e := Open(filepath.Join(dir, "x.sqlite")); e == nil {
		t.Fatal("codec mismatch accepted")
	}
	_ = time.Now
}
