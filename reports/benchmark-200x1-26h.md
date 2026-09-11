# qbit-history storage benchmark (SIMULATED)

- generated: 2026-09-10T02:26:05Z
- tasks: 200 across 1 instance(s) (200 each)
- interval: 1 s
- simulated span: 26.0 h
- scenarios: steady,zero,ramp,spike,random,counter_only,paused
- SQLite: 3.51.3
- Go: go1.26.8 windows/amd64

Method: a simulated clock advances 1 s per round; every round pushes one sample per torrent plus one global sample per instance into the bounded queue; every 30 s the queue is committed as ingest frames; raw windows and hourly summaries are sealed continuously; retention cleanup runs every 5 simulated minutes. No HTTP is involved (collector parsing cost is measured separately in the collector tests). This is NOT a 7-day wall-clock run.

## Pipeline cost

| metric | value |
|---|---|
| wall time for 26.0 simulated hours | 1m25.92s |
| simulated seconds per wall second | 1089 |
| time in Append (frames) | 24.52s |
| time in sealing (raw + rollups + hourly) | 39.64s |
| CPU-seconds per simulated day at this load (approx.) | 59.2 |
| peak Go heap in use | 5.6 MiB |
| last commit transaction | 7 ms |

## Database after cleanup + TRUNCATE checkpoint

| metric | value |
|---|---|
| main file | 168.11 MiB |
| WAL | 0.00 MiB |
| used pages × page size | 167.59 MiB |
| free (reusable) pages | 0.51 MiB |
| raw payload (tier 0 blocks + frames) | 95.55 MiB for 17366400 samples = **5.77 B/sample** |
| 60 s summaries | 8.84 MiB for 312555 buckets = **29.7 B/bucket** |
| 300 s summaries | 2.41 MiB for 62511 buckets = **40.3 B/bucket** |
| shared (indexes, metadata, page slack) | 60.79 MiB |

## Raw bytes per sample by scenario (retained window)

| scenario | series | B/sample |
|---|---|---|
| counter_only | 28 | 0.27 |
| paused | 28 | 0.27 |
| ramp | 29 | 5.26 |
| random | 28 | 18.94 |
| spike | 29 | 0.41 |
| steady | 29 | 14.67 |
| zero | 29 | 0.27 |

## Query latency (single series, max_points=2000)

| window | metric | latency | points | source |
|---|---|---|---|---|
| 15m | speed | 2.1ms | 900 | raw/0 |
| 15m | cumulative | 2ms | 900 | raw/0 |
| 1h | speed | 4.6ms | 798 | raw/0 |
| 1h | cumulative | 4.2ms | 666 | raw/0 |
| 6h | speed | 15.7ms | 1315 | raw/0 |
| 6h | cumulative | 17.9ms | 666 | raw/0 |
| 24h | speed | 54.4ms | 1319 | raw/0 |
| 24h | cumulative | 59.1ms | 666 | raw/0 |
| 24h overview (1 instances) | speed | 24.9ms | 1351 | step 64s |

## Extrapolated steady-state payload (NOT measured: layered model × measured byte rates)

N = 201 series (torrents + globals), I = 1 s. raw = N×86400/I samples × 5.77 B; 60 s = N×72×60 × 29.7 B; 300 s = N×D×288 × 40.3 B. Add SQLite page/index overhead (measured shared share here: 36% of used pages), WAL (up to 64–128 MiB) and free-page slack.

| retention | payload | payload + 35% overhead | fits 2 GiB budget (75% main-db line = 1.5 GiB)? |
|---|---|---|---|
| 7 days | 136 MiB | 183 MiB | yes |
| 14 days | 151 MiB | 204 MiB | yes |
| 30 days | 187 MiB | 252 MiB | yes |

Caveats: the simulated mix has 57/200 zero-speed series, which compress far better than random traffic; random-only fleets are the upper bound (see per-scenario table). Hourly summary blocks were sealed for 26.0 h only; frames for the most recent minutes stay unsealed as in production.
