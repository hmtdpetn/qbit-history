package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"time"

	"qbit-history/internal/codec"
	"qbit-history/internal/model"
	"qbit-history/internal/rollup"
)

const Window = 300000   // five-minute raw window
const HourBlock = 3600000 // one-hour summary block
const SealDelay = 180000  // wait after a window ends: covers the 30 s flush cadence plus the 120 s queue span

// Append commits queued samples as immutable ingest frames (one per series,
// epoch and five-minute window). Samples for windows that already have a final
// block, or for tombstoned series, are dropped: they are late duplicates or
// writes after a delete barrier.
func (s *Store) Append(batches []model.Batch) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	start := time.Now()
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, batch := range batches {
		var dead int
		if tx.QueryRow("SELECT 1 FROM tombstone WHERE series_id=?", batch.SeriesID).Scan(&dead) == nil {
			continue
		}
		groups := map[[2]int64][]model.Sample{}
		for _, p := range batch.Samples {
			k := [2]int64{int64(p.Epoch), p.At / Window * Window}
			groups[k] = append(groups[k], p)
		}
		for k, ps := range groups {
			var sealed int
			if tx.QueryRow("SELECT 1 FROM series_block WHERE series_id=? AND tier=0 AND start_ms=? AND epoch=?", batch.SeriesID, k[1], k[0]).Scan(&sealed) == nil {
				continue
			}
			sort.Slice(ps, func(i, j int) bool { return ps[i].Seq < ps[j].Seq })
			data, e := codec.Encode(ps)
			if e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT OR IGNORE INTO ingest_frame VALUES(?,?,?,?,?,?,?)", batch.SeriesID, k[0], ps[0].Seq, ps[0].At, ps[len(ps)-1].At+1, len(ps), data); e != nil {
				return e
			}
		}
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.mu.Lock()
	s.LastCommit = time.Now().UnixMilli()
	s.LastTxMS = time.Since(start).Milliseconds()
	s.mu.Unlock()
	return nil
}

// Dedup sorts samples by time and removes duplicates by (epoch, seq).
func Dedup(ps []model.Sample) []model.Sample {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].At != ps[j].At {
			return ps[i].At < ps[j].At
		}
		if ps[i].Epoch != ps[j].Epoch {
			return ps[i].Epoch < ps[j].Epoch
		}
		return ps[i].Seq < ps[j].Seq
	})
	seen := map[[2]uint64]bool{}
	out := ps[:0]
	for _, p := range ps {
		k := [2]uint64{p.Epoch, p.Seq}
		if !seen[k] {
			seen[k] = true
			out = append(out, p)
		}
	}
	return out
}

// Raw returns persisted samples in [start, end) from final blocks and frames.
func (s *Store) Raw(ctx context.Context, sid, start, end int64) ([]model.Sample, error) {
	rows, e := s.Read.QueryContext(ctx, "SELECT data FROM series_block WHERE series_id=? AND tier=0 AND start_ms<? AND end_ms>? UNION ALL SELECT data FROM ingest_frame WHERE series_id=? AND start_ms<? AND end_ms>?", sid, end, start, sid, end, start)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Sample{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		ps, e := codec.Decode(b)
		if e != nil {
			// A corrupt block only loses its own window.
			continue
		}
		for _, p := range ps {
			if p.At >= start && p.At < end {
				out = append(out, p)
			}
		}
		if len(out) > 200000 {
			return nil, errors.New("raw_query_limit")
		}
	}
	return Dedup(out), rows.Err()
}
func (s *Store) Summaries(ctx context.Context, sid int64, tier int, start, end int64) ([]rollup.Bucket, error) {
	rows, e := s.Read.QueryContext(ctx, "SELECT data FROM series_block WHERE series_id=? AND tier=? AND start_ms<? AND end_ms>? UNION ALL SELECT data FROM rollup_open WHERE series_id=? AND tier=? AND start_ms<? AND end_ms>?", sid, tier, end, start, sid, tier, end, start)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []rollup.Bucket{}
	for rows.Next() {
		var data []byte
		if e = rows.Scan(&data); e != nil {
			return nil, e
		}
		bs, e := rollup.Decode(data)
		if e != nil {
			continue
		}
		for _, b := range bs {
			if b.Start < end && b.End > start {
				out = append(out, b)
			}
		}
		if len(out) > 20000 {
			return nil, errors.New("summary_query_limit")
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start == out[j].Start {
			return out[i].Epoch < out[j].Epoch
		}
		return out[i].Start < out[j].Start
	})
	// Duplicate buckets (same start/epoch in an open row and a sealed block) collapse to one.
	clean := out[:0]
	for i, b := range out {
		if i > 0 && out[i-1].Start == b.Start && out[i-1].Epoch == b.Epoch {
			continue
		}
		clean = append(clean, b)
	}
	return clean, rows.Err()
}

