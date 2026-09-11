package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
	"qbit-history/internal/model"
)

//go:embed migrations/*.sql
var migrations embed.FS

const RawCodec = "QHR2"
const RollupCodec = "QHS3"

type Store struct {
	DB, Read    *sql.DB
	Path        string
	WriteMu     sync.Mutex
	mu          sync.RWMutex
	settings    model.Settings
	SQLite      string
	ClockHold   bool
	Paused      bool
	PauseReason string
	LastCommit  int64
	LastTxMS    int64
	usageCache  Usage
	usageAt     int64
}

func Open(path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, e := sql.Open("sqlite", dsn)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Store, error) { db.Close(); return nil, e }
	var version string
	if e = db.QueryRow("SELECT sqlite_version()").Scan(&version); e != nil {
		return fail(e)
	}
	var ma, mi, pa int
	fmt.Sscanf(version, "%d.%d.%d", &ma, &mi, &pa)
	if ma < 3 || ma == 3 && (mi < 51 || mi == 51 && pa < 3) {
		return fail(errors.New("embedded SQLite " + version + " lacks the WAL-reset fix (need >= 3.51.3)"))
	}
	var tables int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table'").Scan(&tables)
	if tables == 0 {
		// auto_vacuum only takes effect before the first table is created.
		if _, e = db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); e != nil {
			return fail(e)
		}
	}
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=NORMAL", "PRAGMA wal_autocheckpoint=0", "PRAGMA temp_store=MEMORY", "PRAGMA cache_size=-8192"} {
		if _, e = db.Exec(q); e != nil {
			return fail(e)
		}
	}
	b, _ := migrations.ReadFile("migrations/001.sql")
	if _, e = db.Exec(strings.TrimPrefix(string(b), string([]byte{0xEF, 0xBB, 0xBF}))); e != nil {
		return fail(e)
	}
	var av int
	db.QueryRow("PRAGMA auto_vacuum").Scan(&av)
	if av != 2 {
		return fail(errors.New("existing database needs offline incremental-vacuum migration"))
	}
	for k, v := range map[string]string{"codec:raw": RawCodec, "codec:rollup": RollupCodec} {
		var have string
		if db.QueryRow("SELECT value FROM maintenance_state WHERE key=?", k).Scan(&have) == nil {
			if have != v {
				return fail(fmt.Errorf("database %s=%s but binary expects %s; use a fresh data directory", k, have, v))
			}
		} else if _, e = db.Exec("INSERT INTO maintenance_state VALUES(?,?)", k, v); e != nil {
			return fail(e)
		}
	}
	rd, e := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if e != nil {
		return fail(e)
	}
	rd.SetMaxOpenConns(3)
	s := &Store{DB: db, Read: rd, Path: path, settings: model.DefaultSettings(), SQLite: version}
	var data []byte
	if db.QueryRow("SELECT data FROM settings WHERE id=1").Scan(&data) == nil {
		if e = json.Unmarshal(data, &s.settings); e != nil {
			return fail(e)
		}
	}
	s.setPageLimit()
	os.Chmod(path, 0600)
	return s, nil
}
func (s *Store) Close() error             { s.Read.Close(); return s.DB.Close() }
func (s *Store) Settings() model.Settings { s.mu.RLock(); defer s.mu.RUnlock(); return s.settings }
func (s *Store) setPageLimit() {
	var page int64
	s.DB.QueryRow("PRAGMA page_size").Scan(&page)
	if page > 0 {
		s.DB.Exec("PRAGMA max_page_count=" + strconv.FormatInt(s.settings.Budget*75/100/page, 10))
	}
}
func ValidateSettings(v model.Settings) error {
	if (v.RetentionDays != 7 && v.RetentionDays != 14 && v.RetentionDays != 30) || (v.Interval != 1 && v.Interval != 2 && v.Interval != 5) || (v.Budget != 1<<30 && v.Budget != 2<<30) || v.TimeoutMS < 500 || v.TimeoutMS > 10000 || v.ResponseBytes < 1<<20 || v.ResponseBytes > 64<<20 {
		return errors.New("invalid_settings")
	}
	return nil
}
func (s *Store) SaveSettings(v model.Settings, confirmed bool) error {
	if e := ValidateSettings(v); e != nil {
		return e
	}
	if v.RetentionDays < s.Settings().RetentionDays && !confirmed {
		return errors.New("retention_confirmation_required")
	}
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	b, _ := json.Marshal(v)
	if _, e := s.DB.Exec("INSERT OR REPLACE INTO settings VALUES(1,?)", b); e != nil {
		return e
	}
	s.mu.Lock()
	s.settings = v
	s.mu.Unlock()
	s.setPageLimit()
	return nil
}
func (s *Store) Instances() ([]model.Instance, error) {
	rows, e := s.Read.Query("SELECT id,name,base_url,username,secret,enabled FROM instance WHERE base_url!='' ORDER BY name,id")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Instance{}
	for rows.Next() {
		var i model.Instance
		if e = rows.Scan(&i.ID, &i.Name, &i.BaseURL, &i.Username, &i.Secret, &i.PollEnabled); e != nil {
			return nil, e
		}
		i.CredentialsSaved = len(i.Secret) > 0
		out = append(out, i)
	}
	return out, rows.Err()
}
func (s *Store) SaveInstance(i model.Instance) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	_, e := s.DB.Exec("INSERT INTO instance VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,base_url=excluded.base_url,username=excluded.username,secret=excluded.secret,enabled=excluded.enabled", i.ID, i.Name, i.BaseURL, i.Username, i.Secret, i.PollEnabled)
	return e
}
func (s *Store) GlobalSeries(id string) (int64, error) {
	var sid int64
	if s.Read.QueryRow("SELECT id FROM series WHERE instance_id=? AND torrent_id=0", id).Scan(&sid) == nil {
		return sid, nil
	}
	var n int
	if e := s.Read.QueryRow("SELECT count(*) FROM instance WHERE id=? AND base_url!=''", id).Scan(&n); e != nil || n == 0 {
		return 0, sql.ErrNoRows
	}
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	if _, e := s.DB.Exec("INSERT OR IGNORE INTO series(instance_id,torrent_id) VALUES(?,0)", id); e != nil {
		return 0, e
	}
	e := s.DB.QueryRow("SELECT id FROM series WHERE instance_id=? AND torrent_id=0", id).Scan(&sid)
	return sid, e
}

