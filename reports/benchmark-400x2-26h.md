# qbit-history storage benchmark (SIMULATED)

- generated: 2026-09-10T02:27:44Z
- tasks: 400 across 2 instance(s) (200 each)
- interval: 1 s
- simulated span: 26.0 h
- scenarios: steady,zero,ramp,spike,random,counter_only,paused
- SQLite: 3.51.3
- Go: go1.26.8 windows/amd64

Method: a simulated clock advances 1 s per round; every round pushes one sample per torrent plus one global sample per instance into the bounded queue; every 30 s the queue is committed as ingest frames; raw windows and hourly summaries are sealed continuously; retention cleanup runs every 5 simulated minutes. No HTTP is involved (collector parsing cost is measured separately in the collector tests). This is NOT a 7-day wall-clock run.

## Pipeline cost

| metric | value |
|---|---|
| wall time for 26.0 simulated hours | 3m30.457s |
| simulated seconds per wall second | 445 |
| time in Append (frames) | 1m18.957s |
| time in sealing (raw + rollups + hourly) | 1m28.511s |
| CPU-seconds per simulated day at this load (approx.) | 154.6 |
| peak Go heap in use | 7.0 MiB |
| last commit transaction | 26 ms |

## Database after cleanup + TRUNCATE checkpoint

| metric | value |
|---|---|
| main file | 344.89 MiB |
| WAL | 0.00 MiB |
| used pages × page size | 344.25 MiB |
| free (reusable) pages | 0.65 MiB |
| raw payload (tier 0 blocks + frames) | 196.88 MiB for 35784000 samples = **5.77 B/sample** |
| 60 s summaries | 17.65 MiB for 625110 buckets = **29.6 B/bucket** |
| 300 s summaries | 4.81 MiB for 125022 buckets = **40.3 B/bucket** |
| shared (indexes, metadata, page slack) | 124.91 MiB |

## Raw bytes per sample by scenario (retained window)

| scenario | series | B/sample |
|---|---|---|
| counter_only | 56 | 0.27 |
| paused | 56 | 0.27 |
| ramp | 58 | 5.26 |
| random | 56 | 18.94 |
| spike | 58 | 0.41 |
| steady | 58 | 14.67 |
| zero | 58 | 0.27 |

## Query latency (single series, max_points=2000)

| window | metric | latency | points | source |
|---|---|---|---|---|
| 15m | speed | 2ms | 900 | raw/0 |
| 15m | cumulative | 2.5ms | 900 | raw/0 |
| 1h | speed | 3.5ms | 798 | raw/0 |
| 1h | cumulative | 4.2ms | 666 | raw/0 |
| 6h | speed | 17.5ms | 1315 | raw/0 |
| 6h | cumulative | 16.7ms | 666 | raw/0 |
| 24h | speed | 58.3ms | 1319 | raw/0 |
| 24h | cumulative | 59.5ms | 666 | raw/0 |
| 24h overview (2 instances) | speed | 53.3ms | 1351 | step 64s |

## Extrapolated steady-state payload (NOT measured: layered model × measured byte rates)

N = 402 series (torrents + globals), I = 1 s. raw = N×86400/I samples × 5.77 B; 60 s = N×72×60 × 29.6 B; 300 s = N×D×288 × 40.3 B. Add SQLite page/index overhead (measured shared share here: 36% of used pages), WAL (up to 64–128 MiB) and free-page slack.

| retention | payload | payload + 35% overhead | fits 2 GiB budget (75% main-db line = 1.5 GiB)? |
|---|---|---|---|
| 7 days | 271 MiB | 366 MiB | yes |
| 14 days | 302 MiB | 408 MiB | yes |
| 30 days | 374 MiB | 505 MiB | yes |

Caveats: the simulated mix has 114/400 zero-speed series, which compress far better than random traffic; random-only fleets are the upper bound (see per-scenario table). Hourly summary blocks were sealed for 26.0 h only; frames for the most recent minutes stay unsealed as in production.
