package collector

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"qbit-history/internal/model"
	"qbit-history/internal/security"
	"qbit-history/internal/store"
	"qbit-history/internal/upstream"
)

type worker struct {
	c      *Collector
	cancel context.CancelFunc
	done   chan struct{}
}
type statCache struct {
	one, day string
	coverage float64
	updated  int64
}

// Manager owns one Collector per enabled connection, the shared bounded
// queue, the single database writer loop and cached list statistics.
type Manager struct {
	mu         sync.RWMutex
	workers    map[string]worker
	stopped    map[string]Status          // connections without a running collector
	stored     map[string][]model.Torrent // last persisted torrents for stopped connections
	DB         *store.Store
	Queue      *store.Queue
	Key        []byte
	ctx        context.Context
	stats      map[int64]statCache
	statCursor int
	LastError  string
	Clock      func() time.Time
}

func NewManager(ctx context.Context, s *store.Store, key []byte) *Manager {
	return &Manager{workers: map[string]worker{}, stopped: map[string]Status{}, stored: map[string][]model.Torrent{}, DB: s, Queue: store.NewQueue(), Key: key, ctx: ctx, stats: map[int64]statCache{}, Clock: time.Now}
}

// Reload stops every collector and starts one per enabled connection. Start
// offsets are staggered so instances never fire in the same millisecond.
func (m *Manager) Reload() error {
	m.StopCollectors()
	is, e := m.DB.Instances()
	if e != nil {
		return e
	}
	cfg := m.DB.Settings()
	stored, _ := m.DB.Torrents()
	byInstance := map[string][]model.Torrent{}
	for _, t := range stored {
		byInstance[t.InstanceID] = append(byInstance[t.InstanceID], t)
	}
	m.mu.Lock()
	m.stopped = map[string]Status{}
	m.stored = map[string][]model.Torrent{}
	m.LastError = ""
	m.mu.Unlock()
	for idx, i := range is {
		stoppedState := ""
		var cl *upstream.Client
		if !i.PollEnabled {
			stoppedState = "monitoring_stopped"
		} else if pw, e := security.Decrypt(m.Key, i.ID, i.Secret); e != nil {
			stoppedState = "credential_error"
			m.mu.Lock()
			m.LastError = "有连接凭据无法解密；请重新输入凭据"
			m.mu.Unlock()
		} else if cl, e = upstream.New(i.BaseURL, i.Username, pw, time.Duration(cfg.TimeoutMS)*time.Millisecond, cfg.ResponseBytes); e != nil {
			stoppedState = "invalid_address"
		}
		if stoppedState != "" {
			sid, _ := m.DB.GlobalSeries(i.ID)
			m.mu.Lock()
			m.stopped[i.ID] = Status{Instance: i, State: stoppedState, TargetSeconds: cfg.Interval, GlobalSeries: sid, TorrentCount: len(byInstance[i.ID])}
			m.stored[i.ID] = byInstance[i.ID]
			m.mu.Unlock()
			continue
		}
		c, e := New(i, cl, m.DB, m.Queue)
		if e != nil {
			return e
		}
		c.Clock = m.Clock
		ctx, cancel := context.WithCancel(m.ctx)
		w := worker{c, cancel, make(chan struct{})}
		m.mu.Lock()
		m.workers[i.ID] = w
		m.mu.Unlock()
		go func(w worker, offset time.Duration) { defer close(w.done); w.c.Run(ctx, offset) }(w, time.Duration(idx%10)*91*time.Millisecond)
	}
	return nil
}
func (m *Manager) StopCollectors() {
	m.mu.Lock()
	ws := m.workers
	m.workers = map[string]worker{}
	m.mu.Unlock()
	for _, w := range ws {
		w.cancel()
	}
	for _, w := range ws {
		<-w.done
		w.c.Persist()
	}
}
func (m *Manager) Collector(id string) *Collector {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if w, ok := m.workers[id]; ok {
		return w.c
	}
	return nil
}

// Snapshots returns the status of every connection and the current torrent
// list without touching the database.
func (m *Manager) Snapshots() ([]Status, []model.Torrent) {
	m.mu.RLock()
	ws := make([]worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	out := []Status{}
	ts := []model.Torrent{}
	for id, st := range m.stopped {
		out = append(out, st)
		ts = append(ts, m.stored[id]...)
	}
	m.mu.RUnlock()
	for _, w := range ws {
		st, t := w.c.Snapshot()
		out = append(out, st)
		ts = append(ts, t...)
	}
	m.mu.RLock()
	for j := range ts {
		if c, ok := m.stats[ts[j].ID]; ok {
			one, day := c.one, c.day
			ts[j].Upload1h = &one
			ts[j].Upload24h = &day
			ts[j].StatsCoverage = c.coverage
			ts[j].StatsAt = c.updated
		}
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Instance.Name < out[j].Instance.Name })
	return out, ts
}
func (m *Manager) Torrent(id int64) (model.Torrent, bool) {
	_, ts := m.Snapshots()
	for _, t := range ts {
		if t.ID == id {
			return t, true
		}
	}
	return model.Torrent{}, false
}
func (m *Manager) TorrentByKey(instance, key string) (model.Torrent, bool) {
	_, ts := m.Snapshots()
	for _, t := range ts {
		if t.InstanceID == instance && t.Key == key {
			return t, true
		}
	}
	return model.Torrent{}, false
}

