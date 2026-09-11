# qbit-history

**English** · [简体中文](README.zh-CN.md)

Self-hosted, **read-only** history monitor for one or more qBittorrent
instances. It samples **every** torrent of **every** connected instance once a
second, keeps the last 24 hours lossless, summarises older data without losing
true peaks, and serves a web UI you can leave closed — collection runs in the
container, not in your browser.

It **never changes qBittorrent.** The upstream client can only call five
endpoints, all read-only except the login:

```
POST /api/v2/auth/login     GET /api/v2/app/version    GET /api/v2/app/webapiVersion
GET  /api/v2/sync/maindata  GET /api/v2/torrents/info
```

Anything else — pause, delete, setPreferences, or a redirect to another host —
is refused in the HTTP transport before a request leaves the process, and the
test suite asserts the simulated qBittorrent never saw another path.

![Overview](docs/screenshots/overview-dark-1440.png)

## What you get

* **1-second resolution for 24 hours**, for every torrent, not just the active
  ones. No activity-based sampling, no heat scores.
* **Honest gaps.** A failed request is recorded as a gap and never back-filled
  with the last known value. Coverage is shown as a percentage rather than
  quietly interpolated.
* **Summaries that keep the peaks.** Older data is bucketed to 60 s and 300 s,
  but each bucket keeps the real minimum and maximum *and the timestamps they
  happened at*, plus a time-weighted mean — never an average of averages.
* **Correct counters.** Uploaded/downloaded deltas come from differences of
  qBittorrent's own counters across continuous samples, never from integrating
  speeds. A counter reset starts a new segment instead of inventing traffic.
* **Multiple instances**, isolated by `instance_id + torrent key + generation`,
  so one qB going down or being reinstalled cannot corrupt another's history.
* **A bounded footprint.** ~5.8 bytes per sample in practice; 500 torrents for
  30 days ≈ 634 MiB.

## Quick start

Requires Docker with Compose v2. Nothing else — Go and Node are only needed if
you want to build outside Docker.

```bash
git clone https://github.com/hmtdpetn/qbit-history.git
cd qbit-history
cp .env.example .env

# Create ./data and a 32-byte credential master key in ./secrets:
sh scripts/init-secrets.sh

# Pick the password for this app's own admin account (12-72 characters).
# It is unrelated to your qBittorrent password.
echo 'HISTORY_ADMIN_PASSWORD=choose-a-long-password' >> .env

docker compose up -d --build
```

Open <http://127.0.0.1:28637> and log in as `admin`. Then remove the
`HISTORY_ADMIN_PASSWORD` line from `.env` — it is only read when the account is
created, and leaving it there keeps a password in a file for no reason.

Finally, add your qBittorrent instances under **连接管理 / Connections**.

## Connecting to qBittorrent

The address is resolved **from inside the container**, which is the one thing
that trips people up: `127.0.0.1` there means the history container itself, not
your host and not qBittorrent.

| Where qBittorrent runs | Address to use |
|---|---|
| On the same host, not in Docker | `http://host.docker.internal:8080` |
| Another machine on your LAN | `http://192.168.1.x:8080` |
| A Docker container | its container name + **internal** port, e.g. `http://qbittorrent:8080` — needs the network step below |
| Behind a reverse proxy | the full prefix, e.g. `https://example.com/qb` |

`host.docker.internal` works on Linux too: the Compose file maps it to the host
gateway explicitly.

### Reaching a qBittorrent container by name

A container started by a different Compose project sits on that project's own
bridge network, so this container has to join it. Find the network name:

```bash
docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' qbittorrent
```

Put it in `.env` as `QB_NETWORK_1=...`, then uncomment the `qb1` block at the
bottom of `docker-compose.yml` **and** the `- qb1` line in the service's
`networks:` list. `docker compose up -d` afterwards.

### More than one instance

There is no limit. Repeat the step above with `qb2`, `qb3`, … — one network
entry and one `- qbN` line each — and add each instance in the UI. Instances
that already share a network need only one entry; two entries must never point
at the same network, because Docker refuses to attach one container to the same
network twice.

