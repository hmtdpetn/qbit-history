# Test and capacity report

Executed 2026-09-10 on Windows 11 (amd64), Go 1.26.8 (portable toolchain in
`.tools/`), Node 24.18, embedded SQLite 3.51.3, Vuetify 3.11.8.

Legend: **PASS** executed and passed · **NOT RUN** not executed · **EXTRAPOLATED** derived from measurements, not observed over the real duration.

## 1. Automated tests

`go vet ./...` — clean.
`go test ./... -count=1` — **53/53 PASS** (full log: `reports/go-test.txt`).
`go test ./... -count=1 -race` — **PASS** on every package (no data races).
Statement coverage 67.1 % overall: codec 93.0, rollup 90.4, security 87.2, collector 85.7, upstream 78.3, query 78.0, api 60.6, store 54.8. (`store` is the lowest because its error branches — disk full, corrupt block, failed transaction — are hard to force; those paths are covered by the pause/resume and corruption tests only at the entry point.)

| # (acceptance item) | test | result |
|---|---|---|
| 1 every torrent sampled every round (paused/zero/unchanged incl.) | `collector.TestEveryTorrentSampledEveryRound` | PASS |
| 2 one `sync/maindata` per round regardless of task count; one login | same test (login/sync counters) | PASS |
| 3 same hash on two instances isolated | `collector.TestSameHashDifferentInstancesStayIsolated` | PASS |
| 4 one instance failing/authenticating does not block others; slow requests not stacked | `collector.TestAuthFailureCoolsDown`, `TestSlowUpstreamIsNotStacked` | PASS |
| 5 missing field ≠ 0, empty delta ≠ removal, HTML/truncated/error/bad-JSON never delete or fabricate | `collector.TestFailuresDoNotDeleteOrFabricate` | PASS |
| 6 full sync recovery; incomplete torrents completed via `torrents/info` before sampling | same test (`missingfields` mode) | PASS |
| 7 raw codec round trip (jitter, partial validity, near-MaxInt64, resets, zero/non-zero) | `codec.TestRoundTripShapes` | PASS |
| 8 all-zero runs tiny; zero speed + growing counter preserved | `codec.TestZeroRunIsTiny`, `TestCounterOnlySurvives`, `store.TestAppendSealQuery` | PASS |
| 9 ±5 % oscillation inside 24 h stays lossless, never flattened | `query.TestRawWindowIsLossless` | PASS |
| 10 old summaries keep min/max with original time; isolated peak survives | `rollup.TestBuildWeightsAndExtremes`, `query.TestOldDataFlattensPeaksAndDrift` | PASS |
| 11 up/down peaks at different times both survive thinning | `query.TestThinKeepsExtremesIndependently` | PASS |
| 12 ±5 % flattening; slow drift never merged step by step | `rollup.TestFlatRule`, `query.TestOldDataFlattensPeaksAndDrift` | PASS |
| 13 zero/non-zero mix or gap never flattened / never zero-filled | same two tests | PASS |
| 14 crash during frame→block→summary sealing: no duplicates, no loss | `store.TestAppendSealQuery` (re-seal no-op, late frames dropped), `TestDuplicateFrameAndBlockDedup`, `TestEpochChangeMidWindow` | PASS |
| 15 raw→60 s→300 s idempotent, no average-of-averages | `rollup.TestToFiveMatchesDirectBuildAndIsIdempotent`, `TestMergeDoesNotAverageAverages` | PASS |
| 16 deleting one torrent clears only its generation | `collector.TestExplicitRemovalFlow`, `store.TestTombstoneBarrier` | PASS |
| 17 queued data never revives history; re-add = new generation | same two tests | PASS |
| 18 empty list / restart / bulk vanish → protection, never wipe | `collector.TestRestartProtectionAndBulk`, `TestBulkProtection`, `TestStartupProtectionBlocksDeletion` | PASS |
| 19 30→7 requires confirmation; fine window stays 24 h | `api.TestLoginRateLimitAndCSRF` (409 without header), `store.TestRetentionCleanup` | PASS |
| 20 long zero/summary blocks across query and retention boundaries | `store.TestRetentionCleanup`, `query.TestOldDataFlattensPeaksAndDrift` | PASS |
| 21 budget/page/WAL limits pause and resume with hysteresis; bounded queue | `store.TestSpacePauseAndResume`, `TestQueueBounds` | PASS |
| 22 clock rollback → new epoch; forward jump → retention hold | `store.TestEpochChangeMidWindow`, `TestRetentionCleanup` (clock hold) | PASS |
| 23 first counter value is a baseline; decreases never negative traffic | `rollup.TestCounterResetAndGap`, `query.TestRawWindowIsLossless` | PASS |
| 24 aggregate peaks not summed; missing instance → blank, not zero | `query.TestOverviewAlignmentAndMissing` | PASS |
| 25 partial bucket → estimate flag, no out-of-window extremes | `query.TestOldDataFlattensPeaksAndDrift` | PASS |
| 27 raw/summary/queue-tail boundary without duplicates; real gaps break the line | `query.TestRawWindowIsLossless` | PASS |
| 29 only whitelisted upstream requests | `upstream.TestOnlyWhitelistedEndpoints` (pause/delete/setPreferences/addTrackers/logout all refused before any request), `TestRedirectsAreNotFollowed` (credentials never forwarded), `TestNormalizeRejects` (file/gopher/unix schemes, URL credentials, path traversal, link-local, multicast, cloud metadata by IP and by name — while Docker/private addresses stay allowed), `TestOversizeAndInvalidResponses`, `TestSyncValidation`, `TestMissingFieldIsNotZero`; every collector test asserts `Violations()==0`; also verified live (§3) | PASS |
| writer loop: 30 s commit → seal → rollup → cached statistics | `collector.TestManagerWriterPersistsAndSeals` | PASS |
| stopped / credential-error states keep the last known torrents; removal tombstones and purges | `collector.TestManagerStopsCollectorForDisabledInstance` | PASS |
| credentials: AES-GCM bound to the instance id, random nonces, tamper detection, 0600 key, bcrypt admin password | `security.TestEncryptRoundTripIsBoundToInstance`, `TestKeyFileHandling`, `TestPasswordHashing`, `TestRandomAndDigest` | PASS |
| 30 no anonymous access; login rate limit; CSRF/Origin on writes | `api.TestAuthRequiredEverywhere`, `TestLoginRateLimitAndCSRF` | PASS |
| 31 no password/SID in API responses | `api.TestInstanceLifecycleWithMock` (responses scanned for secrets) | PASS |
| 32 master key separate from ciphertext; empty password keeps credential | `api.TestInstanceLifecycleWithMock` | PASS |
| 26 follow-live off while browsing; stale responses never overwrite | UI request-sequence + `follow` flag; verified by inspection, not automated | NOT RUN |
| 28 list statistics cached; virtual list at 500 items | verified live at 40 items (§3); 500-item run | NOT RUN |
| 33 compose without qB services / socket / public ports | `docker compose config` **and** a real container run (§4): single service, loopback-only publish verified from a non-loopback NIC, `read_only`, `cap_drop [ALL]`, `no-new-privileges`, uid 65532, no `docker.sock`; refuses to render without `QB_NETWORK_1`/`QB_NETWORK_2` | PASS |
| 34 390/768/1440 light/dark screenshots | `web/tests/screenshots.mjs` → `docs/screenshots/` (42 images, 0 console/page errors) | PASS |

