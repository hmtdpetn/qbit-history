// Command benchmark drives the storage pipeline (queue → 30 s frames → 5 min raw
// blocks → 60 s / 300 s rollups → hourly blocks → retention) with a simulated
// clock and synthetic torrents, then reports bytes per sample/bucket, database
// size, query latency and memory. Results are SIMULATED and, for 7/14/30-day
// figures, EXTRAPOLATED from the measured per-tier byte rates.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"qbit-history/internal/mockqb"
	"qbit-history/internal/model"
	"qbit-history/internal/query"
	"qbit-history/internal/store"
)

func main() {
	tasks := flag.Int("tasks", 200, "total torrents across all instances")
	instances := flag.Int("instances", 1, "number of simulated qB instances")
	hours := flag.Float64("hours", 26, "simulated hours (>=25 exercises raw expiry and all tiers)")
	interval := flag.Int("interval", 1, "sampling interval seconds")
	scen := flag.String("scenarios", "steady,zero,ramp,spike,random,counter_only,paused", "comma separated scenario cycle")
	out := flag.String("out", "", "report path (default reports/benchmark-<tasks>x<instances>-<hours>h.md)")
	keep := flag.String("data", "", "data directory to keep the database (default temp)")
	flag.Parse()
	if *out == "" {
		*out = fmt.Sprintf("reports/benchmark-%dx%d-%.0fh.md", *tasks, *instances, *hours)
	}
	dir := *keep
	if dir == "" {
		dir, _ = os.MkdirTemp("", "qh-bench")
		defer os.RemoveAll(dir)
	}
	st, e := store.Open(filepath.Join(dir, "history.sqlite"))
	if e != nil {
		panic(e)
	}
	defer st.Close()
	cfg := st.Settings()
	cfg.Interval = *interval
	st.SaveSettings(cfg, false)
	q := store.NewQueue()
	scenarios := strings.Split(*scen, ",")
	type series struct {
		id       int64
		key      string
		scenario string
		mock     *mockqb.Server
	}
	var all []series
	var globals []int64
	var mocks []*mockqb.Server
	perInstance := *tasks / *instances
	for i := 0; i < *instances; i++ {
		iid := fmt.Sprintf("bench-%d", i)
		st.SaveInstance(model.Instance{ID: iid, Name: iid, BaseURL: "http://" + iid + ":8080", Username: "u", Secret: []byte{1}, PollEnabled: true})
		gid, _ := st.GlobalSeries(iid)
		globals = append(globals, gid)
		m := mockqb.New()
		m.Populate(perInstance, scenarios...)
		mocks = append(mocks, m)
		for _, k := range m.Keys() {
			t, _ := st.EnsureTorrent(iid, k)
			tt, _ := m.Get(k)
			all = append(all, series{t.SeriesID, k, tt.Scenario, m})
		}
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	seconds := int64(*hours * 3600)
	step := int64(*interval)
	ctx := context.Background()
	var epoch uint64 = 1
	start := time.Now()
	var sealTime, appendTime time.Duration
	var maxHeap uint64
	report := &strings.Builder{}
	fmt.Fprintf(report, "# qbit-history storage benchmark (SIMULATED)\n\n")
	fmt.Fprintf(report, "- generated: %s\n- tasks: %d across %d instance(s) (%d each)\n- interval: %d s\n- simulated span: %.1f h\n- scenarios: %s\n- SQLite: %s\n- Go: %s %s/%s\n\n", time.Now().UTC().Format(time.RFC3339), *tasks, *instances, perInstance, *interval, *hours, *scen, st.SQLite, runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(report, "Method: a simulated clock advances %d s per round; every round pushes", step)
	fmt.Fprintf(report, " one sample per torrent plus one global sample per instance into the bounded queue; every 30 s the queue is committed as ingest frames; raw windows and hourly summaries are sealed continuously; retention cleanup runs every 5 simulated minutes. No HTTP is involved (collector parsing cost is measured separately in the collector tests). This is NOT a 7-day wall-clock run.\n\n")
	lastPrint := time.Now()
	for sec := int64(0); sec < seconds; sec += step {
		now := t0 + sec*1000
		for _, m := range mocks {
			m.Step(float64(step))
		}
		for _, s := range all {
			t, _ := s.mock.Get(s.key)
			q.Push(model.Batch{SeriesID: s.id, Samples: []model.Sample{{At: now, Epoch: epoch, Seq: uint64(sec / step), Up: t.Up, Down: t.Down, Uploaded: t.Uploaded, Downloaded: t.Downloaded, Valid: model.CoreValid, StepMS: step * 1000}}})
		}
		for i, gid := range globals {
			m := mocks[i]
			q.Push(model.Batch{SeriesID: gid, Samples: []model.Sample{{At: now, Epoch: epoch, Seq: uint64(sec / step), Up: m.AlltimeUL % 1000000, Down: 0, Uploaded: m.AlltimeUL, Downloaded: m.AlltimeDL, Valid: model.CoreValid, StepMS: step * 1000}}})
		}
		if sec%30 == 0 {
			a := time.Now()
			if e := q.Flush(st); e != nil {
				panic(e)
			}
			appendTime += time.Since(a)
			a = time.Now()
			for {
				n, e := st.SealRaw(ctx, now, 64)
				if e != nil {
					panic(e)
				}
				if n < 64 {
					break
				}
			}
			for {
				n, e := st.SealSummaries(ctx, now, 64)
				if e != nil {
					panic(e)
				}
				if n < 64 {
					break
				}
			}
			sealTime += time.Since(a)
		}
		if sec%300 == 0 {
			st.Cleanup(now)
			st.Checkpoint(false)
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapInuse > maxHeap {
				maxHeap = ms.HeapInuse
			}
		}
		if time.Since(lastPrint) > 5*time.Second {
			fmt.Fprintf(os.Stderr, "  %.1f%% simulated (%.1f h)  elapsed %s\n", float64(sec)*100/float64(seconds), float64(sec)/3600, time.Since(start).Round(time.Second))
			lastPrint = time.Now()
		}
	}
	q.Flush(st)
	endNow := t0 + seconds*1000
	st.Cleanup(endNow)
	st.Checkpoint(true)
	total := time.Since(start)
	u := st.Usage()
	fmt.Fprintf(report, "## Pipeline cost\n\n| metric | value |\n|---|---|\n")
	fmt.Fprintf(report, "| wall time for %.1f simulated hours | %s |\n", *hours, total.Round(time.Millisecond))
	fmt.Fprintf(report, "| simulated seconds per wall second | %.0f |\n", float64(seconds)/total.Seconds())
	fmt.Fprintf(report, "| time in Append (frames) | %s |\n| time in sealing (raw + rollups + hourly) | %s |\n", appendTime.Round(time.Millisecond), sealTime.Round(time.Millisecond))
	fmt.Fprintf(report, "| CPU-seconds per simulated day at this load (approx.) | %.1f |\n", (appendTime+sealTime).Seconds()*86400/float64(seconds))
	fmt.Fprintf(report, "| peak Go heap in use | %.1f MiB |\n", float64(maxHeap)/1048576)
	fmt.Fprintf(report, "| last commit transaction | %d ms |\n\n", u.LastTxMS)
	fmt.Fprintf(report, "## Database after cleanup + TRUNCATE checkpoint\n\n| metric | value |\n|---|---|\n")
	fmt.Fprintf(report, "| main file | %.2f MiB |\n| WAL | %.2f MiB |\n| used pages × page size | %.2f MiB |\n| free (reusable) pages | %.2f MiB |\n", mib(u.Main), mib(u.WAL), mib(u.UsedPages*u.PageSize), mib(u.FreePages*u.PageSize))
	fmt.Fprintf(report, "| raw payload (tier 0 blocks + frames) | %.2f MiB for %d samples = **%.2f B/sample** |\n", mib(u.PayloadRaw), u.RawSamples, div(u.PayloadRaw, u.RawSamples))
	fmt.Fprintf(report, "| 60 s summaries | %.2f MiB for %d buckets = **%.1f B/bucket** |\n", mib(u.Payload60), u.Buckets60, div(u.Payload60, u.Buckets60))
	fmt.Fprintf(report, "| 300 s summaries | %.2f MiB for %d buckets = **%.1f B/bucket** |\n", mib(u.Payload300), u.Buckets300, div(u.Payload300, u.Buckets300))
	fmt.Fprintf(report, "| shared (indexes, metadata, page slack) | %.2f MiB |\n\n", mib(u.Shared))
	// Per-scenario raw bytes.
	byScenario := map[string][]int64{}
	for _, s := range all {
		byScenario[s.scenario] = append(byScenario[s.scenario], s.id)
	}
	fmt.Fprintf(report, "## Raw bytes per sample by scenario (retained window)\n\n| scenario | series | B/sample |\n|---|---|---|\n")
	names := []string{}
	for k := range byScenario {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		var bytes, samples int64
		for _, id := range byScenario[k] {
			var b, n int64
			st.Read.QueryRow("SELECT COALESCE(sum(length(data)),0),COALESCE(sum(count),0) FROM series_block WHERE tier=0 AND series_id=?", id).Scan(&b, &n)
			bytes += b
			samples += n
		}
		fmt.Fprintf(report, "| %s | %d | %.2f |\n", k, len(byScenario[k]), div(bytes, samples))
	}
	// Queries.
	fmt.Fprintf(report, "\n## Query latency (single series, max_points=2000)\n\n| window | metric | latency | points | source |\n|---|---|---|---|---|\n")
	sample := all[0].id
	for _, w := range []struct {
		name string
		ms   int64
	}{{"15m", 900000}, {"1h", 3600000}, {"6h", 21600000}, {"24h", 86400000}, {"3d", 3 * 86400000}, {"7d", 7 * 86400000}} {
		if w.ms > seconds*1000 && w.ms > 86400000 {
			continue
		}
		for _, metric := range []string{"speed", "cumulative"} {
			a := time.Now()
			res, e := query.Series(ctx, st, q, sample, "bench-0", query.Request{Start: endNow - w.ms, End: endNow, Metric: metric, Counter: "reported", MaxPoints: 2000, Now: endNow})
			if e != nil {
				panic(e)
			}
			src := []string{}
			for _, s := range res.Segments {
				src = append(src, fmt.Sprintf("%s/%d", s.Source, s.Resolution))
			}
			fmt.Fprintf(report, "| %s | %s | %s | %d | %s |\n", w.name, metric, time.Since(a).Round(100*time.Microsecond), len(res.Up.Points)+len(res.Up.Extremes), strings.Join(src, " "))
		}
	}
	a := time.Now()
	ids := map[string]int64{}
	for i, g := range globals {
		ids[fmt.Sprintf("bench-%d", i)] = g
	}
	ov, _ := query.Overview(ctx, st, q, ids, query.Request{Start: endNow - 86400000, End: endNow, Metric: "speed", Counter: "reported", MaxPoints: 2000, Now: endNow})
	fmt.Fprintf(report, "| 24h overview (%d instances) | speed | %s | %d | step %ds |\n", len(ids), time.Since(a).Round(100*time.Microsecond), len(ov.Up.Points), ov.Step/1000)
	// Extrapolation.
	raw, one, five := div(u.PayloadRaw, u.RawSamples), div(u.Payload60, u.Buckets60), div(u.Payload300, u.Buckets300)
	n := int64(*tasks + *instances)
	fmt.Fprintf(report, "\n## Extrapolated steady-state payload (NOT measured: layered model × measured byte rates)\n\n")
	fmt.Fprintf(report, "N = %d series (torrents + globals), I = %d s. raw = N×86400/I samples × %.2f B; 60 s = N×72×60 × %.1f B; 300 s = N×D×288 × %.1f B. Add SQLite page/index overhead (measured shared share here: %.0f%% of used pages), WAL (up to 64–128 MiB) and free-page slack.\n\n| retention | payload | payload + 35%% overhead | fits 2 GiB budget (75%% main-db line = 1.5 GiB)? |\n|---|---|---|---|\n", n, *interval, raw, one, five, 100*float64(u.Shared)/float64(max(1, u.UsedPages*u.PageSize)))
	for _, d := range []int{7, 14, 30} {
		p := float64(n*86400/int64(*interval))*raw + float64(n*72*60)*one + float64(n*int64(d)*288)*five
		fits := "yes"
		if p*1.35 > 1.5*1024*1024*1024 {
			fits = "NO"
		}
		fmt.Fprintf(report, "| %d days | %.0f MiB | %.0f MiB | %s |\n", d, p/1048576, p*1.35/1048576, fits)
	}
	fmt.Fprintf(report, "\nCaveats: the simulated mix has %d/%d zero-speed series, which compress far better than random traffic; random-only fleets are the upper bound (see per-scenario table). Hourly summary blocks were sealed for %.1f h only; frames for the most recent minutes stay unsealed as in production.\n", len(byScenario["zero"])+len(byScenario["paused"]), *tasks, *hours)
	os.MkdirAll(filepath.Dir(*out), 0755)
	if e := os.WriteFile(*out, []byte(report.String()), 0644); e != nil {
		panic(e)
	}
	fmt.Println(report.String())
	fmt.Fprintf(os.Stderr, "report written to %s\n", *out)
}
func mib(b int64) float64 { return float64(b) / 1048576 }
func div(a, b int64) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}
