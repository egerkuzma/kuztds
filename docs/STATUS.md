**English** · [Русский](STATUS.ru.md)

# STATUS — where we are and how to continue

Snapshot as of 2026-06-07. For details: `docs/USAGE.md`, `TODO.md`.

## Done (in `main`, tests green)
- **Phases 1–7**: ipindex (+hot-reload), realip, geo (mmdb/Nop) + detect
  (device/OS/browser/brand + bots), router (rule-based stream selection),
  store/logbuf (ClickHouse + Redis), admin API + embedded SPA, render +
  JSON groups config.
- **Block 1** — per-stream bot toggles + bot_redirect/out_bot/b_header.
- **Block 2** — separation, rotator/evenly/random distribution, chance, trash.
- **Block 3** — CURL redirect+cache, remote_pars (`[REMOTE]`), api_mac.
- **Block 4** — postback `?pb=`, keyword collection (save_keys/keys_se),
  conversion and keyword screens.
- **Block 6** — api client (`cmd/apiclient`) + `?api=` handling in the engine.
- **Block 7** — CSV log export, sources (domains), per-group log cleanup,
  stream reordering ↑/↓.
- **UI (redesign 2026-06-07)**: left sidebar navigation, top bar on the right
  (period, Settings gear, user, log out), dark theme. Collapsible group tree;
  clicking a stream — scroll + highlight (stream focus). Full stream builder.
  Covered by tests (`web_test.go` + `-tags=uitest` for JS).
- **Fix**: country/lang/text filters also work when only `values` is set (no
  `raw`) — `router.go: cfgd()/orJoin()`.
- **Fix (found by e2e tests, 2026-06-07):**
  1. WAP operators: `FlagB` is now a whitelist of specific operators (previously
     it meant "any operator present" — you couldn't target a single one).
     `router.go` + comment in `config.go`.
  2. Custom `ip_list` files of streams are now loaded into `ipindex.Set`
     (`ipListFiles()` in `handler.go`, called from `main.go`) — previously the
     per-stream IP filter silently did nothing if the file wasn't in the standard
     set.

## Tests (coverage as of 2026-06-07)
Run: `go test ./...` (unit) and `go test -tags=integration ./...` (with
CH+Redis). `go vet ./...` — clean. Coverage command:
`go test -tags=integration ./... -cover`.

| Package | Coverage | Note |
|---------|:--:|---|
| internal/fetch | 96.9% | httptest + `now` override for TTL |
| internal/security | 87.8% | |
| internal/ipindex | 83.7% | |
| internal/geo | 82.6% | mmdb test |
| internal/router | 81.9% | + regression country/lang values-only |
| internal/logbuf | 81.6% | |
| internal/detect | 80.5% | |
| internal/config | 80.0% | |
| internal/store | 77.0% | miniredis (Counters/sessions) + CH under `-tags=integration` |
| internal/admin | 74.1% | login/CSRF/groups/lists/keys/password/export + file stores + SPA (web_test.go) |
| internal/server | 73.2% | |
| internal/render | 76.8% | |
| cmd/apiclient | 71.6% | round-trip with a fake TDS (`newClientHandler`) |
| cmd/engine | 67.1% | httptest pipeline + helpers + **e2e_test.go** (23 end-to-end scenarios: all redirect types, all macros, bots, geo, filters, operators, distribution, limits, firewall, separation, schedule, chance, api mode, traffic matrix) |
| cmd/admin | 0% | only the `main()` wiring; the logic is in internal/admin |

Refactor for testability: hot-path handlers were extracted from `main()`
closures into `cmd/engine/handler.go` (`engineDeps.root`) and `cmd/apiclient`
(`newClientHandler`). ClickHouse tests are under the `integration` build tag
(`go test -tags=integration ./internal/store/`), skipped when CH is unavailable.

## Remaining
- **Block 5 — cron** (NOT started): bot IP-list updates, VirusTotal, disk
  monitoring + Telegram, cleanup.
- **1.0.x features** (see `TODO.md`): ASN/organization/timezone (geo), GET
  filter, "Other devices", eval redirect, Telegram conversion notifications,
  versions in OS/browser filters `windows:7;10`, group cloning.

## How to run again (dev)
```bash
cd kuztds
make infra-up                         # ClickHouse + Redis
go run ./cmd/admin -hash 'admin123' > /tmp/admin.hash   # password hash

# engine :8080
KUZTDS_DATA_DIR=../database KUZTDS_GROUPS_FILE=configs/test_groups.json \
KUZTDS_TRUSTED_PROXIES=127.0.0.1/32 KUZTDS_POSTBACK_KEY=pbsecret KUZTDS_API_KEY=apikey123 \
KUZTDS_KEYS_DIR=/tmp/kuztds-keys KUZTDS_GEO_DB=internal/geo/testdata/GeoLite2-City-Test.mmdb \
KUZTDS_REDIS_ADDR=localhost:6379 KUZTDS_CLICKHOUSE_ADDR=localhost:9000 \
KUZTDS_CLICKHOUSE_DB=kuztds KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/engine

# admin :8090 (admin / admin123)
KUZTDS_ADMIN_PASSWORD_HASH="$(cat /tmp/admin.hash)" KUZTDS_ADMIN_PASSWORD_FILE=/tmp/admin.hash \
KUZTDS_ADMIN_COOKIE_SECURE=false KUZTDS_ENGINE_URL=http://localhost:8080 \
KUZTDS_GROUPS_FILE=configs/test_groups.json KUZTDS_DATA_DIR=../database KUZTDS_KEYS_DIR=/tmp/kuztds-keys \
KUZTDS_REDIS_ADDR=localhost:6379 KUZTDS_CLICKHOUSE_ADDR=localhost:9000 \
KUZTDS_CLICKHOUSE_DB=kuztds KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/admin
```
The ClickHouse schema is applied automatically from `migrations/clickhouse/*.sql`.
For an existing DB, apply migrations 002/003 via
`docker exec -i <clickhouse-container> clickhouse-client ... --multiquery < file`.

## Notes
- The admin and the engine must point to the SAME `KUZTDS_GROUPS_FILE`; the
  engine caches groups at startup (after editing the file — restart the engine;
  the admin reads live).
- Group: `id` = identifier and address `/<id>`; `name` — optional display name.