### Defects found by running the tests and fixed

1. **`internal/store/store.go`** — a literal BOM inside a Go string made the package fail to compile (`invalid BOM in the middle of the file`); replaced by an explicit `[]byte{0xEF,0xBB,0xBF}`.
2. **`cmd/benchmark`** — `go vet` caught a `%d` verb with no argument.
3. **`web/src/main.ts`** — Vuetify components and directives were never registered, so every `<v-text-field>`/`<v-select>` rendered as an empty custom element (the login form had **no input fields at all**). Found by the first screenshot run. Fixed by registering `vuetify/components` + `vuetify/directives`; the Vuetify chunk grew 50 kB → 530 kB, confirming the components had genuinely been absent.
4. **`internal/api/api.go`** — the login rate limiter counted *successful* logins, so a user opening several tabs locked themselves out for 15 minutes. Now only failures count and a success clears them; regression test added to `TestLoginRateLimitAndCSRF`.
5. UI polish found in the screenshots: Chinese card headings wrapped character-by-character on narrow screens; an all-zero series produced three identical `0 B/s` axis labels; `word-break: break-all` split units (`Gi B`) and digit groups; the coverage suffix crowded the list cells.

## 2. Capacity benchmarks (simulated clock, `cmd/benchmark`)

Scenario mix: steady, zero, ramp, spike, random, counter_only, paused (equal shares), 1 s interval, 26 simulated hours so that all three tiers and the 24 h raw expiry are exercised.

