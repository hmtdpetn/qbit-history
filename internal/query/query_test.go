package query

import (
	"context"
	"math"
	"path/filepath"
	"strconv"
	"testing"

	"qbit-history/internal/model"
	"qbit-history/internal/store"
)

const day = int64(86400000)

var now = int64(1_760_000_000_000)/store.Window*store.Window + 10*day

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, e := store.Open(filepath.Join(t.TempDir(), "q.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	for _, id := range []string{"A", "B"} {
		if e := s.SaveInstance(model.Instance{ID: id, Name: id, BaseURL: "http://" + id + ":8080", Username: "u", Secret: []byte{1}, PollEnabled: true}); e != nil {
			t.Fatal(e)
		}
	}
	return s
}
func feed(t *testing.T, s *store.Store, sid int64, start int64, seconds int, epoch uint64, up func(i int) int64) []model.Sample {
	ps := make([]model.Sample, seconds)
	var upl int64 = 1 << 32
	for i := range ps {
		u := up(i)
		upl += u
		ps[i] = model.Sample{At: start + int64(i)*1000, Epoch: epoch, Seq: uint64(i), Up: u, Down: u / 2, Uploaded: upl, Downloaded: upl * 2, Valid: model.CoreValid, StepMS: 1000}
	}
	for i := 0; i < len(ps); i += 30 {
		if e := s.Append([]model.Batch{{SeriesID: sid, Samples: ps[i:min(i+30, len(ps))]}}); e != nil {
			t.Fatal(e)
		}
	}
	return ps
}
func seal(t *testing.T, s *store.Store) {
	if _, e := s.SealRaw(context.Background(), now, 100000); e != nil {
		t.Fatal(e)
	}
	if _, e := s.SealSummaries(context.Background(), now, 100000); e != nil {
		t.Fatal(e)
	}
}
func req(start, end int64, metric string) Request {
	return Request{Start: start, End: end, Metric: metric, Counter: "reported", MaxPoints: 8000, Now: now}
}
func num(p Point) float64 { v, _ := strconv.ParseFloat(*p.Value, 64); return v }

func TestThinKeepsExtremesIndependently(t *testing.T) {
	var up, down []Point
	for i := 0; i < 5000; i++ {
		u, d := int64(100), int64(100)
		if i == 1234 {
			u = 100000
		}
		if i == 3456 {
			d = 100000
		}
		up = append(up, point(int64(i)*1000, u, "observation", "raw", 0, 0, 1))
		down = append(down, point(int64(i)*1000, d, "observation", "raw", 0, 0, 1))
	}
	up[4000] = null(4000000, "raw", 0)
	tu, td := Thin(up, 300), Thin(down, 300)
	if len(tu) > 300 || len(td) > 300 {
		t.Fatalf("budget exceeded %d %d", len(tu), len(td))
	}
	find := func(ps []Point, at int64, want float64) bool {
		for _, p := range ps {
			if p.At == at && (p.Value == nil && math.IsNaN(want) || p.Value != nil && num(p) == want) {
				return true
			}
		}
		return false
	}
	if !find(tu, 1234000, 100000) || !find(td, 3456000, 100000) {
		t.Fatal("a peak was lost by thinning")
	}
	if !find(tu, 4000000, math.NaN()) {
		t.Fatal("gap marker lost by thinning")
	}
}

