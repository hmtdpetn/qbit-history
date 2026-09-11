# qbit-history storage benchmark (SIMULATED)

- generated: 2026-09-10T02:31:26Z
- tasks: 500 across 2 instance(s) (250 each)
- interval: 1 s
- simulated span: 26.0 h
- scenarios: steady,zero,ramp,spike,random,counter_only,paused
- SQLite: 3.51.3
- Go: go1.26.8 windows/amd64

Method: a simulated clock advances 1 s per round; every round pushes one sample per torrent plus one global sample per instance into the bounded queue; every 30 s the queue is committed as ingest frames; raw windows and hourly summaries are sealed continuously; retention cleanup runs every 5 simulated minutes. No HTTP is involved (collector parsing cost is measured separately in the collector tests). This is NOT a 7-day wall-clock run.

## Pipeline cost

| metric | value |
|---|---|
| wall time for 26.0 simulated hours | 4m44.606s |
| simulated seconds per wall second | 329 |
| time in Append (frames) | 1m55.529s |
| time in sealing (raw + rollups + hourly) | 1m54.949s |
| CPU-seconds per simulated day at this load (approx.) | 212.7 |
| peak Go heap in use | 7.3 MiB |
| last commit transaction | 36 ms |

## Database after cleanup + TRUNCATE checkpoint

| metric | value |
|---|---|
| main file | 437.84 MiB |
| WAL | 0.00 MiB |
| used pages × page size | 437.18 MiB |
| free (reusable) pages | 0.66 MiB |
| raw payload (tier 0 blocks + frames) | 250.33 MiB for 45144000 samples = **5.81 B/sample** |
| 60 s summaries | 22.20 MiB for 780610 buckets = **29.8 B/bucket** |
| 300 s summaries | 6.04 MiB for 156122 buckets = **40.5 B/bucket** |
| shared (indexes, metadata, page slack) | 158.62 MiB |

## Raw bytes per sample by scenario (retained window)

| scenario | series | B/sample |
|---|---|---|
| counter_only | 70 | 0.27 |
| paused | 70 | 0.27 |
| ramp | 72 | 5.26 |
| random | 72 | 18.94 |
| spike | 72 | 0.41 |
| steady | 72 | 14.67 |
| zero | 72 | 0.27 |

## Query latency (single series, max_points=2000)

| window | metric | latency | points | source |
|---|---|---|---|---|
| 15m | speed | 2.1ms | 900 | raw/0 |
| 15m | cumulative | 2.6ms | 900 | raw/0 |
| 1h | speed | 5.8ms | 798 | raw/0 |
| 1h | cumulative | 4.6ms | 666 | raw/0 |
| 6h | speed | 24.9ms | 1315 | raw/0 |
| 6h | cumulative | 17.9ms | 666 | raw/0 |
| 24h | speed | 58.9ms | 1319 | raw/0 |
| 24h | cumulative | 60.7ms | 666 | raw/0 |
| 24h overview (2 instances) | speed | 52.1ms | 1351 | step 64s |

## Extrapolated steady-state payload (NOT measured: layered model × measured byte rates)

N = 502 series (torrents + globals), I = 1 s. raw = N×86400/I samples × 5.81 B; 60 s = N×72×60 × 29.8 B; 300 s = N×D×288 × 40.5 B. Add SQLite page/index overhead (measured shared share here: 36% of used pages), WAL (up to 64–128 MiB) and free-page slack.

| retention | payload | payload + 35% overhead | fits 2 GiB budget (75% main-db line = 1.5 GiB)? |
|---|---|---|---|
| 7 days | 341 MiB | 461 MiB | yes |
| 14 days | 380 MiB | 514 MiB | yes |
| 30 days | 470 MiB | 634 MiB | yes |

Caveats: the simulated mix has 142/500 zero-speed series, which compress far better than random traffic; random-only fleets are the upper bound (see per-scenario table). Hourly summary blocks were sealed for 26.0 h only; frames for the most recent minutes stay unsealed as in production.
