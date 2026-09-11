package store

import (
	"os"
	"path/filepath"
	"time"
)

type Usage struct {
	Main, WAL, SHM, Directory, UsedPages, FreePages, PageSize                                int64
	PayloadRaw, RawSamples, Payload60, Buckets60, Payload300, Buckets300, Shared, HostFree, Budget int64
	Instances, Torrents                                                                       int
	SQLite                                                                                    string
	Paused                                                                                    bool
	Reason                                                                                    string
	LastCommit, LastTxMS                                                                      int64
	ClockHold                                                                                 bool
	StatsAt                                                                                   int64
	Attributions                                                                              []Attribution
}
type Attribution struct {
	InstanceID string `json:"instance_id"`
	SeriesID   int64  `json:"series_id"`
	Bytes      int64  `json:"payload_bytes"`
}

func fileSize(p string) int64 {
	st, e := os.Stat(p)
	if e != nil {
		return 0
	}
	return st.Size()
}

// quick collects only file sizes and pragmas; it is cheap enough to run every few seconds.
func (s *Store) quick() Usage {
	u := Usage{Main: fileSize(s.Path), WAL: fileSize(s.Path + "-wal"), SHM: fileSize(s.Path + "-shm"), Budget: s.Settings().Budget, SQLite: s.SQLite}
	filepath.WalkDir(filepath.Dir(s.Path), func(p string, d os.DirEntry, e error) error {
		if e == nil && !d.IsDir() {
			u.Directory += fileSize(p)
		}
		return nil
	})
	s.Read.QueryRow("PRAGMA page_size").Scan(&u.PageSize)
	var pages int64
	s.Read.QueryRow("PRAGMA page_count").Scan(&pages)
	s.Read.QueryRow("PRAGMA freelist_count").Scan(&u.FreePages)
	u.UsedPages = pages - u.FreePages
	u.HostFree = freeSpace(filepath.Dir(s.Path))
	s.mu.RLock()
	u.Paused = s.Paused
	u.Reason = s.PauseReason
	u.LastCommit = s.LastCommit
	u.LastTxMS = s.LastTxMS
	u.ClockHold = s.ClockHold
	s.mu.RUnlock()
	return u
}

// Usage adds payload statistics (full scans of the block tables); those are cached for a minute.
func (s *Store) Usage() Usage {
	u := s.quick()
	now := time.Now().UnixMilli()
	s.mu.RLock()
	cached, at := s.usageCache, s.usageAt
	s.mu.RUnlock()
	if now-at < 60000 {
		u.PayloadRaw, u.RawSamples, u.Payload60, u.Buckets60, u.Payload300, u.Buckets300 = cached.PayloadRaw, cached.RawSamples, cached.Payload60, cached.Buckets60, cached.Payload300, cached.Buckets300
		u.Instances, u.Torrents, u.Attributions, u.StatsAt = cached.Instances, cached.Torrents, cached.Attributions, at
	} else {
		s.Read.QueryRow("SELECT COUNT(*) FROM instance WHERE base_url!=''").Scan(&u.Instances)
		s.Read.QueryRow("SELECT COUNT(*) FROM torrent WHERE deleted=0").Scan(&u.Torrents)
		s.Read.QueryRow("SELECT COALESCE(sum(length(data)),0),COALESCE(sum(count),0) FROM (SELECT data,count FROM ingest_frame UNION ALL SELECT data,count FROM series_block WHERE tier=0)").Scan(&u.PayloadRaw, &u.RawSamples)
		for _, x := range []struct {
			tier         int
			bytes, count *int64
		}{{60, &u.Payload60, &u.Buckets60}, {300, &u.Payload300, &u.Buckets300}} {
			s.Read.QueryRow("SELECT COALESCE(sum(length(data)),0),COALESCE(sum(count),0) FROM (SELECT data,count FROM series_block WHERE tier=? UNION ALL SELECT data,count FROM rollup_open WHERE tier=?)", x.tier, x.tier).Scan(x.bytes, x.count)
		}
		rows, e := s.Read.Query("SELECT s.instance_id,x.series_id,sum(x.n) FROM (SELECT series_id,length(data) n FROM ingest_frame UNION ALL SELECT series_id,length(data) n FROM series_block UNION ALL SELECT series_id,length(data) n FROM rollup_open) x JOIN series s ON x.series_id=s.id GROUP BY s.instance_id,x.series_id LIMIT 10000")
		if e == nil {
			u.Attributions = []Attribution{}
			for rows.Next() {
				var a Attribution
				rows.Scan(&a.InstanceID, &a.SeriesID, &a.Bytes)
				u.Attributions = append(u.Attributions, a)
			}
			rows.Close()
		}
		u.StatsAt = now
		s.mu.Lock()
		s.usageCache, s.usageAt = u, now
		s.mu.Unlock()
	}
	u.Shared = u.UsedPages*u.PageSize - u.PayloadRaw - u.Payload60 - u.Payload300
	return u
}