// EnsureTorrent returns the live generation for a key or creates the next one.
func (s *Store) EnsureTorrent(id, key string) (model.Torrent, error) {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	var t model.Torrent
	t.InstanceID = id
	t.Key = key
	tx, e := s.DB.Begin()
	if e != nil {
		return t, e
	}
	defer tx.Rollback()
	var blob []byte
	e = tx.QueryRow("SELECT id,generation,snapshot FROM torrent WHERE instance_id=? AND qb_key=? AND deleted=0 ORDER BY generation DESC LIMIT 1", id, key).Scan(&t.ID, &t.Generation, &blob)
	if e == sql.ErrNoRows {
		e = tx.QueryRow("SELECT COALESCE(MAX(generation),0)+1 FROM torrent WHERE instance_id=? AND qb_key=?", id, key).Scan(&t.Generation)
		if e != nil {
			return t, e
		}
		res, e := tx.Exec("INSERT INTO torrent(instance_id,qb_key,generation,first_seen_ms) VALUES(?,?,?,?)", id, key, t.Generation, time.Now().UnixMilli())
		if e != nil {
			return t, e
		}
		t.ID, _ = res.LastInsertId()
		t.FirstSeen = time.Now().UnixMilli()
	} else if e != nil {
		return t, e
	} else if len(blob) > 0 {
		if e = json.Unmarshal(blob, &t); e != nil {
			return t, e
		}
	}
	if _, e = tx.Exec("INSERT OR IGNORE INTO series(instance_id,torrent_id) VALUES(?,?)", id, t.ID); e != nil {
		return t, e
	}
	e = tx.QueryRow("SELECT id FROM series WHERE instance_id=? AND torrent_id=?", id, t.ID).Scan(&t.SeriesID)
	if e != nil {
		return t, e
	}
	return t, tx.Commit()
}
func (s *Store) Torrents() ([]model.Torrent, error) {
	rows, e := s.Read.Query("SELECT t.id,t.instance_id,t.qb_key,t.generation,t.first_seen_ms,s.id,t.snapshot FROM torrent t JOIN series s ON s.torrent_id=t.id WHERE t.deleted=0")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []model.Torrent{}
	for rows.Next() {
		var t model.Torrent
		var b []byte
		var id, sid int64
		var iid, key string
		var gen int
		var first int64
		if e = rows.Scan(&id, &iid, &key, &gen, &first, &sid, &b); e != nil {
			return nil, e
		}
		if len(b) > 0 {
			json.Unmarshal(b, &t)
		}
		t.ID, t.InstanceID, t.Key, t.Generation, t.SeriesID, t.FirstSeen = id, iid, key, gen, sid, first
		out = append(out, t)
	}
	return out, rows.Err()
}
func (s *Store) Snapshots(ts []model.Torrent) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, t := range ts {
		b, _ := json.Marshal(t)
		if _, e = tx.Exec("UPDATE torrent SET snapshot=? WHERE id=? AND deleted=0", b, t.ID); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Store) AddEvent(ev model.Event) {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	s.DB.Exec("INSERT INTO event(instance_id,series_id,at_ms,end_ms,kind,detail) VALUES(?,?,?,?,?,?)", ev.InstanceID, ev.SeriesID, ev.At, ev.End, ev.Kind, ev.Detail)
}
func (s *Store) Events(ctx context.Context, id string, series int64, start, end int64) []model.Event {
	rows, e := s.Read.QueryContext(ctx, "SELECT instance_id,series_id,at_ms,end_ms,kind,detail FROM event WHERE (?='' OR instance_id=?) AND (?=0 OR series_id=0 OR series_id=?) AND at_ms<? AND end_ms>=? ORDER BY at_ms DESC LIMIT 200", id, id, series, series, end, start)
	if e != nil {
		return nil
	}
	defer rows.Close()
	out := []model.Event{}
	for rows.Next() {
		var x model.Event
		rows.Scan(&x.InstanceID, &x.SeriesID, &x.At, &x.End, &x.Kind, &x.Detail)
		out = append(out, x)
	}
	return out
}

