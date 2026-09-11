// Package rollup builds and merges time-weighted summary buckets (60 s and 300 s)
// from raw samples. Buckets keep true extremes with their timestamps and counter
// endpoints so that older history stays honest after raw samples expire.
package rollup

import (
	"errors"
	"math"

	"qbit-history/internal/codec"
	"qbit-history/internal/model"
)

type Stat struct {
	Sum                           int64 // Σ value × weight_ms; mean = Sum / Weight
	Weight                        int64 // ms of observed support
	First, Last, Min, Max         int64
	FirstAt, LastAt, MinAt, MaxAt int64
	Zero, Positive                int64
}
type Counter struct {
	First, Last, Delta, Duration int64
	FirstAt, LastAt              int64
	Epoch                        uint64
	Known, Reset                 bool
}
type Bucket struct {
	Start, End           int64
	Resolution           int
	Epoch                uint64
	Count, ValidDuration int64
	Quality              uint16
	Up, Down             Stat
	Uploaded, Downloaded Counter
}

func (s *Stat) add(v, at, weight int64) {
	if s.Weight == 0 && s.Zero == 0 && s.Positive == 0 {
		s.First = v
		s.FirstAt = at
		s.Min = v
		s.Max = v
		s.MinAt = at
		s.MaxAt = at
	}
	s.Last = v
	s.LastAt = at
	if v < s.Min {
		s.Min = v
		s.MinAt = at
	}
	if v > s.Max {
		s.Max = v
		s.MaxAt = at
	}
	s.Sum += v * weight
	s.Weight += weight
	if v == 0 {
		s.Zero++
	} else {
		s.Positive++
	}
}
func (s Stat) Mean() float64 {
	if s.Weight == 0 {
		return 0
	}
	return float64(s.Sum) / float64(s.Weight)
}

// Flat reports whether the whole stat can be drawn as a horizontal trend line:
// every observed value is within ±5 % of the time-weighted mean, there is no
// zero/non-zero mix, and no quality flag (gap, reset, clock change) is present.
func (s Stat) Flat(quality uint16) bool {
	if s.Weight == 0 || quality != 0 || s.Zero > 0 && s.Positive > 0 {
		return false
	}
	m := s.Mean()
	if m == 0 {
		return s.Min == 0 && s.Max == 0
	}
	return float64(s.Min) >= .95*m && float64(s.Max) <= 1.05*m
}
func (c *Counter) add(v int64, at int64, pv int64, p model.Sample, s model.Sample, valid bool) {
	if !c.Known {
		c.First = v
		c.FirstAt = at
		c.Epoch = s.Epoch
		c.Known = true
	}
	c.Last = v
	c.LastAt = at
	if valid && s.Continuous(p) {
		if v >= pv && v-pv <= math.MaxInt64-c.Delta {
			c.Delta += v - pv
			c.Duration += at - p.At
		} else {
			c.Reset = true
		}
	}
}

// Build folds samples into res-second buckets. Speed support is at most one
// target interval per sample and is clipped to the bucket; counter deltas belong
// to the bucket of the right endpoint and gaps contribute no delta.
func Build(samples []model.Sample, res int, previous *model.Sample) []Bucket {
	out := []Bucket{}
	var p model.Sample
	has := previous != nil
	if has {
		p = *previous
	}
	for i, s := range samples {
		start := s.At / int64(res*1000) * int64(res*1000)
		if len(out) == 0 || out[len(out)-1].Start != start || out[len(out)-1].Epoch != s.Epoch {
			out = append(out, Bucket{Start: start, End: start + int64(res*1000), Resolution: res, Epoch: s.Epoch})
		}
		b := &out[len(out)-1]
		b.Count++
		b.Quality |= s.Quality
		weight := s.StepMS
		if i+1 < len(samples) && samples[i+1].Continuous(s) && samples[i+1].At-s.At < weight {
			weight = samples[i+1].At - s.At
		}
		if b.End-s.At < weight {
			weight = b.End - s.At
		}
		if weight < 0 {
			weight = 0
		}
		if has && !s.Continuous(p) {
			b.Quality |= model.GapBefore
		}
		if s.Valid&model.ValidUp != 0 {
			b.Up.add(s.Up, s.At, weight)
		}
		if s.Valid&model.ValidDown != 0 {
			b.Down.add(s.Down, s.At, weight)
		}
		if s.Valid&(model.ValidUp|model.ValidDown) == model.ValidUp|model.ValidDown {
			b.ValidDuration += weight
		}
		if s.Valid&model.ValidUploaded != 0 {
			b.Uploaded.add(s.Uploaded, s.At, p.Uploaded, p, s, has && p.Valid&model.ValidUploaded != 0)
		}
		if s.Valid&model.ValidDownloaded != 0 {
			b.Downloaded.add(s.Downloaded, s.At, p.Downloaded, p, s, has && p.Valid&model.ValidDownloaded != 0)
		}
		if b.Uploaded.Reset || b.Downloaded.Reset {
			b.Quality |= model.CounterReset
		}
		p = s
		has = true
	}
	return out
}
func MergeStat(a, b Stat) Stat {
	if a.Zero+a.Positive == 0 {
		return b
	}
	if b.Zero+b.Positive == 0 {
		return a
	}
	a.Sum += b.Sum
	a.Weight += b.Weight
	if b.FirstAt < a.FirstAt {
		a.First = b.First
		a.FirstAt = b.FirstAt
	}
	if b.LastAt > a.LastAt {
		a.Last = b.Last
		a.LastAt = b.LastAt
	}
	if b.Min < a.Min {
		a.Min = b.Min
		a.MinAt = b.MinAt
	}
	if b.Max > a.Max {
		a.Max = b.Max
		a.MaxAt = b.MaxAt
	}
	a.Zero += b.Zero
	a.Positive += b.Positive
	return a
}
func mergeCounter(a, b Counter) Counter {
	if !a.Known {
		return b
	}
	if !b.Known {
		return a
	}
	a.Last = b.Last
	a.LastAt = b.LastAt
	a.Delta += b.Delta
	a.Duration += b.Duration
	a.Reset = a.Reset || b.Reset || a.Epoch != b.Epoch
	return a
}