| | 200 tasks / 1 instance | 400 / 2 | 500 / 2 |
|---|---|---|---|
| wall time for 26 simulated h | 1 m 31 s | 3 m 25 s | 4 m 39 s |
| simulated seconds per wall second | 1027 | 458 | 336 |
| **CPU-seconds per simulated day** | **59** | **155** | **213** |
| peak Go heap in use | 5.6 MiB | 7.0 MiB | 7.3 MiB |
| longest commit transaction | 7 ms | 26 ms | 42 ms |
| main DB after cleanup (26 h) | 168 MiB | 345 MiB | 438 MiB |
| **raw bytes / sample** | **5.77** | **5.77** | **5.81** |
| 60 s summary bytes / bucket | 29.7 | 29.6 | 29.9 |
| 300 s summary bytes / bucket | 40.3 | 40.3 | 40.5 |
| shared (indexes, metadata, slack) | 36 % of used pages | 36 % | 36 % |
| 24 h single-series query | 54 ms | 58 ms | 58 ms |
| 24 h overview query | 25 ms | 47 ms | 53 ms |

Raw bytes per sample by traffic shape (measured, 26 h retained window):

| shape | B/sample |
|---|---|
| zero / paused / counter_only | 0.27 |
| spike (constant + isolated peak) | 0.41 |
| ramp | 5.3 |
| steady (±5 % sine) | 14.7 |
| random | 18.9 |

**EXTRAPOLATED** steady-state payload (layered model × measured byte rates, +35 % SQLite overhead; excludes WAL and free-page slack):

| retention | 200 tasks | 400 tasks | 500 tasks |
|---|---|---|---|
| 7 days | 183 MiB | 366 MiB | 461 MiB |
| 14 days | 204 MiB | 408 MiB | 514 MiB |
| 30 days | 252 MiB | 505 MiB | 634 MiB |

All combinations stay below the 2 GiB budget's 1.5 GiB main-DB line, and 500 tasks / 30 days also fits the 1 GiB mode. **Caveat:** the mix contains 28 % zero-speed series which compress far better than real traffic. A fleet of only random-varying torrents would be ~3.3× the raw payload (18.9 vs 5.8 B/sample), i.e. roughly 1.4 GiB payload at 500 tasks / 30 days — near the budget line, where the app would start pausing writes rather than silently reducing precision. The UI forecast recalibrates from the actual measured rates once a day of data exists.

### Resource limits

`mem_limit: 256m` / `cpus: 0.25` in the compose file are **plausible but not yet validated on the target host**:

* The benchmark's 209 CPU-seconds/simulated-day at 500 tasks is 0.24 % of one core, but it excludes HTTP and JSON parsing, which the collector does once per second per instance.
* Live measurement of the running server (40 torrents, 1 Hz, serving the UI): **27.8 MiB working set**, 1.6 CPU-seconds over ~15 minutes (Windows; Linux/distroless RSS is usually lower). The peak-heap figures above are sampled every 5 simulated minutes and may miss short spikes.
* Confirm with `docker stats --no-stream qbit-history` after a day at the real task count; raise to 384m if OOM-killed.

## 3. Live end-to-end run (real HTTP, simulated qB)

`cmd/mockqb` with 40 torrents + `cmd/history` on 127.0.0.1:28637, driven through the real API.

| check | observed |
|---|---|
| sampling rate | 31 rounds → 1240 logical samples (31 × 40), actual interval 1001 ms, 0 skipped, 10-minute coverage 100 % |
| request scaling | 40 torrents produced **1 `sync/maindata` per round**, not per torrent |
| upstream whitelist | 1025 requests total over the session: 1015 `sync/maindata`, 3 `auth/login`, 4 `app/version`, 3 `app/webapiVersion`. **0 violations** — no torrent control, config or tracker endpoint was ever called |
| secrets | `/status` and `/instances` responses contain no password, SID or tracker URL |
| restart | server restarted twice; credentials decrypted from the master key, collectors resumed, coverage returned to 100 % |
| explicit deletion | removing 1 torrent → immediately a *candidate* (list still 40, `deletion_candidates: 1`); confirmed and cleaned ~23 s later with a `torrent_removed` event |
| bulk protection | removing 15 of 39 at once → `bulk_delete_protection: true`, 15 candidates, **history kept at 39** until the user confirmed; after `confirm-bulk-removal` the list dropped to 24 |
| counter-only traffic | zero-speed torrents still accumulate effective upload (3.52 MiB/24 h shown in the list) |
| UI | 42 screenshots at 390/768/1440 px in light and dark, **0 page errors and 0 console errors** |

## 4. Containerised run (Docker 28.5.1, real image, production topology)

`docker compose build` → image **~14 MiB** (11.6 MiB static binary + 1.9 MiB UI
assets on `distroless/static:nonroot`). Then the delivered `docker-compose.yml`
was started against an external network carrying **two** mock qB containers
named `qb-a-qbittorrent-1` and `qb-b-qbittorrent-1`, both listening on the
same internal port 8080 — exactly the topology the connection help text
describes.