## Supported qBittorrent versions

WebUI API v2, so **4.1 and newer** in principle. Both login protocols are
handled: `200 "Ok."` with a `SID` cookie up to 5.0, and `204 No Content` with a
`QBT_SID_<port>` cookie from 5.1 on, which is what silently breaks many older
clients against current releases.

Verified against **5.2.3**. Older versions follow the same documented API but
have not been tested against a real instance.

## Configuration

Everything lives in `.env`; see `.env.example` for the annotated list.

| variable | default | meaning |
|---|---|---|
| `HISTORY_BIND` | `127.0.0.1` | set to `0.0.0.0` to reach the UI from your LAN |
| `HISTORY_PORT` | `28637` | host port |
| `HISTORY_DATA_DIR` | `./data` | SQLite database location |
| `HISTORY_MASTER_KEY_FILE` | `./secrets/master.key` | key that encrypts stored qB passwords |
| `HISTORY_SECURE_COOKIE` | `0` | set to `1` **only** when served over HTTPS |
| `HISTORY_MEM_LIMIT` / `HISTORY_CPUS` | `256m` / `1.0` | container limits |

Retention (7/14/30 days), sampling interval (1/2/5 s) and the storage budget
(1 or 2 GiB) are set in the UI, not here.

Back up `secrets/master.key` **separately** from `data/`: without it the saved
qBittorrent passwords cannot be decrypted.

## Security model

* Its own administrator account (bcrypt), unrelated to qBittorrent's.
* HttpOnly, `SameSite=Strict` session cookie; `Origin` and `X-CSRF-Token`
  checked on every write; failed logins rate-limited (successful ones never
  count toward the limit).
* qBittorrent credentials encrypted with AES-GCM using a master key mounted
  read-only from a separate file, bound to the instance id.
* No proxying: the UI cannot ask the server to fetch an arbitrary URL.
  Link-local, multicast and cloud metadata addresses are refused, and DNS
  results are re-checked before dialling.
* Responses and logs never contain credentials or session cookie values.
* The container runs as uid 65532, read-only root filesystem, `cap_drop: ALL`,
  `no-new-privileges`, from a `distroless/static` base with no shell.
* The UI binds to `127.0.0.1` by default, so it is not exposed until you choose
  to expose it.

The container does hold real qBittorrent credentials. Read-only behaviour is
enforced by the endpoint whitelist and its tests, not by qBittorrent-side
permissions — qBittorrent has no read-only account type.

## Design notes

* **Storage tiers**: raw 24 h (lossless columnar delta/RLE + zstd), 60 s buckets
  for 72 h, 300 s buckets to the retention horizon, all physically overlapping;
  capacity forecasts account for the overlap. Format details in
  [`docs/codec.md`](docs/codec.md).
* **Trend display**: for old data, runs of complete buckets whose min/max stay
  within ±5 % of the time-weighted mean are drawn as one horizontal segment
  (≤ 15 min in the 60 s tier, ≤ 60 min in the 300 s tier). This affects
  **drawing only** — stored values and byte counters are never modified, and
  extremes keep their real timestamps and are shown as markers.
* **Deletion**: a torrent qBittorrent reports as removed becomes a candidate and
  is confirmed after one fresh full read ≥ 8 s later; a torrent that merely
  stops appearing needs three confirmations ≥ 30 s apart spanning ≥ 60 s. After
  a start or reconnect nothing is deleted for 120 s. If `max(10, 20 %)` of an
  instance's torrents vanish at once, deletion is held until you confirm it in
  the UI, because a mass disappearance is as likely to be a broken qB as a real
  deletion. Only this application's own history is ever deleted.
* **Space**: the database is capped at 75 % of the budget via `max_page_count`,
  the WAL stops at 128 MiB, and there is a host-free-space guard with hysteresis
  on resume. It never deletes qBittorrent tasks to free space.
* **Bandwidth**: responses over 1400 bytes are gzipped. A 24-hour chart is
  ~24 kB instead of ~418 kB, which matters over an SSH tunnel where one round
  trip can cost a few hundred milliseconds.

## Measurements