func TestRawWindowIsLossless(t *testing.T) {
	s := openStore(t)
	q := store.NewQueue()
	tor, _ := s.EnsureTorrent("A", "aaaa")
	start := now - 2*3600000
	ps := feed(t, s, tor.SeriesID, start, 3600, 1, func(i int) int64 { return int64(float64(1<<20) * (1 + 0.05*math.Sin(float64(i)/7))) })
	seal(t, s)
	// Unsealed tail lives in the queue only.
	tail := []model.Sample{{At: now - 5000, Epoch: 1, Seq: 99999, Up: 7, Down: 7, Uploaded: 1, Downloaded: 1, Valid: model.CoreValid, StepMS: 1000}}
	q.Push(model.Batch{SeriesID: tor.SeriesID, Samples: tail})
	res, e := Series(context.Background(), s, q, tor.SeriesID, "A", req(start, now, "speed"))
	if e != nil {
		t.Fatal(e)
	}
	obs := 0
	for _, p := range res.Up.Points {
		if p.Kind == "observation" {
			obs++
		}
	}
	if obs != len(ps)+1 || !res.Unpersisted {
		t.Fatalf("observations %d want %d, unpersisted=%v", obs, len(ps)+1, res.Unpersisted)
	}
	if res.Up.Flattened || res.Up.Thinned {
		t.Fatal("raw data must never be flattened or thinned within budget")
	}
	for i, p := range res.Up.Points[:len(ps)] {
		if p.At != ps[i].At || num(p) != float64(ps[i].Up) {
			t.Fatalf("raw point %d altered", i)
		}
	}
	if res.Segments[0].Source != "raw" || res.Coverage < 0.49 || res.Coverage > 0.51 {
		t.Fatalf("segments %+v coverage %f", res.Segments, res.Coverage)
	}
	// The gap between the sealed hour and the queue tail must be a break, not a line.
	var gaps int
	for _, p := range res.Up.Points {
		if p.Value == nil {
			gaps++
		}
	}
	if gaps == 0 {
		t.Fatal("missing gap marker before the tail sample")
	}
	// Cumulative: observed delta ignores the first sample and the discontinuous tail.
	cum, _ := Series(context.Background(), s, q, tor.SeriesID, "A", req(start, now, "cumulative"))
	if cum.ObservedUpload != strconv.FormatInt(ps[len(ps)-1].Uploaded-ps[0].Uploaded, 10) {
		t.Fatalf("observed upload %s", cum.ObservedUpload)
	}
}

