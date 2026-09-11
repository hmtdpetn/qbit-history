package rollup

import (
	"math"
	"math/rand"
	"testing"

	"qbit-history/internal/model"
)

// base is aligned to both 60 s and 300 s boundaries.
const base = int64(1_700_000_100_000)

func series(n int, at0 int64, f func(i int) (up, down int64)) []model.Sample {
	out := make([]model.Sample, n)
	var upl, dl int64 = 1 << 30, 1 << 31
	for i := range out {
		up, down := f(i)
		upl += up
		dl += down
		out[i] = model.Sample{At: at0 + int64(i)*1000, Epoch: 9, Seq: uint64(i), Up: up, Down: down, Uploaded: upl, Downloaded: dl, Valid: model.CoreValid, StepMS: 1000}
	}
	return out
}

func TestBuildWeightsAndExtremes(t *testing.T) {
	// 10 minutes, one isolated 20 MiB/s peak at second 130 inside a 300 KiB/s baseline.
	ps := series(600, base, func(i int) (int64, int64) {
		if i == 130 {
			return 20 << 20, 0
		}
		return 300 << 10, 0
	})
	bs := Build(ps, 60, nil)
	if len(bs) != 10 {
		t.Fatalf("expected 10 buckets, got %d", len(bs))
	}
	b := bs[2]
	if b.Up.Max != 20<<20 || b.Up.MaxAt != ps[130].At {
		t.Fatalf("peak lost: max=%d at=%d", b.Up.Max, b.Up.MaxAt)
	}
	if b.Up.Weight != 60000 || b.ValidDuration != 60000 {
		t.Fatalf("weight %d duration %d", b.Up.Weight, b.ValidDuration)
	}
	mean := b.Up.Mean()
	want := (59*float64(300<<10) + float64(20<<20)) / 60
	if math.Abs(mean-want) > 1 {
		t.Fatalf("mean %f want %f", mean, want)
	}
	if b.Uploaded.Delta != 59*(300<<10)+(20<<20) {
		t.Fatalf("counter delta %d", b.Uploaded.Delta)
	}
	// The first bucket has no previous sample: 59 deltas only.
	if bs[0].Uploaded.Delta != 59*(300<<10) {
		t.Fatalf("first bucket delta %d", bs[0].Uploaded.Delta)
	}
}

func TestToFiveMatchesDirectBuildAndIsIdempotent(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	ps := series(3000, 1_700_000_100_000, func(i int) (int64, int64) { return r.Int63n(5 << 20), r.Int63n(1 << 20) })
	one := Build(ps, 60, nil)
	five := ToFive(one)
	direct := Build(ps, 300, nil)
	if len(five) != len(direct) {
		t.Fatalf("%d vs %d buckets", len(five), len(direct))
	}
	for i := range five {
		a, b := five[i], direct[i]
		if a.Up.Sum != b.Up.Sum || a.Up.Weight != b.Up.Weight || a.Up.Max != b.Up.Max || a.Up.MaxAt != b.Up.MaxAt || a.Up.Min != b.Up.Min || a.Uploaded.Delta != b.Uploaded.Delta || a.Downloaded.Delta != b.Downloaded.Delta || a.Uploaded.First != b.Uploaded.First || a.Uploaded.Last != b.Uploaded.Last || a.Count != b.Count {
			t.Fatalf("bucket %d differs:\n%+v\n%+v", i, a, b)
		}
	}
	again := ToFive(five)
	for i := range again {
		if again[i] != five[i] {
			t.Fatalf("ToFive not idempotent at %d", i)
		}
	}
}

func TestMergeDoesNotAverageAverages(t *testing.T) {
	a := Stat{Sum: 100 * 10, Weight: 10, First: 100, Last: 100, Min: 100, Max: 100, Positive: 1, FirstAt: 1, LastAt: 1, MinAt: 1, MaxAt: 1}
	b := Stat{Sum: 200 * 30, Weight: 30, First: 200, Last: 200, Min: 200, Max: 200, Positive: 1, FirstAt: 2, LastAt: 2, MinAt: 2, MaxAt: 2}
	m := MergeStat(a, b)
	if m.Mean() != 175 {
		t.Fatalf("weighted mean %f, want 175 (not 150)", m.Mean())
	}
	if m.First != 100 || m.Last != 200 || m.MinAt != 1 || m.MaxAt != 2 {
		t.Fatalf("endpoints/extremes wrong: %+v", m)
	}
}

func TestCounterResetAndGap(t *testing.T) {
	ps := series(120, 1_700_000_100_000, func(i int) (int64, int64) { return 1000, 0 })
	for i := 60; i < 120; i++ {
		ps[i].Uploaded = int64(i) * 10 // counter went backwards at i=60
	}
	ps[30].Quality |= model.GapBefore
	bs := Build(ps, 60, nil)
	if bs[0].Quality&model.GapBefore == 0 {
		t.Fatal("gap flag not propagated")
	}
	if !bs[1].Uploaded.Reset || bs[1].Quality&model.CounterReset == 0 {
		t.Fatal("counter reset not detected")
	}
	if bs[1].Uploaded.Delta != 59*10 {
		t.Fatalf("delta after reset should only count the new segment, got %d", bs[1].Uploaded.Delta)
	}
	if bs[0].Uploaded.Delta < 0 {
		t.Fatal("negative delta")
	}
}

func TestFlatRule(t *testing.T) {
	mib := int64(1 << 20)
	flat := Stat{Sum: mib * 60000, Weight: 60000, Min: mib * 96 / 100, Max: mib * 104 / 100, Positive: 60}
	if !flat.Flat(0) {
		t.Fatal("±4 % should be flat")
	}
	wide := flat
	wide.Max = mib * 107 / 100
	if wide.Flat(0) {
		t.Fatal("+7 % must not be flat")
	}
	if flat.Flat(model.GapBefore) {
		t.Fatal("quality flag must block flattening")
	}
	mixed := flat
	mixed.Zero = 1
	if mixed.Flat(0) {
		t.Fatal("zero/non-zero mix must not be flat")
	}
	zero := Stat{Weight: 60000, Zero: 60}
	if !zero.Flat(0) {
		t.Fatal("true all-zero is flat")
	}
	low := Stat{Sum: 5 * 60000, Weight: 60000, Min: 0, Max: 300, Positive: 1, Zero: 0}
	if low.Flat(0) {
		t.Fatal("isolated low upload must not be swallowed by an absolute tolerance")
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	ps := series(3600, 1_700_000_100_000, func(i int) (int64, int64) { return r.Int63n(5 << 20), r.Int63n(1 << 20) })
	ps[500].Uploaded = 1
	in := Build(ps, 60, nil)
	b, e := Encode(in)
	if e != nil {
		t.Fatal(e)
	}
	out, e := Decode(b)
	if e != nil {
		t.Fatal(e)
	}
	if len(out) != len(in) {
		t.Fatalf("len %d != %d", len(out), len(in))
	}
	for i := range in {
		if in[i] != out[i] {
			t.Fatalf("bucket %d mismatch\n%+v\n%+v", i, in[i], out[i])
		}
	}
	t.Logf("60 one-minute buckets -> %d bytes (%.1f B/bucket)", len(b), float64(len(b))/float64(len(in)))
	c := append([]byte{}, b...)
	c[len(c)-3] ^= 1
	if _, e := Decode(c); e == nil {
		t.Fatal("corruption not detected")
	}
}