| check | observed |
|---|---|
| container state | `Up (healthy)` — the healthcheck (`/app/history -healthcheck`) passes |
| security | `user=65532:65532`, `ReadonlyRootfs=true`, `CapDrop=[ALL]`, `no-new-privileges:true`, `PidsLimit=128`, `Memory=256m`, `NanoCpus=0.25` |
| mounts | only `./data → /data` (rw) and `./secrets/master.key → /run/secrets/master.key` (ro). No Docker socket, no qB paths |
| port | `127.0.0.1:28637->28637/tcp`; a request to the host's non-loopback address (`172.21.80.1:28637`) **fails to connect** |
| DNS by container name | both instances registered as `http://<container-name>:8080` and came online; qB version read back as v5.0.4 |
| **identity isolation** | the two mocks expose the **same 30 hashes**: 30 distinct `qb_key` → **60 distinct torrent ids and 60 distinct series**, 2 instances |
| sampling | 50 rounds each, 30 torrents each, 10-minute coverage 100 % on both |
| **fault isolation** | `docker stop qb-b-qbittorrent-1` → that instance went `offline` with `network_dns_tls_or_timeout` and 5 failed rounds, while the other stayed `online` with **0 failed rounds** |
| overview during the outage | `missing_components: true`, 4 `null` points — and **0 fabricated zeros** |
| outage ≠ deletion | after `docker start`, both instances returned online and the torrent count was still **60** (no tombstones) |
| restart | `docker compose restart` logged a clean `stopped`, resumed both collectors, and kept all 60 torrents and the database |
| persistence | `data/history.sqlite` + `-wal` + `-shm` on the host volume; 10 571 samples, `queue_dropped: 0`, `paused: false` |
| **upstream whitelist** | mock request logs read from inside the network: qb-a 332 `sync/maindata` + 4 login + 8 version, qb-b 195 + 1 + 2 — **`violations: []` on both** |
| **resources** | **10.1 MiB / 256 MiB (3.9 %)**, CPU 0.17 %, 8 PIDs, with 60 torrents across 2 instances at 1 Hz |

The 256 MiB / 0.25 CPU limits therefore have a large margin at 60 torrents;
they still need confirmation at the real task count (see §2).

### Defect found here and fixed

The master key permission check aborted startup with a bare
`master_key_must_be_0600`. On Windows/SMB bind mounts a file always reports mode
0777 and cannot be chmod-ed, so the container entered a restart loop with an
error that said neither which file nor how to fix it. The check itself is
correct and stays; the message now names the file, the observed mode and the
exact `chmod`/`chown` fix, and `HISTORY_ALLOW_INSECURE_KEY_PERMS=1` allows local
testing on such filesystems while logging a prominent warning on every start.
`.env.example` and the compose file default it to `0`.

## 5. Not executed

* No production load test, no test torrents, no repeated failed logins against the real qB.
* No 7- or 30-day wall-clock run; long-horizon capacity figures are extrapolations.
* 500-item virtual-list interaction and the follow-live/stale-response behaviour were reviewed but not automated.
* The container run used two *simulated* qB instances on a throw-away network.
* Only qBittorrent **5.2.3** was exercised against a real instance. 4.1–5.1 are
  supported by protocol (both login styles and the optional `full_update` field
  are handled and unit-tested) but were not run against real servers.

## 6. Reproduce

```bash
cd qbit-history
export GOROOT=$PWD/.tools/go GOMODCACHE=$PWD/.cache/gomod/cache PATH=$GOROOT/bin:$PATH
go vet ./... && go test ./... -count=1 && go test ./... -count=1 -race
go run ./cmd/benchmark -tasks 200 -instances 1 -hours 26
go run ./cmd/benchmark -tasks 400 -instances 2 -hours 26
go run ./cmd/benchmark -tasks 500 -instances 2 -hours 26
cd web && npm ci && npm run build && npx playwright install chromium
sh ../scripts/dev.sh          # mock qB + server; add http://127.0.0.1:18080 (admin/adminadmin) in the UI
HISTORY_PASSWORD=local-demo-password node tests/screenshots.mjs
```

Container topology test (§4), using the throw-away mock network:

```bash
docker build -t qbit-history:2.0.0 .
docker network create qh-test-net
# build a test-only mock image from the same tree, run two of them on that network,
# then point .env at QB_NETWORK_1/QB_NETWORK_2 and: docker compose up -d
# (the local test used one network for both mocks; production uses two, see the audit)
```
