package codec

import (
	"math"
	"math/rand"
	"testing"

	"qbit-history/internal/model"
)

func gen(n int, r *rand.Rand, shape string) []model.Sample {
	out := make([]model.Sample, n)
	var up, down, upl, dl int64 = 0, 0, 1 << 40, 1 << 41
	at := int64(1_757_000_000_000)
	for i := range out {
		step := int64(1000)
		switch shape {
		case "zero":
		case "steady":
			up = 1048576 + r.Int63n(104857) - 52428
			down = 524288 + r.Int63n(52428) - 26214
		case "random":
			up = r.Int63n(50 << 20)
			down = r.Int63n(50 << 20)
		case "big":
			up = math.MaxInt64 - r.Int63n(1000)
			down = r.Int63n(1 << 62)
			upl = math.MaxInt64 - int64(i)
		case "jitter":
			step = 1000 + r.Int63n(300) - 150
			up = 100
		case "reset":
			if i == n/2 {
				upl = 0
			}
			up = 10
		}
		upl += up
		dl += down
		at += step
		q := uint16(0)
		if shape == "reset" && i == n/2 {
			q = model.CounterReset
		}
		if i%97 == 3 {
			q |= model.GapBefore
		}
		valid := model.CoreValid
		if shape == "partial" && i%5 == 0 {
			valid = model.ValidUp | model.ValidDown
		}
		out[i] = model.Sample{At: at, Epoch: 12345, Seq: uint64(i), Up: up, Down: down, Uploaded: upl, Downloaded: dl, Valid: valid, Quality: q, StepMS: 1000}
	}
	return out
}

func TestRoundTripShapes(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for _, shape := range []string{"zero", "steady", "random", "big", "jitter", "reset", "partial"} {
		for _, n := range []int{1, 2, 30, 300, 5000} {
			in := gen(n, r, shape)
			b, e := Encode(in)
			if e != nil {
				t.Fatalf("%s/%d encode: %v", shape, n, e)
			}
			out, e := Decode(b)
			if e != nil {
				t.Fatalf("%s/%d decode: %v", shape, n, e)
			}
			if len(out) != len(in) {
				t.Fatalf("%s/%d length %d != %d", shape, n, len(out), len(in))
			}
			for i := range in {
				if in[i] != out[i] {
					t.Fatalf("%s/%d sample %d mismatch: %+v != %+v", shape, n, i, in[i], out[i])
				}
			}
		}
	}
}

func TestZeroRunIsTiny(t *testing.T) {
	in := gen(300, rand.New(rand.NewSource(2)), "zero")
	b, _ := Encode(in)
	if len(b) > 120 {
		t.Fatalf("300 all-zero samples encoded to %d bytes; expected a handful of RLE runs", len(b))
	}
	t.Logf("300 zero samples -> %d bytes (%.3f B/sample)", len(b), float64(len(b))/300)
}

func TestCounterOnlySurvives(t *testing.T) {
	in := gen(300, rand.New(rand.NewSource(3)), "zero")
	for i := range in {
		in[i].Uploaded += int64(i) * 4096
	}
	b, _ := Encode(in)
	out, e := Decode(b)
	if e != nil {
		t.Fatal(e)
	}
	if out[299].Uploaded-out[0].Uploaded != 299*4096 || out[299].Up != 0 {
		t.Fatal("zero speed with growing counter was not preserved")
	}
}

func TestCorruptionDetected(t *testing.T) {
	in := gen(300, rand.New(rand.NewSource(4)), "random")
	b, _ := Encode(in)
	for _, pos := range []int{4, 5, 9, 12, 16, 20, len(b) - 1} {
		c := append([]byte{}, b...)
		c[pos] ^= 0x55
		if _, e := Decode(c); e == nil {
			t.Fatalf("byte %d corruption not detected", pos)
		}
	}
	if _, e := Decode(b[:17]); e == nil {
		t.Fatal("short block accepted")
	}
	if _, e := Decode(b); e != nil {
		t.Fatal("original rejected after corruption tests", e)
	}
}

func TestLimits(t *testing.T) {
	if _, e := Encode(make([]model.Sample, MaxSamples+1)); e == nil {
		t.Fatal("over-limit sample count accepted")
	}
	if _, e := Pack("TOOLONG", nil, 0); e == nil {
		t.Fatal("bad magic length accepted")
	}
}

func TestSizePerSample(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	for _, shape := range []string{"zero", "steady", "random", "jitter"} {
		in := gen(300, r, shape)
		b, _ := Encode(in)
		t.Logf("%-7s 300 samples -> %5d bytes  %.2f B/sample", shape, len(b), float64(len(b))/300)
	}
}

func BenchmarkEncode300(b *testing.B) {
	in := gen(300, rand.New(rand.NewSource(6)), "steady")
	for i := 0; i < b.N; i++ {
		Encode(in)
	}
}
func BenchmarkDecode300(b *testing.B) {
	in := gen(300, rand.New(rand.NewSource(6)), "steady")
	enc, _ := Encode(in)
	for i := 0; i < b.N; i++ {
		Decode(enc)
	}
}
