// Package collector polls one qBittorrent instance per Collector with a single
// in-flight request, merges sync/maindata increments into a cache and emits one
// logical sample per known torrent per round, regardless of activity.
package collector

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"qbit-history/internal/model"
	"qbit-history/internal/store"
	"qbit-history/internal/upstream"
)

type Status struct {
	Instance       model.Instance `json:"instance"`
	State          string         `json:"connection_status"`
	Error          string         `json:"error"`
	QBVersion      string         `json:"qb_version"`
	WebAPIVersion  string         `json:"webapi_version"`
	LastSuccess    int64          `json:"last_success_at"`
	ActualInterval int64          `json:"actual_interval_ms"`
	RequestMS      int64          `json:"request_ms"`
	DelayMS        int64          `json:"delay_ms"`
	TargetSeconds  int            `json:"target_seconds"`
	Rounds         int64          `json:"rounds"`
	Skipped        int64          `json:"skipped"`
	Failures       int64          `json:"failed_rounds"`
	Samples        int64          `json:"logical_samples"`
	TorrentCount   int            `json:"torrent_count"`
	Candidates     int            `json:"deletion_candidates"`
	BulkProtection bool           `json:"bulk_delete_protection"`
	ProtectedUntil int64          `json:"protected_until"`
	CooldownUntil  int64          `json:"cooldown_until"`
	Global         model.Sample   `json:"global"`
	GlobalSeries   int64          `json:"global_series_id"`
	Coverage       float64        `json:"coverage_10m"`
}
type Collector struct {
	mu           sync.RWMutex
	instance     model.Instance
	client       *upstream.Client
	db           *store.Store
	queue        *store.Queue
	cache        map[string]upstream.Patch
	known        map[string]*model.Torrent
	server       upstream.Server
	life         Lifecycle
	status       Status
	rid          int64
	epoch, seq   uint64
	full         bool
	logged       bool
	lastCal      int64
	cooldown     int64
	failures     int
	previousTime time.Time
	nextGap      bool
	globalID     int64
	writable     func() bool
	recent       []bool // success/failure of the last rounds for a coverage figure
	protectOnStart bool // first round (re)arms the 120 s deletion protection on the collector clock
	Clock        func() time.Time
}

func New(i model.Instance, c *upstream.Client, s *store.Store, q *store.Queue) (*Collector, error) {
	sid, e := s.GlobalSeries(i.ID)
	if e != nil {
		return nil, e
	}
	var r [8]byte
	rand.Read(r[:])
	epoch := binary.LittleEndian.Uint64(r[:]) & 0x7fffffffffffffff
	now := time.Now().UnixMilli()
	col := &Collector{instance: i, client: c, db: s, queue: q, cache: map[string]upstream.Patch{}, known: map[string]*model.Torrent{}, life: NewLifecycle(now), epoch: epoch, full: true, nextGap: true, globalID: sid, Clock: time.Now, protectOnStart: true}
	var old Lifecycle
	if b := s.LoadCandidates(i.ID); len(b) > 0 && json.Unmarshal(b, &old) == nil {
		col.life = old
		col.life.Approved = false
		if col.life.Candidates == nil {
			col.life.Candidates = map[string]Candidate{}
		}
	}
	ts, _ := s.Torrents()
	for _, t := range ts {
		if t.InstanceID == i.ID {
			t := t
			t.Candidate = false
			col.known[t.Key] = &t
		}
	}
	col.status = Status{Instance: i, State: "connecting", GlobalSeries: sid, TargetSeconds: s.Settings().Interval}
	col.writable = func() bool { return !s.PersistStatus() }
	return col, nil
}

