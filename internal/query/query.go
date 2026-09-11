// Package query turns stored samples and summaries into chart-ready results
// with explicit source, resolution, coverage, gaps and estimate markers.
package query

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"qbit-history/internal/model"
	"qbit-history/internal/rollup"
	"qbit-history/internal/store"
)

type Point struct {
	At         int64   `json:"at"`
	Value      *string `json:"value"`
	Kind       string  `json:"kind"`
	Source     string  `json:"source"`
	Resolution int     `json:"resolution"`
	Quality    uint16  `json:"quality"`
	Approx     bool    `json:"approximate"`
	Epoch      uint64  `json:"epoch"`
}
type Curve struct {
	Points    []Point `json:"points"`
	Extremes  []Point `json:"extremes"`
	Thinned   bool    `json:"thinned"`
	Flattened bool    `json:"flattened"`
}
type Segment struct {
	Start            int64  `json:"start"`
	End              int64  `json:"end"`
	Source           string `json:"source"`
	Resolution       int    `json:"resolution"`
	BoundaryEstimate bool   `json:"boundary_estimate"`
}
type Result struct {
	RequestedStart   int64         `json:"requested_start"`
	RequestedEnd     int64         `json:"requested_end"`
	EffectiveStart   int64         `json:"effective_start"`
	EffectiveEnd     int64         `json:"effective_end"`
	ActualStart      int64         `json:"actual_start"`
	ActualEnd        int64         `json:"actual_end"`
	Metric           string        `json:"metric"`
	CounterMode      string        `json:"counter_mode"`
	Up               Curve         `json:"up"`
	Down             Curve         `json:"down"`
	Segments         []Segment     `json:"segments"`
	Coverage         float64       `json:"coverage"`
	CounterCoverage  float64       `json:"counter_coverage"`
	ObservedUpload   string        `json:"observed_upload_bytes"`
	ObservedDownload string        `json:"observed_download_bytes"`
	Unpersisted      bool          `json:"unpersisted_tail"`
	Watermark        int64         `json:"persisted_watermark"`
	Gaps             []model.Event `json:"gaps"`
	Notes            []string      `json:"notes"`
	Composition      []string      `json:"composition"`
	PeakSemantics    string        `json:"peak_semantics"`
	Missing          bool          `json:"missing_components"`
	Step             int64         `json:"step_ms"`
}
type Request struct {
	Start, End      int64
	Metric, Counter string
	MaxPoints       int
	Now             int64
}

const MaxRange = 31 * 86400000

func Parse(start, end, metric, counter, maxPoints string, now int64) (Request, error) {
	a, e := strconv.ParseInt(start, 10, 64)
	if e != nil {
		return Request{}, errors.New("invalid_start")
	}
	b, e := strconv.ParseInt(end, 10, 64)
	if e != nil || b <= a || a < 0 || b-a > MaxRange {
		return Request{}, errors.New("invalid_range_max_31_days")
	}
	if metric == "" {
		metric = "speed"
	}
	if metric != "speed" && metric != "cumulative" {
		return Request{}, errors.New("invalid_metric")
	}
	if counter == "" {
		counter = "reported"
	}
	if counter != "reported" && counter != "delta" {
		return Request{}, errors.New("invalid_counter_mode")
	}
	n := 2000
	if maxPoints != "" {
		n, e = strconv.Atoi(maxPoints)
		if e != nil {
			return Request{}, errors.New("invalid_points")
		}
	}
	if n < 16 || n > 8000 {
		return Request{}, errors.New("max_points_16_to_8000")
	}
	return Request{a, b, metric, counter, n, now}, nil
}
func point(at int64, v int64, kind, source string, res int, q uint16, epoch uint64) Point {
	s := strconv.FormatInt(v, 10)
	return Point{at, &s, kind, source, res, q, false, epoch}
}
func null(at int64, source string, res int) Point {
	return Point{At: at, Source: source, Resolution: res, Kind: "gap"}
}
func (s Point) number() float64 {
	if s.Value == nil {
		return math.NaN()
	}
	v, _ := strconv.ParseFloat(*s.Value, 64)
	return v
}