func TestOldDataFlattensPeaksAndDrift(t *testing.T) {
	s := openStore(t)
	q := store.NewQueue()
	flat, _ := s.EnsureTorrent("A", "flat")
	drift, _ := s.EnsureTorrent("A", "drift")
	mixed, _ := s.EnsureTorrent("A", "mixed")
	base := now - 80*3600000
	mib := float64(1 << 20)
	feed(t, s, flat.SeriesID, base, 2*3600, 1, func(i int) int64 {
		if i == 17*60+3 {
			return 20 << 20
		}
		return int64(mib * (1 + 0.03*math.Sin(float64(i)/5)))
	})
	feed(t, s, drift.SeriesID, base, 3*3600, 1, func(i int) int64 { return int64(mib * (1 + 0.002*float64(i)/60)) })
	feed(t, s, mixed.SeriesID, base, 600, 1, func(i int) int64 {
		if i%600 < 200 {
			return 0
		}
		return 1 << 20
	})
	seal(t, s)
	ctx := context.Background()
	res, e := Series(ctx, s, q, flat.SeriesID, "A", req(base, base+2*3600000, "speed"))
	if e != nil {
		t.Fatal(e)
	}
	if res.Segments[0].Resolution != 300 || res.Segments[0].Source != "summary" {
		t.Fatalf("expected 300 s summary segment, got %+v", res.Segments)
	}
	if !res.Up.Flattened {
		t.Fatal("±3 % old data should be flattened")
	}
	flatPts, means := 0, 0
	for _, p := range res.Up.Points {
		switch p.Kind {
		case "flat_5pct":
			flatPts++
		case "weighted_mean":
			means++
		}
	}
	// 24 buckets: the spike bucket is a weighted mean; the others form runs no longer than 60 min.
	if means != 1 || flatPts < 4 || flatPts > 8 {
		t.Fatalf("flat points %d, mean points %d", flatPts, means)
	}
	var peak *Point
	for i, p := range res.Up.Extremes {
		if p.Kind == "maximum" && num(p) == float64(20<<20) {
			peak = &res.Up.Extremes[i]
		}
	}
	if peak == nil || peak.At != base+int64(17*60+3)*1000 {
		t.Fatalf("isolated peak lost or moved: %+v", peak)
	}
	// Drift: adjacent buckets are within 5 % but the whole span is not -> several distinct levels, never one line.
	res, _ = Series(ctx, s, q, drift.SeriesID, "A", req(base, base+3*3600000, "speed"))
	levels := map[string]bool{}
	for _, p := range res.Up.Points {
		if p.Kind == "flat_5pct" {
			levels[*p.Value] = true
		}
	}
	if len(levels) < 3 {
		t.Fatalf("drift merged into %d level(s); step-wise merging must be bounded by the whole-span check", len(levels))
	}
	var lo, hi float64 = math.Inf(1), 0
	for _, p := range res.Up.Points {
		if p.Value != nil {
			lo, hi = math.Min(lo, num(p)), math.Max(hi, num(p))
		}
	}
	if hi/lo < 1.2 {
		t.Fatalf("drift trend lost: %f..%f", lo, hi)
	}
	// Zero/non-zero mixed bucket is never flattened.
	res, _ = Series(ctx, s, q, mixed.SeriesID, "A", req(base, base+600000, "speed"))
	for _, p := range res.Up.Points {
		if p.Kind == "flat_5pct" && p.At < base+300000 {
			t.Fatal("bucket mixing zero and non-zero speed was flattened")
		}
	}
	// Window cutting into the middle of a bucket: approximate marker and no out-of-window extremes.
	res, _ = Series(ctx, s, q, flat.SeriesID, "A", req(base+150000, base+450000, "speed"))
	if !res.Segments[0].BoundaryEstimate {
		t.Fatal("partial bucket window not marked as estimate")
	}
	for _, p := range res.Up.Extremes {
		if p.At < base+150000 || p.At >= base+450000 {
			t.Fatalf("extreme outside window returned: %+v", p)
		}
	}
	// Cumulative on old data: counter endpoints only, delta ignores partial buckets.
	res, _ = Series(ctx, s, q, flat.SeriesID, "A", req(base, base+2*3600000, "cumulative"))
	if res.Up.Points[0].Kind != "counter_endpoint" || res.ObservedUpload == "0" {
		t.Fatalf("cumulative summary output wrong: %+v %s", res.Up.Points[0], res.ObservedUpload)
	}
}

func TestOverviewAlignmentAndMissing(t *testing.T) {
	s := openStore(t)
	q := store.NewQueue()
	ga, _ := s.GlobalSeries("A")
	gb, _ := s.GlobalSeries("B")
	start := now - 3600000
	feed(t, s, ga, start, 3600, 1, func(i int) int64 { return 1000 })
	feed(t, s, gb, start, 1800, 1, func(i int) int64 { return 500 }) // B stops after 30 minutes
	seal(t, s)
	r := req(start, now, "speed")
	r.MaxPoints = 4000
	res, e := Overview(context.Background(), s, q, map[string]int64{"A": ga, "B": gb}, r)
	if e != nil {
		t.Fatal(e)
	}
	if !res.Missing || len(res.Composition) != 2 {
		t.Fatalf("missing=%v composition=%v", res.Missing, res.Composition)
	}
	valued, nulls := 0, 0
	for _, p := range res.Up.Points {
		if p.Value == nil {
			nulls++
			continue
		}
		valued++
		if num(p) != 1500 {
			t.Fatalf("aligned sum %f want 1500", num(p))
		}
	}
	if valued == 0 || nulls == 0 {
		t.Fatalf("valued=%d nulls=%d", valued, nulls)
	}
	last := res.Up.Points[len(res.Up.Points)-1]
	if last.Value != nil && last.At >= start+1800000 {
		t.Fatal("second half must be blank when B is missing, not A alone")
	}
}