// CheckSpace applies the budget and free-space rules with hysteresis and
// returns whether history persistence is allowed.
func (s *Store) CheckSpace() bool {
	u := s.quick()
	s.mu.Lock()
	defer s.mu.Unlock()
	reason := ""
	walStop := int64(128 << 20)
	if u.Budget <= 1<<30 {
		walStop = 96 << 20
	}
	if u.WAL >= walStop {
		reason = "WAL 达到停止阈值，可能被长读事务阻塞 checkpoint"
	} else if u.Directory+15<<20 >= u.Budget*95/100 {
		reason = "数据目录达到预算安全线"
	} else if u.HostFree >= 0 && u.HostFree < 64<<20 {
		reason = "宿主可用空间不足 64 MiB"
	} else if u.UsedPages*u.PageSize >= u.Budget*74/100 {
		reason = "主库达到预算页上限安全线"
	}
	if reason != "" {
		s.Paused = true
		s.PauseReason = reason
	} else if s.Paused && u.Directory+15<<20 < u.Budget*85/100 && u.WAL < walStop/2 && (u.HostFree < 0 || u.HostFree > 128<<20) && u.UsedPages*u.PageSize < u.Budget*68/100 {
		s.Paused = false
		s.PauseReason = ""
	}
	return !s.Paused
}
func (s *Store) SetWriteError() {
	s.mu.Lock()
	s.Paused = true
	s.PauseReason = "数据库写入失败；历史已暂停，等待维护回收"
	s.mu.Unlock()
}
func (s *Store) HoldClock()    { s.mu.Lock(); s.ClockHold = true; s.mu.Unlock() }
func (s *Store) ConfirmClock() { s.mu.Lock(); s.ClockHold = false; s.mu.Unlock() }

type Forecast struct {
	Days                 int     `json:"days"`
	HealthySamples       int64   `json:"healthy_raw_samples"`
	BytesLow             int64   `json:"bytes_low"`
	BytesHigh            int64   `json:"bytes_high"`
	Confidence           string  `json:"confidence"`
	RawBytesPerSample    float64 `json:"raw_bytes_per_sample"`
	MinuteBytesPerBucket float64 `json:"minute_bytes_per_bucket"`
	FiveBytesPerBucket   float64 `json:"five_bytes_per_bucket"`
	Series               int64   `json:"series"`
	OverBudget           bool    `json:"over_budget"`
	Calibrated           bool    `json:"calibrated"`
}

// Forecast applies the layered capacity model (raw 24 h + 60 s 72 h + 300 s D days)
// using measured bytes per sample/bucket when available. It never extrapolates
// short-term net growth of the database file.
func (s *Store) Forecast() []Forecast {
	u := s.Usage()
	cfg := s.Settings()
	n := int64(u.Torrents + u.Instances)
	if n == 0 {
		n = 200
	}
	// Pre-calibration byte rates are deliberately conservative (measured mixes
	// land near 6 B/sample; a random-traffic-only fleet can reach ~19).
	raw, one, five := 12., 48., 48.
	confidence := "未校准：任务数为实测值，字节率使用偏保守的默认假设，不是实测保证"
	factor := 2.0
	calibrated := false
	if u.RawSamples > 50000 {
		raw = float64(u.PayloadRaw) / float64(u.RawSamples)
		confidence = "低置信度：尚未覆盖完整 24 小时，负载可能变化"
		if u.Buckets60 > 0 {
			one = float64(u.Payload60) / float64(u.Buckets60)
		}
		if u.Buckets300 > 0 {
			five = float64(u.Payload300) / float64(u.Buckets300)
		}
		var oldest int64
		s.Read.QueryRow("SELECT COALESCE(min(start_ms),0) FROM series_block WHERE tier=300").Scan(&oldest)
		if oldest > 0 && time.Now().UnixMilli()-oldest >= 86400000 {
			confidence = "已校准：使用分层实测字节率，仍不是配额保证"
			factor = 1.35
			calibrated = true
		}
	}
	out := []Forecast{}
	for _, d := range []int{7, 14, 30} {
		samples := n * 86400 / int64(cfg.Interval)
		p := float64(samples)*raw + float64(n*72*60)*one + float64(n*int64(d)*288)*five
		low := int64(p*1.15) + 160<<20
		high := int64(p*factor) + 224<<20
		out = append(out, Forecast{d, samples, low, high, confidence, raw, one, five, n, high > cfg.Budget, calibrated})
	}
	return out
}