// SealRaw finalises five-minute windows whose frames are older than the window
// end plus two minutes: one final raw block plus both rollup tiers are committed
// in the same transaction that deletes the source frames.
func (s *Store) SealRaw(ctx context.Context, now int64, limit int) (int, error) {
	rows, e := s.Read.QueryContext(ctx, "SELECT series_id,epoch,(start_ms/300000)*300000 AS window FROM ingest_frame WHERE start_ms<? GROUP BY window,series_id,epoch ORDER BY window,series_id LIMIT ?", now-Window-SealDelay, limit)
	if e != nil {
		return 0, e
	}
	var keys [][3]int64
	for rows.Next() {
		var k [3]int64
		rows.Scan(&k[0], &k[1], &k[2])
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if e = s.sealOne(ctx, k); e != nil {
			return 0, e
		}
	}
	return len(keys), nil
}
func (s *Store) sealOne(ctx context.Context, k [3]int64) error {
	sid, epoch, start := k[0], k[1], k[2]
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var dead int
	if tx.QueryRow("SELECT 1 FROM tombstone WHERE series_id=?", sid).Scan(&dead) == nil {
		return nil
	}
	rows, e := tx.Query("SELECT data FROM ingest_frame WHERE series_id=? AND epoch=? AND start_ms>=? AND start_ms<?", sid, epoch, start, start+Window)
	if e != nil {
		return e
	}
	var ps []model.Sample
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		x, e := codec.Decode(b)
		if e != nil {
			rows.Close()
			return e
		}
		ps = append(ps, x...)
	}
	rows.Close()
	if len(ps) == 0 {
		return nil
	}
	ps = Dedup(ps)
	data, e := codec.Encode(ps)
	if e != nil {
		return e
	}
	var previous *model.Sample
	var prevData []byte
	if tx.QueryRow("SELECT data FROM series_block WHERE series_id=? AND tier=0 AND epoch=? AND start_ms<? ORDER BY start_ms DESC LIMIT 1", sid, epoch, start).Scan(&prevData) == nil {
		p, e := codec.Decode(prevData)
		if e == nil && len(p) > 0 && ps[0].Seq == p[len(p)-1].Seq+1 {
			previous = &p[len(p)-1]
		}
	}
	one := rollup.Build(ps, 60, previous)
	five := rollup.ToFive(one)
	if _, e = tx.Exec("INSERT OR IGNORE INTO series_block VALUES(?,0,?,?,?,?,?)", sid, start, epoch, ps[len(ps)-1].At+1, len(ps), data); e != nil {
		return e
	}
	for _, tier := range []struct {
		n int
		b []rollup.Bucket
	}{{60, one}, {300, five}} {
		for _, b := range tier.b {
			data, e := rollup.Encode([]rollup.Bucket{b})
			if e != nil {
				return e
			}
			if _, e = tx.Exec("INSERT OR IGNORE INTO rollup_open VALUES(?,?,?,?,?,?,?)", sid, tier.n, b.Start, epoch, b.End, 1, data); e != nil {
				return e
			}
		}
	}
	if _, e = tx.Exec("DELETE FROM ingest_frame WHERE series_id=? AND epoch=? AND start_ms>=? AND start_ms<?", sid, epoch, start, start+Window); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT OR REPLACE INTO maintenance_state VALUES(?,?)", "raw:"+strconv.FormatInt(sid, 10)+":"+strconv.FormatInt(epoch, 10), strconv.FormatInt(start+Window, 10)); e != nil {
		return e
	}
	return tx.Commit()
}

// SealSummaries packs completed hours of open rollup buckets into one block per
// series, tier and epoch.
func (s *Store) SealSummaries(ctx context.Context, now int64, limit int) (int, error) {
	rows, e := s.Read.QueryContext(ctx, "SELECT series_id,tier,epoch,(start_ms/3600000)*3600000 AS hour FROM rollup_open WHERE start_ms<? GROUP BY hour,series_id,tier,epoch ORDER BY hour,series_id LIMIT ?", now-HourBlock-Window-SealDelay, limit)
	if e != nil {
		return 0, e
	}
	var keys [][4]int64
	for rows.Next() {
		var k [4]int64
		rows.Scan(&k[0], &k[1], &k[2], &k[3])
		keys = append(keys, k)
	}
	rows.Close()
	for _, k := range keys {
		if e = s.sealSummary(ctx, k); e != nil {
			return 0, e
		}
	}
	return len(keys), nil
}
func (s *Store) sealSummary(ctx context.Context, k [4]int64) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var dead int
	if tx.QueryRow("SELECT 1 FROM tombstone WHERE series_id=?", k[0]).Scan(&dead) == nil {
		return nil
	}
	rows, e := tx.Query("SELECT data FROM rollup_open WHERE series_id=? AND tier=? AND epoch=? AND start_ms>=? AND start_ms<? ORDER BY start_ms", k[0], k[1], k[2], k[3], k[3]+HourBlock)
	if e != nil {
		return e
	}
	var bs []rollup.Bucket
	for rows.Next() {
		var b []byte
		rows.Scan(&b)
		x, e := rollup.Decode(b)
		if e != nil {
			rows.Close()
			return e
		}
		bs = append(bs, x...)
	}
	rows.Close()
	if len(bs) == 0 {
		return nil
	}
	data, e := rollup.Encode(bs)
	if e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT OR IGNORE INTO series_block VALUES(?,?,?,?,?,?,?)", k[0], k[1], k[3], k[2], bs[len(bs)-1].End, len(bs), data); e != nil {
		return e
	}
	if _, e = tx.Exec("DELETE FROM rollup_open WHERE series_id=? AND tier=? AND epoch=? AND start_ms>=? AND start_ms<?", k[0], k[1], k[2], k[3], k[3]+HourBlock); e != nil {
		return e
	}
	return tx.Commit()
}