// Barrier marks a series as removed: late samples, sealing and rollups are
// refused from now on, and Cleanup deletes the payload in small batches.
func (s *Store) Barrier(series int64) error {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec("INSERT OR IGNORE INTO tombstone(series_id,at_ms) VALUES(?,?)", series, time.Now().UnixMilli()); e != nil {
		return e
	}
	if _, e = tx.Exec("UPDATE torrent SET deleted=1,snapshot=NULL WHERE id=(SELECT torrent_id FROM series WHERE id=?)", series); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) RemoveInstance(id string) error {
	rows, e := s.Read.Query("SELECT id FROM series WHERE instance_id=?", id)
	if e != nil {
		return e
	}
	var ids []int64
	for rows.Next() {
		var sid int64
		rows.Scan(&sid)
		ids = append(ids, sid)
	}
	rows.Close()
	for _, sid := range ids {
		if e = s.Barrier(sid); e != nil {
			return e
		}
	}
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	_, e = s.DB.Exec("UPDATE instance SET enabled=0,name='[removed]',base_url='',username='',secret=x'' WHERE id=?", id)
	if e == nil {
		_, e = s.DB.Exec("DELETE FROM deletion_candidate WHERE instance_id=?", id)
	}
	return e
}
func (s *Store) SaveCandidates(id string, b []byte) {
	s.WriteMu.Lock()
	defer s.WriteMu.Unlock()
	s.DB.Exec("INSERT OR REPLACE INTO deletion_candidate VALUES(?,'*',?)", id, b)
}
func (s *Store) LoadCandidates(id string) []byte {
	var b []byte
	s.Read.QueryRow("SELECT data FROM deletion_candidate WHERE instance_id=? AND qb_key='*'", id).Scan(&b)
	return b
}