// Run drives the collector on the configured target interval. A round that
// cannot start before the previous one finished is skipped, never queued.
func (c *Collector) Run(ctx context.Context, offset time.Duration) {
	timer := time.NewTimer(offset)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	c.Tick(ctx, c.Clock())
	step := time.Duration(c.db.Settings().Interval) * time.Second
	ticker := time.NewTicker(step)
	defer ticker.Stop()
	lastEnd := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case target := <-ticker.C:
			if target.Before(lastEnd) {
				c.mu.Lock()
				c.status.Skipped++
				c.seq++
				c.nextGap = true
				c.mu.Unlock()
				continue
			}
			c.mu.Lock()
			c.status.DelayMS = time.Since(target).Milliseconds()
			c.mu.Unlock()
			c.Tick(ctx, c.Clock())
			lastEnd = time.Now()
		}
	}
}
func (c *Collector) fail(now int64, e error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.State == "online" || c.status.State == "connecting" {
		c.db.AddEvent(model.Event{InstanceID: c.instance.ID, SeriesID: 0, At: now, End: now, Kind: "gap", Detail: "上游采集失败：" + e.Error() + "；不延用缓存作为新测量"})
	}
	c.status.State = "offline"
	c.status.Error = e.Error()
	c.status.Failures++
	c.full = true
	c.nextGap = true
	c.epoch++
	c.failures++
	c.life.Protect(now)
	c.track(false)
	if errors.Is(e, upstream.ErrAuth) {
		c.logged = false
		c.cooldown = now + 300000
		c.status.State = "authentication_cooldown"
	} else {
		delay := int64(1<<min(c.failures-1, 5)) * 1000
		c.cooldown = now + delay
	}
	c.status.CooldownUntil = c.cooldown
}
func (c *Collector) track(ok bool) {
	c.recent = append(c.recent, ok)
	if len(c.recent) > 600 {
		c.recent = c.recent[len(c.recent)-600:]
	}
	n := 0
	for _, v := range c.recent {
		if v {
			n++
		}
	}
	c.status.Coverage = float64(n) / float64(len(c.recent))
}
func merge(a, b upstream.Patch) upstream.Patch {
	if b.Name != nil {
		a.Name = b.Name
	}
	if b.State != nil {
		a.State = b.State
	}
	if b.Category != nil {
		a.Category = b.Category
	}
	if b.Tags != nil {
		a.Tags = b.Tags
	}
	if b.Up != nil {
		a.Up = b.Up
	}
	if b.Down != nil {
		a.Down = b.Down
	}
	if b.Uploaded != nil {
		a.Uploaded = b.Uploaded
	}
	if b.Downloaded != nil {
		a.Downloaded = b.Downloaded
	}
	if b.Size != nil {
		a.Size = b.Size
	}
	if b.Added != nil {
		a.Added = b.Added
	}
	if b.Completed != nil {
		a.Completed = b.Completed
	}
	if b.Progress != nil {
		a.Progress = b.Progress
	}
	if b.Ratio != nil {
		a.Ratio = b.Ratio
	}
	if b.Availability != nil {
		a.Availability = b.Availability
	}
	if b.Peers != nil {
		a.Peers = b.Peers
	}
	if b.Seeds != nil {
		a.Seeds = b.Seeds
	}
	return a
}
func complete(p upstream.Patch) bool {
	return p.Up != nil && p.Down != nil && p.Uploaded != nil && p.Downloaded != nil
}
func mergeServer(a, b upstream.Server) upstream.Server {
	if b.Up != nil {
		a.Up = b.Up
	}
	if b.Down != nil {
		a.Down = b.Down
	}
	if b.Uploaded != nil {
		a.Uploaded = b.Uploaded
	}
	if b.Downloaded != nil {
		a.Downloaded = b.Downloaded
	}
	return a
}
func sample(p upstream.Patch, at int64, epoch, seq uint64, step int, quality uint16) model.Sample {
	s := model.Sample{At: at, Epoch: epoch, Seq: seq, StepMS: int64(step) * 1000, Quality: quality}
	for _, x := range []struct {
		v    *int64
		to   *int64
		flag uint8
	}{{p.Up, &s.Up, model.ValidUp}, {p.Down, &s.Down, model.ValidDown}, {p.Uploaded, &s.Uploaded, model.ValidUploaded}, {p.Downloaded, &s.Downloaded, model.ValidDownloaded}} {
		if x.v != nil {
			*x.to = *x.v
			s.Valid |= x.flag
		}
	}
	return s
}