// Merge combines two adjacent buckets of the same series (a before b). Sums and
// weights add, extremes keep their original timestamps, counter deltas add; it
// never averages averages.
func Merge(a, b Bucket) Bucket {
	if b.End > a.End {
		a.End = b.End
	}
	a.Count += b.Count
	a.ValidDuration += b.ValidDuration
	a.Quality |= b.Quality
	a.Up = MergeStat(a.Up, b.Up)
	a.Down = MergeStat(a.Down, b.Down)
	a.Uploaded = mergeCounter(a.Uploaded, b.Uploaded)
	a.Downloaded = mergeCounter(a.Downloaded, b.Downloaded)
	return a
}
func ToFive(in []Bucket) []Bucket {
	out := []Bucket{}
	for _, b := range in {
		start := b.Start / 300000 * 300000
		b.Start = start
		b.End = start + 300000
		b.Resolution = 300
		if len(out) > 0 && out[len(out)-1].Start == start && out[len(out)-1].Epoch == b.Epoch {
			out[len(out)-1] = Merge(out[len(out)-1], b)
		} else {
			out = append(out, b)
		}
	}
	return out
}

const MaxBuckets = 3600
const columns = 51

func statCols(s *Stat) []*int64 {
	return []*int64{&s.Sum, &s.Weight, &s.First, &s.Last, &s.Min, &s.Max, &s.FirstAt, &s.LastAt, &s.MinAt, &s.MaxAt, &s.Zero, &s.Positive}
}
func b2i(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
func (b *Bucket) column(c int) int64 {
	switch {
	case c < 7:
		return []int64{b.Start, b.End, int64(b.Resolution), int64(b.Epoch), b.Count, b.ValidDuration, int64(b.Quality)}[c]
	case c < 19:
		return *statCols(&b.Up)[c-7]
	case c < 31:
		return *statCols(&b.Down)[c-19]
	case c < 41:
		return counterCol(&b.Uploaded, c-31)
	default:
		return counterCol(&b.Downloaded, c-41)
	}
}
func counterCol(x *Counter, i int) int64 {
	return []int64{x.First, x.Last, x.Delta, x.Duration, x.FirstAt, x.LastAt, int64(x.Epoch), b2i(x.Known), b2i(x.Reset), 0}[i]
}
func setCounter(x *Counter, i int, v int64) error {
	switch i {
	case 0:
		x.First = v
	case 1:
		x.Last = v
	case 2:
		x.Delta = v
	case 3:
		x.Duration = v
	case 4:
		x.FirstAt = v
	case 5:
		x.LastAt = v
	case 6:
		x.Epoch = uint64(v)
	case 7, 8:
		if v != 0 && v != 1 {
			return errors.New("invalid flag")
		}
		if i == 7 {
			x.Known = v == 1
		} else {
			x.Reset = v == 1
		}
	}
	return nil
}
func (b *Bucket) set(c int, v int64) error {
	switch {
	case c < 7:
		switch c {
		case 0:
			b.Start = v
		case 1:
			b.End = v
		case 2:
			b.Resolution = int(v)
		case 3:
			b.Epoch = uint64(v)
		case 4:
			b.Count = v
		case 5:
			b.ValidDuration = v
		case 6:
			if v < 0 || v > 65535 {
				return errors.New("invalid quality")
			}
			b.Quality = uint16(v)
		}
	case c < 19:
		*statCols(&b.Up)[c-7] = v
	case c < 31:
		*statCols(&b.Down)[c-19] = v
	case c < 41:
		return setCounter(&b.Uploaded, c-31, v)
	default:
		return setCounter(&b.Downloaded, c-41, v)
	}
	return nil
}

func Encode(in []Bucket) ([]byte, error) {
	if len(in) > MaxBuckets {
		return nil, errors.New("rollup count limit")
	}
	raw := codec.EncodeColumns(len(in), columns, func(row, col int) int64 { return in[row].column(col) })
	return codec.Pack("QHS3", raw, len(in))
}
func Decode(in []byte) ([]Bucket, error) {
	raw, n, e := codec.Unpack("QHS3", in)
	if e != nil {
		return nil, e
	}
	if n > MaxBuckets {
		return nil, errors.New("rollup count limit")
	}
	out := make([]Bucket, n)
	if e = codec.DecodeColumns(raw, n, columns, func(row, col int, v int64) error { return out[row].set(col, v) }); e != nil {
		return nil, e
	}
	for _, b := range out {
		if b.End <= b.Start || (b.Resolution != 60 && b.Resolution != 300) || b.Count < 0 || b.Up.Weight < 0 || b.Down.Weight < 0 {
			return nil, errors.New("invalid bucket")
		}
	}
	return out, nil
}
