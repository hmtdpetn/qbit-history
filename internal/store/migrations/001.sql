PRAGMA auto_vacuum=INCREMENTAL;
CREATE TABLE IF NOT EXISTS schema_version(version INTEGER NOT NULL);
INSERT INTO schema_version SELECT 1 WHERE NOT EXISTS(SELECT 1 FROM schema_version);
CREATE TABLE IF NOT EXISTS instance(id TEXT PRIMARY KEY, name TEXT NOT NULL, base_url TEXT NOT NULL, username TEXT NOT NULL, secret BLOB NOT NULL, enabled INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS torrent(id INTEGER PRIMARY KEY AUTOINCREMENT, instance_id TEXT NOT NULL REFERENCES instance(id), qb_key TEXT NOT NULL, generation INTEGER NOT NULL, deleted INTEGER NOT NULL DEFAULT 0, first_seen_ms INTEGER NOT NULL DEFAULT 0, snapshot BLOB, UNIQUE(instance_id,qb_key,generation));
CREATE INDEX IF NOT EXISTS torrent_active ON torrent(instance_id,deleted,qb_key);
CREATE TABLE IF NOT EXISTS series(id INTEGER PRIMARY KEY AUTOINCREMENT, instance_id TEXT NOT NULL REFERENCES instance(id), torrent_id INTEGER NOT NULL DEFAULT 0, UNIQUE(instance_id,torrent_id));
-- 30-second ingest frames: immutable, replaced by a final tier-0 block once the five-minute window is stable.
CREATE TABLE IF NOT EXISTS ingest_frame(series_id INTEGER NOT NULL, epoch INTEGER NOT NULL, seq INTEGER NOT NULL, start_ms INTEGER NOT NULL, end_ms INTEGER NOT NULL, count INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(series_id,epoch,seq)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS frame_range ON ingest_frame(series_id,start_ms,end_ms);
CREATE INDEX IF NOT EXISTS frame_age ON ingest_frame(start_ms);
-- tier 0 = raw five-minute blocks, tier 60 / 300 = one-hour blocks of summary buckets.
CREATE TABLE IF NOT EXISTS series_block(series_id INTEGER NOT NULL, tier INTEGER NOT NULL, start_ms INTEGER NOT NULL, epoch INTEGER NOT NULL, end_ms INTEGER NOT NULL, count INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(series_id,tier,start_ms,epoch)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS block_age ON series_block(tier,end_ms);
CREATE TABLE IF NOT EXISTS rollup_open(series_id INTEGER NOT NULL, tier INTEGER NOT NULL, start_ms INTEGER NOT NULL, epoch INTEGER NOT NULL, end_ms INTEGER NOT NULL, count INTEGER NOT NULL, data BLOB NOT NULL, PRIMARY KEY(series_id,tier,start_ms,epoch)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS rollup_age ON rollup_open(start_ms);
CREATE TABLE IF NOT EXISTS tombstone(series_id INTEGER PRIMARY KEY, at_ms INTEGER NOT NULL, cleaned INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS maintenance_state(key TEXT PRIMARY KEY,value TEXT NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS event(id INTEGER PRIMARY KEY AUTOINCREMENT,instance_id TEXT NOT NULL,series_id INTEGER NOT NULL,at_ms INTEGER NOT NULL,end_ms INTEGER NOT NULL,kind TEXT NOT NULL,detail TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS event_range ON event(instance_id,at_ms);
CREATE TABLE IF NOT EXISTS settings(id INTEGER PRIMARY KEY CHECK(id=1), data BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS local_user(id INTEGER PRIMARY KEY CHECK(id=1),name TEXT NOT NULL,password_hash BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS session(token_hash TEXT PRIMARY KEY,csrf TEXT NOT NULL,expires_ms INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS deletion_candidate(instance_id TEXT NOT NULL,qb_key TEXT NOT NULL,data BLOB NOT NULL,PRIMARY KEY(instance_id,qb_key)) WITHOUT ROWID;