// Cleanup runs one bounded maintenance round: tombstoned payload, expired
// tiers, expired events and sessions, removed-instance rows, and a small
// incremental vacuum. Every statement is limited so no round holds the write
// lock for long; the caller repeats rounds every few minutes.
func (s *Store) Cleanup(now int64) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	for _, table := range []string{"ingest_frame", "series_block", "rollup_open"} {
		cols := "series_id,epoch,seq"
		if table != "ingest_frame" {
			cols = "series_id,tier,start_ms,epoch"
		}
		if _, e := s.DB.Exec("DELETE FROM " + table + " WHERE (" + cols + ") IN (SELECT " + cols + " FROM " + table + " WHERE series_id IN (SELECT series_id FROM tombstone) LIMIT 256)"); e != nil {
			return e
		}
	}
	s.mu.RLock()
	hold := s.ClockHold
	s.mu.RUnlock()
	if !hold {
		d := int64(s.Settings().RetentionDays) * 86400000
		for _, tier := range []struct {
			n   int
			age int64
		}{{0, 86400000}, {60, 72 * 3600000}, {300, d}} {
			for _, table := range []string{"series_block", "rollup_open"} {
				if _, e := s.DB.Exec("DELETE FROM "+table+" WHERE (series_id,tier,start_ms,epoch) IN (SELECT series_id,tier,start_ms,epoch FROM "+table+" WHERE tier=? AND end_ms<=? LIMIT 256)", tier.n, now-tier.age); e != nil {
					return e
				}
			}
		}
		// Frames that never sealed (e.g. persistent seal errors) still expire with the raw tier.
		s.DB.Exec("DELETE FROM ingest_frame WHERE (series_id,epoch,seq) IN (SELECT series_id,epoch,seq FROM ingest_frame WHERE end_ms<=? LIMIT 256)", now-86400000)
		s.DB.Exec("DELETE FROM event WHERE id IN (SELECT id FROM event WHERE end_ms<? LIMIT 256)", now-d)
		s.DB.Exec("DELETE FROM session WHERE expires_ms<?", now)
	}
	s.DB.Exec("DELETE FROM event WHERE id IN (SELECT id FROM event WHERE series_id IN (SELECT series_id FROM tombstone WHERE cleaned=0) LIMIT 256)")
	s.DB.Exec("UPDATE tombstone SET cleaned=1 WHERE cleaned=0 AND NOT EXISTS(SELECT 1 FROM ingest_frame f WHERE f.series_id=tombstone.series_id) AND NOT EXISTS(SELECT 1 FROM series_block b WHERE b.series_id=tombstone.series_id) AND NOT EXISTS(SELECT 1 FROM rollup_open r WHERE r.series_id=tombstone.series_id)")
	// A removed connection disappears entirely once every series it owned is clean (children first: foreign keys are on).
	removable := "SELECT id FROM instance WHERE base_url='' AND NOT EXISTS(SELECT 1 FROM series s JOIN tombstone t ON t.series_id=s.id WHERE s.instance_id=instance.id AND t.cleaned=0) AND NOT EXISTS(SELECT 1 FROM torrent x WHERE x.instance_id=instance.id AND x.deleted=0)"
	s.DB.Exec("DELETE FROM series WHERE instance_id IN (" + removable + ")")
	s.DB.Exec("DELETE FROM torrent WHERE instance_id IN (" + removable + ")")
	s.DB.Exec("DELETE FROM instance WHERE id IN (" + removable + ")")
	s.DB.Exec("PRAGMA incremental_vacuum(128)")
	return nil
}
func (s *Store) Checkpoint(truncate bool) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	mode := "PASSIVE"
	if truncate {
		mode = "TRUNCATE"
	}
	var busy, log, done int
	return s.DB.QueryRow("PRAGMA wal_checkpoint("+mode+")").Scan(&busy, &log, &done)
}
func (s *Store) CheckBarrier(sid int64) bool {
	var n int
	return s.Read.QueryRow("SELECT 1 FROM tombstone WHERE series_id=?", sid).Scan(&n) != sql.ErrNoRows
}

// Count is a test/diagnostic helper returning the row count of a table.
func (s *Store) Count(table string) int64 {
	var n int64
	s.Read.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
	return n
}