// Thin reduces a curve to at most budget points using per-cell first/last/
// min/max selection, keeping gaps and quality boundaries. Each direction is
// thinned independently, so an upload peak never hides a download peak.
func Thin(in []Point, budget int) []Point {
	if len(in) <= budget {
		return in
	}
	cells := max(1, budget/6)
	start, end := in[0].At, in[len(in)-1].At+1
	out := []Point{}
	for pos := 0; pos < len(in); {
		cell := (in[pos].At - start) * int64(cells) / max64(1, end-start)
		last := pos + 1
		for last < len(in) && (in[last].At-start)*int64(cells)/max64(1, end-start) == cell {
			last++
		}
		idx := map[int]bool{pos: true, last - 1: true}
		mi, ma := pos, pos
		gap := -1
		boundary := -1
		for j := pos; j < last; j++ {
			if in[j].Value == nil {
				gap = j
				continue
			}
			if in[mi].Value == nil || in[j].number() < in[mi].number() {
				mi = j
			}
			if in[ma].Value == nil || in[j].number() > in[ma].number() {
				ma = j
			}
			if in[j].Quality != 0 {
				boundary = j
			}
		}
		idx[mi] = true
		idx[ma] = true
		if gap >= 0 {
			idx[gap] = true
		}
		if boundary >= 0 {
			idx[boundary] = true
		}
		keys := []int{}
		for i := range idx {
			keys = append(keys, i)
		}
		sort.Ints(keys)
		for _, i := range keys {
			out = append(out, in[i])
		}
		pos = last
	}
	if len(out) > budget {
		return out[:budget]
	}
	return out
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// tierSpans splits [start,end) into the storage tiers by age: 300 s summaries
// beyond 72 h, 60 s summaries between 72 h and 24 h, raw samples within 24 h.
func tierSpans(start, end, now int64) []struct {
	a, b int64
	tier int
} {
	return []struct {
		a, b int64
		tier int
	}{{start, min64(end, now-72*3600000), 300}, {max64(start, now-72*3600000), min64(end, now-86400000), 60}, {max64(start, now-86400000), end, 0}}
}

// rawSamples merges persisted samples with the not-yet-committed queue tail.
func rawSamples(ctx context.Context, s *store.Store, q *store.Queue, id, a, b int64) ([]model.Sample, bool, error) {
	ps, e := s.Raw(ctx, id, a, b)
	if e != nil {
		return nil, false, e
	}
	unpersisted := false
	for _, p := range q.Tail(id) {
		if p.At >= a && p.At < b {
			ps = append(ps, p)
			unpersisted = true
		}
	}
	return store.Dedup(ps), unpersisted, nil
}

type direction struct {
	curve   *Curve
	st      rollup.Stat
	counter rollup.Counter
}

// flatGroup accumulates adjacent complete buckets that together satisfy the
// ±5 % rule. Buckets are re-checked over the whole candidate span so slow drift
// can never be merged step by step.
type flatGroup struct {
	buckets []rollup.Bucket
	stat    rollup.Stat
	maxSpan int64
}

func (g *flatGroup) accepts(b rollup.Bucket, st rollup.Stat, partial bool) bool {
	if partial || b.Quality != 0 || st.Weight == 0 {
		return false
	}
	if len(g.buckets) == 0 {
		return st.Flat(0)
	}
	last := g.buckets[len(g.buckets)-1]
	if last.End != b.Start || last.Epoch != b.Epoch || b.End-g.buckets[0].Start > g.maxSpan {
		return false
	}
	return rollup.MergeStat(g.stat, st).Flat(0)
}

func Series(ctx context.Context, s *store.Store, q *store.Queue, id int64, instance string, r Request) (Result, error) {
	res := Result{RequestedStart: r.Start, RequestedEnd: r.End, EffectiveStart: max64(r.Start, r.Now-int64(s.Settings().RetentionDays)*86400000), EffectiveEnd: min64(r.End, r.Now+1), Metric: r.Metric, CounterMode: r.Counter, Up: Curve{Points: []Point{}, Extremes: []Point{}}, Down: Curve{Points: []Point{}, Extremes: []Point{}}, Segments: []Segment{}, Gaps: []model.Event{}, Notes: []string{"原始层保留采到的核心值，不保证亚秒峰值。旧摘要保存原生桶极值，并非所有次要峰形。"}, ObservedUpload: "0", ObservedDownload: "0", PeakSemantics: "observed_per_series"}
	if res.EffectiveStart >= res.EffectiveEnd {
		return res, nil
	}
	if s.CheckBarrier(id) {
		return res, errors.New("series_removed")
	}
	var duration, counterDuration, du, dd int64
	for _, span := range tierSpans(res.EffectiveStart, res.EffectiveEnd, r.Now) {
		if span.a >= span.b {
			continue
		}
		if span.tier == 0 {
			ps, unpersisted, e := rawSamples(ctx, s, q, id, span.a, span.b)
			if e != nil {
				return res, e
			}
			res.Unpersisted = res.Unpersisted || unpersisted
			var prev model.Sample
			for i, p := range ps {
				cont := i > 0 && p.Continuous(prev)
				if i > 0 && !cont {
					res.Up.Points = append(res.Up.Points, null(prev.At+1, "raw", 0))
					res.Down.Points = append(res.Down.Points, null(prev.At+1, "raw", 0))
					res.Gaps = append(res.Gaps, model.Event{InstanceID: instance, SeriesID: id, At: prev.At + 1, End: p.At, Kind: "sample_gap", Detail: "采集序号、epoch 或时间不连续"})
				}
				if p.Quality&model.CounterReset != 0 {
					res.Gaps = append(res.Gaps, model.Event{InstanceID: instance, SeriesID: id, At: p.At, End: p.At, Kind: "counter_reset", Detail: "累计计数回退，开启新段"})
				}
				w := min64(p.StepMS, span.b-p.At)
				if p.Valid&(model.ValidUp|model.ValidDown) == model.ValidUp|model.ValidDown {
					duration += w
				}
				up, down := p.Up, p.Down
				uf, df := model.ValidUp, model.ValidDown
				if cont {
					if p.Valid&prev.Valid&model.ValidUploaded != 0 && p.Uploaded >= prev.Uploaded {
						du += p.Uploaded - prev.Uploaded
						counterDuration += p.At - prev.At
					}
					if p.Valid&prev.Valid&model.ValidDownloaded != 0 && p.Downloaded >= prev.Downloaded {
						dd += p.Downloaded - prev.Downloaded
					}
				}
				if r.Metric == "cumulative" {
					up, down = p.Uploaded, p.Downloaded
					uf, df = model.ValidUploaded, model.ValidDownloaded
					if p.Quality&model.CounterReset != 0 {
						res.Up.Points = append(res.Up.Points, null(p.At-1, "raw", 0))
						res.Down.Points = append(res.Down.Points, null(p.At-1, "raw", 0))
					}
					if r.Counter == "delta" {
						up, down = du, dd
					}
				}
				for _, v := range []struct {
					curve *Curve
					value int64
					valid uint8
				}{{&res.Up, up, uf}, {&res.Down, down, df}} {
					if p.Valid&v.valid != 0 {
						v.curve.Points = append(v.curve.Points, point(p.At, v.value, "observation", "raw", 0, p.Quality, p.Epoch))
					} else {
						v.curve.Points = append(v.curve.Points, null(p.At, "raw", 0))
					}
				}
				prev = p
			}
			res.Segments = append(res.Segments, Segment{span.a, span.b, "raw", 0, false})
			continue
		}
		bs, e := s.Summaries(ctx, id, span.tier, span.a, span.b)
		if e != nil {
			return res, e
		}
		res.Segments = append(res.Segments, Segment{span.a, span.b, "summary", span.tier, span.a%int64(span.tier*1000) != 0 || span.b%int64(span.tier*1000) != 0})
		maxSpan := int64(15 * 60000)
		if span.tier == 300 {
			maxSpan = 60 * 60000
		}
		groups := map[*Curve]*flatGroup{&res.Up: {maxSpan: maxSpan}, &res.Down: {maxSpan: maxSpan}}
		var lastEnd int64
		for _, b := range bs {
			partial := b.Start < span.a || b.End > span.b
			left := max64(b.Start, span.a)
			if lastEnd > 0 && b.Start > lastEnd || b.Quality&model.GapBefore != 0 {
				for c, g := range groups {
					emitFlat(c, g, span.tier)
					c.Points = append(c.Points, null(left-1, "summary", span.tier))
				}
				if lastEnd > 0 && b.Start > lastEnd {
					res.Gaps = append(res.Gaps, model.Event{InstanceID: instance, SeriesID: id, At: lastEnd, End: b.Start, Kind: "summary_gap", Detail: "摘要桶之间没有观测"})
				}
			}
			if !partial {
				duration += b.ValidDuration
				counterDuration += b.Uploaded.Duration
				du += b.Uploaded.Delta
				dd += b.Downloaded.Delta
			}
			for _, dir := range []struct {
				direction
				delta int64
			}{{direction{&res.Up, b.Up, b.Uploaded}, du}, {direction{&res.Down, b.Down, b.Downloaded}, dd}} {
				if r.Metric == "cumulative" {
					if !dir.counter.Known {
						dir.curve.Points = append(dir.curve.Points, null(left, "summary", span.tier))
						continue
					}
					if dir.counter.Reset {
						dir.curve.Points = append(dir.curve.Points, null(left, "summary", span.tier))
						res.Gaps = append(res.Gaps, model.Event{InstanceID: instance, SeriesID: id, At: b.Start, End: b.End, Kind: "counter_reset", Detail: "摘要桶内累计计数回退"})
					}
					for _, v := range []struct{ at, value int64 }{{dir.counter.FirstAt, dir.counter.First}, {dir.counter.LastAt, dir.counter.Last}} {
						if v.at < span.a || v.at >= span.b {
							continue
						}
						if r.Counter == "delta" {
							if partial {
								continue
							}
							v.value = dir.delta
						}
						p := point(v.at, v.value, "counter_endpoint", "summary", span.tier, b.Quality, b.Epoch)
						p.Approx = partial
						dir.curve.Points = append(dir.curve.Points, p)
					}
					continue
				}
				g := groups[dir.curve]
				if dir.st.Weight == 0 {
					emitFlat(dir.curve, g, span.tier)
					dir.curve.Points = append(dir.curve.Points, null(left, "summary", span.tier))
					continue
				}
				if g.accepts(b, dir.st, partial) {
					g.buckets = append(g.buckets, b)
					g.stat = rollup.MergeStat(g.stat, dir.st)
				} else {
					emitFlat(dir.curve, g, span.tier)
					if dir.st.Flat(b.Quality) && !partial {
						g.buckets = []rollup.Bucket{b}
						g.stat = dir.st
					} else {
						mean := strconv.FormatFloat(dir.st.Mean(), 'f', 3, 64)
						dir.curve.Points = append(dir.curve.Points, Point{left, &mean, "weighted_mean", "summary", span.tier, b.Quality, partial, b.Epoch})
					}
				}
				for _, v := range []struct {
					at, value int64
					kind      string
				}{{dir.st.MinAt, dir.st.Min, "minimum"}, {dir.st.MaxAt, dir.st.Max, "maximum"}} {
					if v.at < span.a || v.at >= span.b {
						continue
					}
					dir.curve.Extremes = append(dir.curve.Extremes, point(v.at, v.value, v.kind, "summary", span.tier, b.Quality, b.Epoch))
				}
			}
			lastEnd = b.End
		}
		for c, g := range groups {
			emitFlat(c, g, span.tier)
		}
	}
	for _, c := range []*Curve{&res.Up, &res.Down} {
		sort.SliceStable(c.Points, func(i, j int) bool { return c.Points[i].At < c.Points[j].At })
		sort.SliceStable(c.Extremes, func(i, j int) bool { return c.Extremes[i].At < c.Extremes[j].At })
		extBudget := min(len(c.Extremes), r.MaxPoints/3)
		pointBudget := r.MaxPoints - extBudget
		c.Thinned = len(c.Points) > pointBudget || len(c.Extremes) > extBudget
		c.Points = Thin(c.Points, pointBudget)
		if extBudget > 0 {
			c.Extremes = Thin(c.Extremes, extBudget)
		} else {
			c.Extremes = []Point{}
		}
		for _, p := range c.Points {
			if p.Value != nil {
				if res.ActualStart == 0 || p.At < res.ActualStart {
					res.ActualStart = p.At
				}
				if p.At > res.ActualEnd {
					res.ActualEnd = p.At
				}
			}
		}
	}
	res.Coverage = math.Min(1, float64(duration)/float64(res.EffectiveEnd-res.EffectiveStart))
	res.CounterCoverage = math.Min(1, float64(counterDuration)/float64(res.EffectiveEnd-res.EffectiveStart))
	res.ObservedUpload = fmt.Sprint(du)
	res.ObservedDownload = fmt.Sprint(dd)
	res.Watermark = s.CommitWatermark()
	res.Gaps = append(res.Gaps, s.Events(ctx, instance, id, res.EffectiveStart, res.EffectiveEnd)...)
	if len(res.Gaps) > 256 {
		res.Gaps = res.Gaps[:256]
		res.Notes = append(res.Notes, "缺口事件超过响应上限；折线仍保留断点")
	}
	if res.Up.Flattened || res.Down.Flattened {
		res.Notes = append(res.Notes, "旧数据中 ±5% 以内的平稳区间显示为趋势直线；数据库仍保留每个桶的真实极值")
	}
	return res, nil
}

// emitFlat writes out a pending flat group: a two-point horizontal segment at
// the time-weighted mean when it spans more than one bucket or was accepted as
// flat, otherwise nothing (the bucket already produced a point).
func emitFlat(c *Curve, g *flatGroup, tier int) {
	if len(g.buckets) == 0 {
		return
	}
	mean := strconv.FormatFloat(g.stat.Mean(), 'f', 3, 64)
	first, last := g.buckets[0], g.buckets[len(g.buckets)-1]
	c.Points = append(c.Points, Point{first.Start, &mean, "flat_5pct", "summary", tier, first.Quality, true, first.Epoch}, Point{last.End - 1, &mean, "flat_5pct", "summary", tier, last.Quality, true, last.Epoch})
	c.Flattened = true
	g.buckets = nil
	g.stat = rollup.Stat{}
}

type accum struct {
	sum    float64 // Σ mean × weight
	weight int64
}

// aligned returns per-bucket time-weighted mean speed for one series on a
// fixed UTC grid, computed from raw samples (≤24 h) or summary buckets.
func aligned(ctx context.Context, s *store.Store, q *store.Queue, id int64, step, start, end, now int64) (map[int64]accum, map[int64]accum, float64, bool, error) {
	up, down := map[int64]accum{}, map[int64]accum{}
	var duration int64
	unpersisted := false
	add := func(m map[int64]accum, t int64, v float64, w int64) {
		a := m[t]
		a.sum += v * float64(w)
		a.weight += w
		m[t] = a
	}
	for _, span := range tierSpans(start, end, now) {
		if span.a >= span.b {
			continue
		}
		if span.tier == 0 {
			ps, u, e := rawSamples(ctx, s, q, id, span.a, span.b)
			if e != nil {
				return nil, nil, 0, false, e
			}
			unpersisted = unpersisted || u
			for i, p := range ps {
				w := p.StepMS
				if i+1 < len(ps) && ps[i+1].Continuous(p) && ps[i+1].At-p.At < w {
					w = ps[i+1].At - p.At
				}
				w = min64(w, span.b-p.At)
				t := p.At / step * step
				w = min64(w, t+step-p.At)
				if p.Valid&model.ValidUp != 0 {
					add(up, t, float64(p.Up), w)
				}
				if p.Valid&model.ValidDown != 0 {
					add(down, t, float64(p.Down), w)
				}
				if p.Valid&(model.ValidUp|model.ValidDown) == model.ValidUp|model.ValidDown {
					duration += w
				}
			}
			continue
		}
		bs, e := s.Summaries(ctx, id, span.tier, span.a, span.b)
		if e != nil {
			return nil, nil, 0, false, e
		}
		for _, b := range bs {
			if b.Start < span.a || b.End > span.b {
				continue
			}
			t := b.Start / step * step
			if b.Up.Weight > 0 {
				add(up, t, b.Up.Mean(), b.Up.Weight)
			}
			if b.Down.Weight > 0 {
				add(down, t, b.Down.Mean(), b.Down.Weight)
			}
			duration += b.ValidDuration
		}
	}
	return up, down, math.Min(1, float64(duration)/float64(end-start)), unpersisted, nil
}

// Overview sums aligned per-instance means on a common UTC grid. A bucket is
// only emitted when every selected instance has data for it; per-instance
// bucket peaks are never added together and called an aggregate peak.
func Overview(ctx context.Context, s *store.Store, q *store.Queue, ids map[string]int64, r Request) (Result, error) {
	out := Result{RequestedStart: r.Start, RequestedEnd: r.End, EffectiveStart: max64(r.Start, r.Now-int64(s.Settings().RetentionDays)*86400000), EffectiveEnd: min64(r.End, r.Now+1), Metric: "speed", CounterMode: r.Counter, Up: Curve{Points: []Point{}, Extremes: []Point{}}, Down: Curve{Points: []Point{}, Extremes: []Point{}}, Segments: []Segment{}, Gaps: []model.Event{}, Composition: []string{}, Notes: []string{"按共同 UTC 桶对齐；缺少任一组成实例时合计留白。各实例桶峰值不相加。", "累计量总览请分别查看各实例报告的 alltime 计数；连接变化会导致组成分段。"}, PeakSemantics: "aligned_bucket_mean_sum; aggregate_peak_unavailable", ObservedUpload: "unavailable", ObservedDownload: "unavailable"}
	if len(ids) == 0 || out.EffectiveStart >= out.EffectiveEnd {
		return out, nil
	}
	if len(ids) > 16 {
		return out, errors.New("overview_max_16_instances")
	}
	step := int64(s.Settings().Interval) * 1000
	if out.EffectiveStart < r.Now-86400000 {
		step = 300000
		if out.EffectiveStart >= r.Now-72*3600000 {
			step = 60000
		}
	}
	for (out.EffectiveEnd-out.EffectiveStart)/step > int64(r.MaxPoints) {
		step *= 2
	}
	if step == int64(s.Settings().Interval)*1000 {
		out.PeakSemantics = "aligned_observed_sum"
	}
	out.Step = step
	up, down := map[int64]map[string]accum{}, map[int64]map[string]accum{}
	coverage := 1.0
	for instance, sid := range ids {
		u, d, cov, unpersisted, e := aligned(ctx, s, q, sid, step, out.EffectiveStart, out.EffectiveEnd, r.Now)
		if e != nil {
			return out, e
		}
		out.Composition = append(out.Composition, instance)
		coverage = math.Min(coverage, cov)
		out.Unpersisted = out.Unpersisted || unpersisted
		for t, a := range u {
			if up[t] == nil {
				up[t] = map[string]accum{}
			}
			up[t][instance] = a
		}
		for t, a := range d {
			if down[t] == nil {
				down[t] = map[string]accum{}
			}
			down[t][instance] = a
		}
		out.Gaps = append(out.Gaps, s.Events(ctx, instance, sid, out.EffectiveStart, out.EffectiveEnd)...)
	}
	sort.Strings(out.Composition)
	for _, dir := range []struct {
		in    map[int64]map[string]accum
		curve *Curve
	}{{up, &out.Up}, {down, &out.Down}} {
		wasNull := true
		for t := out.EffectiveStart / step * step; t < out.EffectiveEnd; t += step {
			cell := dir.in[t]
			total := 0.0
			ok := len(cell) == len(ids)
			for _, a := range cell {
				if a.weight == 0 {
					ok = false
					break
				}
				total += a.sum / float64(a.weight)
			}
			if !ok {
				if len(cell) > 0 {
					out.Missing = true
				}
				if !wasNull {
					dir.curve.Points = append(dir.curve.Points, null(t, "aligned", int(step/1000)))
				}
				wasNull = true
				continue
			}
			v := strconv.FormatFloat(total, 'f', 3, 64)
			dir.curve.Points = append(dir.curve.Points, Point{max64(t, out.EffectiveStart), &v, "aligned_sum", "aligned", int(step / 1000), 0, step > int64(s.Settings().Interval)*1000, 0})
			wasNull = false
		}
		dir.curve.Thinned = len(dir.curve.Points) > r.MaxPoints
		dir.curve.Points = Thin(dir.curve.Points, r.MaxPoints)
	}
	for _, span := range tierSpans(out.EffectiveStart, out.EffectiveEnd, r.Now) {
		if span.a < span.b {
			src := "summary"
			if span.tier == 0 {
				src = "raw"
			}
			out.Segments = append(out.Segments, Segment{span.a, span.b, src, span.tier, false})
		}
	}
	out.Coverage = coverage
	out.Watermark = s.CommitWatermark()
	if len(out.Gaps) > 256 {
		out.Gaps = out.Gaps[:256]
	}
	return out, nil
}