// Tick performs one round: (re)login if needed, one sync/maindata request
// (full every five minutes or when a deletion confirmation is due), optional
// torrents/info to complete missing core fields, then apply.
func (c *Collector) Tick(ctx context.Context, observed time.Time) {
	now := millis(observed)
	c.mu.Lock()
	if c.protectOnStart {
		c.life.Protect(now)
		c.protectOnStart = false
	}
	if now < c.cooldown {
		c.mu.Unlock()
		return
	}
	c.seq++
	rid := c.rid
	if c.full || now-c.lastCal >= 300000 || c.life.ConfirmationDue(now) {
		rid = 0
	}
	logged := c.logged
	c.mu.Unlock()
	started := time.Now()
	if !logged {
		if e := c.client.Login(ctx); e != nil {
			c.fail(now, e)
			return
		}
		if e := c.client.Versions(ctx); e != nil {
			c.fail(now, e)
			return
		}
		c.mu.Lock()
		c.logged = true
		c.mu.Unlock()
	}
	syncData, e := c.client.Sync(ctx, rid)
	if errors.Is(e, upstream.ErrAuth) {
		// Exactly one re-login attempt; a second failure enters the auth cooldown.
		if e = c.client.Login(ctx); e == nil {
			syncData, e = c.client.Sync(ctx, 0)
			rid = 0
		}
	}
	if e != nil {
		c.fail(now, e)
		return
	}
	c.mu.RLock()
	working := map[string]upstream.Patch{}
	if !*syncData.Full {
		for k, v := range c.cache {
			working[k] = v
		}
	}
	c.mu.RUnlock()
	for k, p := range syncData.Torrents {
		working[k] = merge(working[k], p)
	}
	for _, k := range syncData.Removed {
		delete(working, k)
	}
	needsInfo := false
	for _, p := range working {
		if !complete(p) {
			needsInfo = true
			break
		}
	}
	if needsInfo {
		info, err := c.client.Info(ctx)
		if err != nil {
			c.fail(now, err)
			return
		}
		for k, p := range info {
			if _, ok := working[k]; ok {
				working[k] = merge(working[k], p)
			}
		}
	}
	// The sample time is the actual observation time, not the scheduled boundary.
	at := c.Clock().UnixMilli()
	c.apply(working, syncData, at, observed, started, rid)
}
func (c *Collector) apply(working map[string]upstream.Patch, data upstream.Sync, at int64, mono time.Time, started time.Time, rid int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.RequestMS = time.Since(started).Milliseconds()
	c.status.Rounds++
	c.status.QBVersion = c.client.Version
	c.status.WebAPIVersion = c.client.APIVersion
	if !c.previousTime.IsZero() {
		wall := mono.UnixMilli() - c.previousTime.UnixMilli()
		elapsed := mono.Sub(c.previousTime).Milliseconds()
		if wall <= 0 || abs(wall-elapsed) > 120000 {
			c.epoch++
			c.nextGap = true
			c.db.AddEvent(model.Event{InstanceID: c.instance.ID, At: at, End: at, Kind: "clock_change", Detail: "UTC 时钟不连续，已开启新采集 epoch"})
			if wall > 120000 {
				c.db.HoldClock()
			}
		}
	}
	c.previousTime = mono
	c.status.ActualInterval = at - c.status.LastSuccess
	if c.status.LastSuccess == 0 {
		c.status.ActualInterval = 0
	}
	c.status.LastSuccess = at
	c.status.State = "online"
	c.status.Error = ""
	c.status.CooldownUntil = 0
	c.failures = 0
	c.cooldown = 0
	c.rid = *data.RID
	c.cache = working
	c.full = false
	c.track(true)
	if *data.Full {
		c.lastCal = at
		c.server = upstream.Server{}
		known, present := map[string]bool{}, map[string]bool{}
		for k := range c.known {
			known[k] = true
		}
		for k := range working {
			present[k] = true
		}
		c.life.ObserveFull(known, present, at)
	}
	c.server = mergeServer(c.server, data.Server)
	for _, key := range data.Removed {
		if _, ok := c.known[key]; ok {
			c.life.Missing(key, true, at)
		}
	}
	for k := range working {
		delete(c.life.Candidates, k)
	}
	for _, key := range c.life.Ready(at, *data.Full) {
		if t := c.known[key]; t != nil {
			if c.db.Barrier(t.SeriesID) == nil {
				c.queue.Invalidate(t.SeriesID)
				c.db.AddEvent(model.Event{InstanceID: c.instance.ID, SeriesID: t.SeriesID, At: at, End: at, Kind: "torrent_removed", Detail: "确认任务已从 qB 移除；仅清理本应用历史"})
				delete(c.known, key)
				delete(c.life.Candidates, key)
			}
		}
	}
	if len(c.life.Candidates) == 0 {
		c.life.Bulk = false
		c.life.Approved = false
	}
	quality := uint16(0)
	if c.nextGap {
		quality = model.GapBefore
	}
	cfg := c.db.Settings()
	write := c.writable()
	accepted := write
	for key, p := range working {
		if !complete(p) {
			continue
		}
		t := c.known[key]
		if t == nil {
			created, e := c.db.EnsureTorrent(c.instance.ID, key)
			if e != nil {
				accepted = false
				continue
			}
			t = &created
			c.known[key] = t
			c.db.AddEvent(model.Event{InstanceID: c.instance.ID, SeriesID: t.SeriesID, At: at, End: at, Kind: "torrent_added", Detail: "首次观察到任务；首个累计值是基线，不计入本窗口增量"})
		}
		old := *t
		setMetadata(t, p)
		s := sample(p, at, c.epoch, c.seq, cfg.Interval, quality)
		if old.Sample.At > 0 && s.Continuous(old.Sample) && (s.Uploaded < old.Sample.Uploaded || s.Downloaded < old.Sample.Downloaded) {
			s.Quality |= model.CounterReset
		}
		t.Sample = s
		t.Candidate = false
		if s.Up > 0 || old.Sample.At > 0 && s.Continuous(old.Sample) && s.Uploaded > old.Sample.Uploaded {
			t.LastUpload = at
		}
		if old.State != "" && (old.State != t.State || old.Name != t.Name || old.Category != t.Category || old.Tags != t.Tags) {
			c.db.AddEvent(model.Event{InstanceID: c.instance.ID, SeriesID: t.SeriesID, At: at, End: at, Kind: "metadata_change", Detail: "任务状态或元数据变化（不保存原始响应）"})
		}
		if write {
			if !c.queue.Push(model.Batch{SeriesID: t.SeriesID, Samples: []model.Sample{s}}) {
				accepted = false
			}
		}
		c.status.Samples++
	}
	for key := range c.life.Candidates {
		if t := c.known[key]; t != nil {
			t.Candidate = true
		}
	}
	global := sample(upstream.Patch{Up: c.server.Up, Down: c.server.Down, Uploaded: c.server.Uploaded, Downloaded: c.server.Downloaded}, at, c.epoch, c.seq, cfg.Interval, quality)
	c.status.Global = global
	if write {
		if !c.queue.Push(model.Batch{SeriesID: c.globalID, Samples: []model.Sample{global}}) {
			accepted = false
		}
	}
	c.nextGap = !accepted
	c.status.TorrentCount = len(working)
	c.status.Candidates = len(c.life.Candidates)
	c.status.BulkProtection = c.life.Bulk
	c.status.ProtectedUntil = c.life.ProtectedUntil
	c.status.TargetSeconds = cfg.Interval
}
func setMetadata(t *model.Torrent, p upstream.Patch) {
	if p.Name != nil {
		t.Name = limit(*p.Name, 4096)
	}
	if p.Category != nil {
		t.Category = limit(*p.Category, 512)
	}
	if p.Tags != nil {
		t.Tags = limit(*p.Tags, 2048)
	}
	if p.State != nil {
		t.State = limit(*p.State, 80)
	}
	if p.Size != nil {
		t.Size = *p.Size
	}
	if p.Added != nil {
		t.Added = *p.Added
	}
	if p.Completed != nil {
		t.Completed = *p.Completed
	}
	if p.Progress != nil {
		t.Progress = *p.Progress
	}
	if p.Ratio != nil {
		t.Ratio = *p.Ratio
	}
	if p.Availability != nil {
		t.Availability = *p.Availability
	}
	if p.Peers != nil {
		t.Peers = *p.Peers
	}
	if p.Seeds != nil {
		t.Seeds = *p.Seeds
	}
}
func limit(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
func (c *Collector) Snapshot() (Status, []model.Torrent) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ts := make([]model.Torrent, 0, len(c.known))
	for _, t := range c.known {
		ts = append(ts, *t)
	}
	return c.status, ts
}
func (c *Collector) Persist() {
	c.mu.RLock()
	b, _ := json.Marshal(c.life)
	id := c.instance.ID
	c.mu.RUnlock()
	c.db.SaveCandidates(id, b)
}

// Approve lets the user clear the history of torrents that vanished in bulk from this instance only.
func (c *Collector) Approve() {
	c.mu.Lock()
	c.life.Approved = true
	c.cooldown = 0
	c.full = true
	c.mu.Unlock()
}
func (c *Collector) GlobalID() int64 { return c.globalID }
