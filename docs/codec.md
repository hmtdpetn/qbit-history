# Block codec and summary format

This document describes the on-disk encodings used by qbit-history. All formats
are versioned by a 4-byte magic; a database created with a different codec
version is refused at startup (`maintenance_state` keys `codec:raw` and
`codec:rollup`) rather than silently misread.

## Container (`internal/codec.Pack`)

| offset | size | content |
|---|---|---|
| 0 | 4 | magic: `QHR2` raw samples, `QHS3` summary buckets |
| 4 | 1 | container version (1) |
| 5 | 1 | flag: 0 = payload stored as-is, 1 = payload is one Zstandard frame |
| 6 | 4 | row count (LE uint32) |
| 10 | 4 | decoded payload length |
| 14 | 4 | CRC32-IEEE of the decoded payload |
| 18 | … | payload |

Limits enforced on decode: row count ≤ 60 000, decoded size ≤ 16 MiB,
Zstandard decoder memory ≤ 32 MiB, CRC and length must match. A corrupt block
therefore only loses its own window; it is never decoded as zeros.

Zstandard is used at the fastest level (`klauspost/compress` `SpeedFastest`)
with a single shared encoder/decoder guarded by a mutex (compression
concurrency 1). If compression does not shrink the payload, the payload is
stored uncompressed (flag 0), so small blocks never grow.

## Column encoding (`EncodeColumns` / `DecodeColumns`)

Every block is a table of `n` rows × `k` int64 columns, written column by
column. Each column is a sequence of `(uvarint run, varint delta)` pairs:
"the next `run` values each increase by `delta`". Constant columns
(`delta = 0`) and regular time steps (`delta = 1000` ms) collapse to one pair;
arithmetic wrap-around is intentional and round-trips exactly. Decoding
validates that runs never exceed the remaining row count and that no trailing
bytes remain.

## Raw samples (`QHR2`, 10 columns)

`at_ms, epoch, seq, up_Bps, down_Bps, uploaded_bytes, downloaded_bytes,
valid_bits, quality_bits, step_ms`

* `epoch` / `seq`: collection epoch (random per collector start, incremented on
  failure or clock anomaly) and per-round sequence. Two samples are
  *continuous* when `epoch` is equal, `seq` increments by one, time moves
  forward by at most two target steps and no gap/clock flag is set.
* `valid_bits`: 1 = up, 2 = down, 4 = uploaded, 8 = downloaded. A missing
  field is never stored as 0.
* `quality_bits`: 1 = gap before this sample, 2 = counter reset, 4 = clock
  change, 8 = partial.
* Measured sizes, from the 26 h benchmark's retained blocks (`reports/`), not estimates:
  all-zero / paused / counter-only **0.27 B/sample**, constant-with-isolated-spike
  0.41, linear ramp 5.3, ±5 % sine 14.7, uniformly random 18.9. Across the mixed
  fleet the average was **5.77–5.81 B/sample** at 200, 400 and 500 tasks.

`decode(encode(x)) == x` field for field is covered by
`internal/codec/raw_test.go` (shapes: zero, steady, random, near-MaxInt64,
time jitter, counter reset, partial validity).

## Summary buckets (`QHS3`, 51 columns)

Per bucket: `start, end, resolution, epoch, count, valid_duration, quality`,
then for **up** and **down** speed: `sum(value×weight_ms), weight_ms, first,
last, min, max, first_at, last_at, min_at, max_at, zero_count,
positive_count`, then for **uploaded** and **downloaded** counters: `first,
last, delta, duration_ms, first_at, last_at, epoch, known, reset` (+ one spare
column).

* The mean is `sum / weight`, time-weighted over observed support; merging
  buckets adds sums and weights and keeps the earliest first / latest last /
  true min & max with their timestamps — never an average of averages.
* Counter `delta` only accumulates across *continuous* samples with
  non-decreasing counters; a decrease marks `reset` and starts a new segment
  without producing negative or bogus positive traffic.
* 60 s buckets are built from the raw five-minute block at sealing time; 300 s
  buckets are merged from the 60 s buckets (`rollup.ToFive`), which the tests
  prove identical to building 300 s buckets directly.
* One-hour blocks hold ≤ 60 (60 s) or ≤ 12 (300 s) buckets per series, tier and
  epoch. Measured in the 26 h benchmark: **29.7 B per 60 s bucket** and
  **40.3 B per 300 s bucket** once sealed into hourly blocks (an unsealed
  bucket still sitting alone in `rollup_open` costs ~160 B, because it carries
  its own 18-byte header and zstd frame — this is why the forecast only
  calibrates after a full day).

## Storage life cycle

1. Collector samples → bounded queue (32 MiB / 120 s).
2. Every 30 s: `Append` writes one immutable `ingest_frame` per series, epoch
   and five-minute window (INSERT OR IGNORE; frames for windows that already
   have a final block are dropped as late duplicates).
3. Three minutes after a five-minute window ends (30 s flush cadence + 120 s
   queue span): `SealRaw` decodes its frames,
   deduplicates by (epoch, seq), writes one final tier-0 block plus the 60 s and
   300 s buckets into `rollup_open`, and deletes the frames — in **one**
   transaction. Re-running is a no-op.
4. Eight minutes after an hour ends: `SealSummaries` packs the hour's open
   buckets into one `series_block` per tier and deletes the open rows, again in
   one transaction.
5. Every five minutes: `Cleanup` deletes (LIMIT 256 per statement) tombstoned
   payload, raw blocks older than 24 h, 60 s blocks older than 72 h, 300 s
   blocks older than the retention, expired events and sessions, and runs
   `PRAGMA incremental_vacuum(128)`.

Queries read `series_block ∪ ingest_frame` (raw) or `series_block ∪
rollup_open` (summaries) with overlap predicates on `[start_ms, end_ms)` and
deduplicate, so a crash between steps can never double count.