From the benchmarks in [`docs/test-report.md`](docs/test-report.md) and
[`reports/`](reports/), on simulated fleets over 26 simulated hours:

| | |
|---|---|
| raw sample | **5.8 bytes** (0.27 B when idle, 18.9 B worst case) |
| 60 s / 300 s summary bucket | 29.7 B / 40.3 B |
| CPU, 500 torrents | **213 CPU-seconds per day** (~0.003 of a core) |
| peak Go heap | 7.3 MiB |
| 24 h single-series query | 58 ms |
| 500 torrents × 30 days | **634 MiB** |
| real container, 60 torrents at 1 Hz | 10.1 MiB RSS, 0.17 % CPU |

## Building and development

The published image is built from source by `docker compose up -d --build`, so
**any architecture Docker supports works**, including arm64 (Raspberry Pi,
Apple Silicon). There is no prebuilt image to be locked to.

To run it outside Docker, against a simulated qBittorrent:

```bash
sh scripts/dev.sh     # builds both binaries and the UI, then runs mock qB + the server
# http://127.0.0.1:28637   admin / local-demo-password
# → Connections → add http://127.0.0.1:18080 with admin / adminadmin
```

By hand:

```bash
go test ./... && go build -o bin/history ./cmd/history && go build -o bin/mockqb ./cmd/mockqb
(cd web && npm ci && npm run build)
./bin/mockqb -listen 127.0.0.1:18080 -torrents 40 &
HISTORY_ADMIN_PASSWORD=local-demo-password ./bin/history -data .local/data -master-key .local/master.key -listen 127.0.0.1:28637
```

The mock exposes `/_mock/requests` so you can verify the endpoint whitelist
yourself, and `/_mock/mode?mode=html|truncated|error|forbidden|hang` to inject
failures. It deliberately mimics qBittorrent 5.2 quirks — the `204` login, the
`QBT_SID_<port>` cookie, and omitting `full_update` on incremental syncs.

`go test ./...` runs **58 tests**; `go vet ./...` and the race detector are
clean.

### Repository layout

```
cmd/history         server binary (also -healthcheck, -version)
cmd/mockqb          simulated qBittorrent WebUI API
cmd/benchmark       storage pipeline benchmark with a simulated clock
internal/upstream   whitelisted qB client (URL normalisation, SSRF guards, redirect refusal)
internal/collector  per-instance state machine, deletion lifecycle, writer loop
internal/codec      block container + columnar codec
internal/rollup     time-weighted summary buckets
internal/store      SQLite schema, frames/blocks, sealing, retention, budget, auth
internal/query      series/overview queries, thinning, flattening
internal/api        HTTP API + static UI
web/                Vue 3 + Vuetify + ECharts UI
```

## API

All under `/api/v1`, cookie session, `X-CSRF-Token` on writes:

`POST auth/login` · `POST auth/logout` · `GET auth/session` · `POST auth/password` ·
`GET/POST instances` · `POST instances/test` · `PATCH/DELETE instances/{id}` ·
`POST instances/{id}/reconnect` · `POST instances/{id}/confirm-history-purge` ·
`POST instances/{id}/confirm-bulk-removal` · `GET instances/{id}/series` ·
`GET instances/{id}/torrent-by-key/{key}` · `GET torrents` · `GET torrents/{id}` ·
`GET torrents/{id}/series` · `GET overview/series` · `GET/PATCH settings` ·
`GET storage/usage` · `GET storage/forecast` · `GET status` · `GET events`.

Series queries take `start`/`end` (UTC ms, half-open), `metric=speed|cumulative`,
`counter=reported|delta` and `max_points`, and return sources, resolutions,
coverage, gaps, extremes and estimate flags.

## Known limitations

* 1-second polling is not packet capture. Sub-second peaks and qBittorrent's own
  refresh rate limit the effective resolution.
* A torrent deleted and re-added between two polls with identical evidence
  cannot be told apart.
* Two different DNS names pointing at the same qBittorrent cannot be detected
  automatically; the UI only warns about identical normalised URLs.
* The UI is currently Chinese only.

## License

MIT — see [LICENSE](LICENSE).