// Confirm approves the bulk-removal cleanup for one instance.
func (m *Manager) Confirm(id string) bool {
	if c := m.Collector(id); c != nil {
		c.Approve()
		return true
	}
	return false
}

// Remove stops the collector, invalidates queued data and marks the
// connection's history for deletion. It never contacts qBittorrent.
func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	w, ok := m.workers[id]
	delete(m.workers, id)
	delete(m.stopped, id)
	delete(m.stored, id)
	m.mu.Unlock()
	if ok {
		w.cancel()
		<-w.done
		_, ts := w.c.Snapshot()
		for _, t := range ts {
			m.Queue.Invalidate(t.SeriesID)
		}
		m.Queue.Invalidate(w.c.globalID)
	}
	now := m.Clock().UnixMilli()
	m.DB.AddEvent(model.Event{InstanceID: id, At: now, End: now, Kind: "composition_change", Detail: "本监控连接已移除；总览组成变化"})
	return m.DB.RemoveInstance(id)
}

// Flush commits queued samples, current snapshots and lifecycle state.
func (m *Manager) Flush() error {
	e := m.Queue.Flush(m.DB)
	if e != nil {
		m.DB.SetWriteError()
		now := m.Clock().UnixMilli()
		m.DB.AddEvent(model.Event{At: now, End: now, Kind: "storage_gap", Detail: "待写批次未能提交，历史留白"})
		return e
	}
	_, ts := m.Snapshots()
	live := ts[:0]
	for _, t := range ts {
		if m.Collector(t.InstanceID) != nil {
			live = append(live, t)
		}
	}
	if e = m.DB.Snapshots(live); e != nil {
		return e
	}
	m.mu.RLock()
	ws := make([]worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.RUnlock()
	for _, w := range ws {
		w.c.Persist()
	}
	return nil
}

// RunWriter is the single maintenance loop: 30 s commits, sealing, space
// checks, periodic cleanup and cached statistics.
func (m *Manager) RunWriter(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	n := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			n++
			m.Step(ctx, n)
		}
	}
}

// Step runs one second of the writer schedule; tests call it directly.
func (m *Manager) Step(ctx context.Context, n int) {
	now := m.Clock().UnixMilli()
	if n%30 == 0 {
		if e := m.Flush(); e != nil {
			m.setError("持久化失败；已启用空间/数据库保护")
		}
	}
	if n%5 == 0 {
		m.DB.CheckSpace()
	}
	if n%300 == 0 || m.DB.PersistStatus() && n%5 == 0 {
		m.DB.Cleanup(now)
		m.DB.Checkpoint(false)
	}
	if !m.DB.PersistStatus() {
		if _, e := m.DB.SealRaw(ctx, now, 8); e != nil {
			m.setError("原始封块失败；保留源 frame，未丢弃数据")
		}
		m.DB.SealSummaries(ctx, now, 8)
	}
	if n%30 == 0 {
		m.DB.Checkpoint(false)
	}
	m.refreshStats(ctx, now, 4)
}
func (m *Manager) setError(s string) { m.mu.Lock(); m.LastError = s; m.mu.Unlock() }

// refreshStats recomputes 1 h / 24 h effective upload for a few torrents per
// second from 300 s summaries, so the list never triggers full raw decoding.
func (m *Manager) refreshStats(ctx context.Context, now int64, limit int) {
	_, ts := m.Snapshots()
	if len(ts) == 0 {
		return
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
	for j := 0; j < limit && j < len(ts); j++ {
		t := ts[m.statCursor%len(ts)]
		m.statCursor++
		m.mu.RLock()
		old := m.stats[t.ID]
		m.mu.RUnlock()
		if now-old.updated < 60000 {
			continue
		}
		bs, e := m.DB.Summaries(ctx, t.SeriesID, 300, now-86400000, now)
		if e != nil {
			continue
		}
		var one, day, duration int64
		for _, b := range bs {
			if b.Start < now-86400000 || b.End > now {
				continue
			}
			day += b.Uploaded.Delta
			duration += b.Uploaded.Duration
			if b.Start >= now-3600000 {
				one += b.Uploaded.Delta
			}
		}
		// The unsealed tail (last ~7 minutes) is added from raw samples of the queue and frames.
		if tail, e := m.DB.Raw(ctx, t.SeriesID, now-900000, now); e == nil {
			tail = append(tail, m.Queue.Tail(t.SeriesID)...)
			tail = store.Dedup(tail)
			var sealedEnd int64
			for _, b := range bs {
				if b.End > sealedEnd {
					sealedEnd = b.End
				}
			}
			for i := 1; i < len(tail); i++ {
				p, s := tail[i-1], tail[i]
				if s.At < sealedEnd || !s.Continuous(p) || p.Valid&s.Valid&model.ValidUploaded == 0 || s.Uploaded < p.Uploaded {
					continue
				}
				day += s.Uploaded - p.Uploaded
				one += s.Uploaded - p.Uploaded
				duration += s.At - p.At
			}
		}
		m.mu.Lock()
		m.stats[t.ID] = statCache{fmt.Sprint(one), fmt.Sprint(day), float64(duration) / 86400000, now}
		m.mu.Unlock()
	}
}
func (m *Manager) Error() string { m.mu.RLock(); defer m.mu.RUnlock(); return m.LastError }
