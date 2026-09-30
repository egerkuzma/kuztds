**English** · [Русский](STATUS.ru.md)

# STATUS — where we are and how to continue

Snapshot as of 2026-09-30. For details: `docs/USAGE.md`, `TODO.md`.

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
- **UI (redesigned 2026-09-08; replaced on 2026-09-30, see below)**: GitHub (Primer) style in **light and dark**
  (toggle in the top bar, remembered in the browser, system preference until
  chosen). **Dashboard** answers "what works": KPI tiles incl. conversions /
  profit / CR, a two-hue chart with hover, and a **performance table by group
  and stream** (new `GET /api/stats/performance`: events and postbacks
  aggregated per group/stream in the store, merged in Go). **Groups** shows a
  group as a **waterfall** of streams in matching order — one rule per row
  (switch, condition chips, output, bots, hits / bots / conversions), drag to
  reorder, a "no match → group default" row with its own numbers, group
  settings folded out on demand. The **stream editor** is WHEN → THEN:
  only the configured conditions are shown, the other 19 kinds sit behind
  "Add condition"; bots as toggle chips; the rare parts (chance, separation,
  `[REMOTE]`, CURL, API mac) folded into Advanced. The earlier master–detail
  invariants stay: one pane, both sides scroll internally, no
  `scrollIntoView`, unsaved marker, leave/close guards, `Ctrl`/`Cmd`+`S`.
  Also exposed: `save_keys` / `save_keys_se` in the group settings (the
  previous form did not carry them, so every save reset both to false).
  Tests: `web_test.go` anchors + `-tags=uitest`; the integration test now
  covers `Performance` and `DeleteGroupLogs`.
- **Fix (2026-09-08)**: "Clear logs" deleted by `group_name` while the panel
  sent the group's **ID** — with a display name set, nothing was deleted.
  `DeleteGroupLogs` keys on `group_id` now (`clickhouse.go`).
- **2026-09-30 — geo, cron, flows** (one branch):
  1. **Geo from MaxMind-format databases.** `geo.DB` reads a City/Country file
     (`KUZTDS_GEO_DB`) and an ASN file (`KUZTDS_ASN_DB`) into memory and swaps
     them when the files change; a corrupt replacement never blanks the data.
     New stream conditions: `asn`, `org`, `timezone` (UTC offset or zone name),
     `get` (URL parameter). New macros `[ASN] [ORG] [TIMEZONE] [UTC]`. Events
     carry `asn/org/timezone` (columns added on start; an old schema is
     tolerated). Verified against GeoLite2 test databases and live against
     DB-IP Lite (no time zone and no region ISO codes in that one — the region
     falls back to its name). A lookup decodes only the fields it uses: about
     0.4 µs and 5 allocations for City + ASN (`make bench`), down from 15
     allocations when the localized names were read as maps.
  2. **`CF-IPCountry` is trusted only through a trusted proxy**, and the
     group's `geo` setting now decides which source wins (`cf` = header first,
     otherwise the database first). Before, the header was read from any
     visitor and the setting did nothing.
  3. **Block 5 — the cron service** (`cmd/cron`, `internal/cron`): bot IP lists
     (plain, sectioned, Google/Bing JSON; replace/merge), geo database
     downloads (`.mmdb` / `.gz` / `.tar.gz`, validated before the rename,
     `If-Modified-Since` when there is a file to keep), VirusTotal (alert once
     per domain, optional stream switch-off; an unknown domain is clean, a
     spent quota stops the pass), disk space, keyword cleanup, conversion
     alerts, Telegram. File-based config, status and "run now"; secrets masked
     in the API. Run end to end on the stand against the real Google, Bing and
     DB-IP endpoints. One race was found on the way, by a test that failed
     once in twelve runs: the heartbeat and a finishing job both wrote the
     status file, and the older snapshot could land last — a job then looked
     "running" after it had finished and, across a restart, ran again. The
     writes are ordered now; the regression test fails five times in six
     without the fix.
  4. **Group links**: redirect type `group` hands the visitor to another group
     (≤ 3 hops, broken links answer like an unknown group); the event records
     the forwarding stream (`via`) and `Performance` counts it there too.
  5. **Admin API**: `GET /api/lookup`, `POST /api/simulate` (walks a visitor
     through the groups sent in the request; `router.Why` names the rejecting
     rule), `GET|PUT /api/cron`, `POST /api/cron/run`. CSV export defuses
     spreadsheet formulas.
  6. **The interface, rebuilt** — top navigation (no sidebars), hash routing,
     the flow canvas (trunk, branches, traffic-proportional wires, drag to
     reorder), drawer editors, "Test a visitor", Tools, Automation, one save
     bar with drafts that survive navigation. Streams got a note and a
     Content-Type override, flows can be duplicated, streams moved between
     flows, the uniqueness window is entered in hours. Checked in a real
     browser: a 69-step scenario against the live admin (round trip of every
     stream and flow through its form, conditions, links, reorder, simulate,
     save and read back, automation, theme) and a drag test driven by pointer
     events (10 checks).
  7. `fetch`: `TestConnectionReuse` compared the tuned transport with the
     default one, which depends on scheduling and failed on a loaded CI runner;
     it now asserts the bound the tuned transport guarantees.
  8. Throughput was compared with `main` on one machine under the same load
     (`ab`, 50 keep-alive connections, geo databases loaded, no Redis or
     ClickHouse): `main` about 110k req/s, this branch 118–163k over three
     runs — noisy, but no regression, so the figures in the README stand.
- **2026-09-30 — flows read as words** (admin only; the config format and the
  engine are unchanged):
  1. **Conditions are sentences with chips.** "organization contains amazon,
     google, hetzner +4" instead of `/amazon|google|…/i`; countries carry flags;
     text conditions say `contains`, expressions `matches`. In the editor,
     values are chips (Enter, a comma or a pasted list), not a comma-separated
     string. An expression that is only alternatives is shown and edited as
     words and saved as the same expression, flags included — untouched, byte
     for byte; a real expression is edited as text. The router reads `raw`, so
     that is what decides whether a condition is an expression.
  2. **An expression the engine cannot use is caught.** RE2 compiles no
     lookarounds or back-references, and a pattern that does not compile never
     matches — under `is` that turns every visitor away. The editor names the
     problem while the pattern is typed; the canvas marks the chip.
  3. **Outputs are rows.** Variants (`|||` in the config) are one row each, with
     "which variant a visit gets" shown only when there are several, in words;
     the canvas lists them one per line and highlights macros. The same for
     the bots' output and the flow default. Proxy rewrites (`find|||replace`
     lines) are find → replace rows, shown where they apply (the type is
     "Proxy a page"). Fields a type does not use are hidden and keep their
     values.
  4. Smaller: limits read "50 per hour", schedules "weekdays"; days start on
     Monday; aliases are chips and checked like IDs on save; with a drawer open
     the save bar sits beside it, above the scrim, so a click on Save saves
     instead of closing the drawer.
  5. The helpers that convert stored values and back live in one block of the
     page that `web_logic_test.go` runs under node (`-tags=uitest`): word
     lists ↔ expressions, flags, escaping, variants, rewrites. In the browser
     against the stand: scenario 101/101 (every flow through the editor
     without edits leaves every condition byte-identical), drag 10/10.
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

- **Fix (code review, 2026-08-20):**
  1. **Crash on a hostile rotator cookie.** `ztrot_<group>_<stream>` is written
     by the engine but comes back from the visitor. A negative value (`-5`)
     produced a negative index into the `|||` variant list and panicked the
     request: only the upper bound was checked. Indexes from the cookie and from
     the Redis `evenly` counter now go through `variantIndex()` (`main.go`),
     which restarts the cycle on anything out of range.
  2. **CSV log export silently returned one page.** `handleLogsExport` asked for
     50000 rows, `CH.Logs` clamped anything above 1000 back to 100. The store
     ceiling is now the export size (`maxLogRows`), and pagination of the
     interactive endpoint moved to the admin layer (`maxPageRows`), so both
     limits say what they mean (`clickhouse.go`, `admin.go`).
  3. **Rate-limit counters could become permanent bans.** `Firewall` set a TTL
     only when the window was positive, so an enabled firewall with `seconds: 0`
     left an immortal Redis key and blocked the IP forever. Same shape for a
     type-2 stream limit with no period. Counters now go through
     `incrWithTTL()`, which guarantees an expiry (`redis.go`). *Since #14 the
     helper is one atomic Lua script (`INCR` + `PEXPIRE`); the earlier
     delete-on-failed-`EXPIRE` compensation is gone because there is nothing
     left to compensate for.*
  4. **`save_ip` appended duplicates until the next hot-reload.** Dedup was done
     against the in-memory index, which only catches up once a minute, so every
     hit from the same crawler IP added another line to `ip_<se>.dat`. A process-level
     set (`savedBotIPs`) now bounds it to one write per IP (`bots.go`).
  5. `?api=` key is compared with `security.EqualTokens` instead of `!=`
     (`api.go`), matching how every other secret in the project is compared.
  6. `chance` now draws through `hitPercent()` instead of repeating the
     `rand.Intn(100)+1` comparison by hand — the same expression whose
     off-by-one had already been fixed once in `api_mac` (`handler.go`).

  Regression tests: `cmd/engine/rotator_test.go`, `cmd/engine/savebotip_test.go`,
  `internal/store/firewall_ttl_test.go`, `internal/store/loglimit_test.go`,
  `internal/admin/logs_limit_test.go`. All five fail on the previous code.

- **Change (2026-08-20):** the group is now resolved from the **first path
  segment** instead of the whole path, so `/promo/iphone-15-sale.html` routes to
  `promo` like `/promo` does. Matching the whole path sent every deep link to
  trash mode — an empty 200 by default — while README and ARCHITECTURE had
  promised first-segment matching all along. `handler.go` + `routing_test.go`.

- **Hot-reload of the groups config (2026-08-20):** the engine re-reads
  `KUZTDS_GROUPS_FILE` when it changes and swaps it in atomically, on the same
  `KUZTDS_RELOAD_INTERVAL` ticker as the `.dat` lists — `config.Groups.Replace`
  finally has a caller. Admin edits go live without a restart. A malformed or
  missing file is skipped and retried, leaving the running config in place, and
  ip_list files a newly added stream references are loaded *before* the swap, so
  the filter never goes live pointing at a list that was never read.
  `cmd/engine/reload.go` + `reload_test.go`.

- **Hardening pass (2026-08-27 → 2026-09-07; #13, #14, #16–#19, #21–#25, #27 —
  #15 superseded by #24, #20 and #26 closed unmerged).** Grouped by what each
  protects:

  *Hot path, correctness.* A failed CURL fetch is no longer served as a normal
  page — the partner's error body used to be rendered under our own 200 and
  logged as an ordinary serve (#13). The `[REMOTE]` value is spliced in **after**
  `render.Expand`, so a partner returning `[RANDLINE-(secret.dat)-1]` can no
  longer make the engine read a file from the data dir and serve it (#18). The
  stream limit is taken in one atomic step instead of read-then-increment, which
  used to overshoot by roughly the concurrency — 114 serves on a limit of 100
  under 64 goroutines, exactly 100 after (#14).

  *Load and lifetime.* logbuf accumulates and inserts in separate goroutines, so
  a slow ClickHouse no longer blinds the buffer at the rate of having no buffer
  at all; shutdown has one owner and two sequential budgets, and a full batch
  queue is its own loss cause, `Losses.Queue` (#19). The fetch cache is bounded
  by bytes and sweeps expired entries — its keys carry `[IP]`/`[CID]`/`[PAR-n]`,
  so it grew by one entry per visitor forever (#24). The HTTP client has a real
  transport with an outbound ceiling; `net/http` defaults to 2 idle connections
  per host and no cap at all on simultaneous ones (#16). Separation lists are
  held in memory instead of being read from disk on every request (#17).

  *Admin.* Login hardening — `SECURITY.md` §3 (#21, #22, #23 via #25).

- **Process note.** #15–#17 were a stack: each targeted the previous *branch*
  rather than `main`. #13 landed first, the chain snapped, and GitHub reported
  the rest as MERGED — which was true of the branches and false of `main`. The
  transport and seplist work sat outside `main` for eleven days until #27 carried
  it over; the login half (#23 into #21's branch) was caught earlier by #25.
  Worth remembering before stacking PRs again: merged into a branch is not
  merged.

## Tests (coverage as of 2026-09-30)
Run: `go test ./...` (unit) and `go test -tags=integration ./...` (with
CH+Redis). `go vet ./...` — clean. Coverage command:
`go test -tags=integration ./... -cover` (ClickHouse up).

| Package | Coverage | Note |
|---------|:--:|---|
| internal/fetch | 98.6% | httptest + `now` override for TTL |
| internal/logbuf | 90.9% | |
| internal/cron | 89.5% | every job against local HTTP servers; config, secrets, schedule, run-now |
| internal/server | 88.9% | |
| internal/geo | 88.8% | MaxMind test databases (City, Country, ASN), reload, offsets |
| internal/router | 87.6% | + asn/org/timezone/get filters, `Why` |
| internal/security | 87.3% | |
| internal/ipindex | 83.7% | |
| internal/store | 80.6% | miniredis (Counters/sessions) + CH under `-tags=integration` |
| internal/seplist | 80.5% | separation lists in memory, hot-reload, malformed lines |
| internal/detect | 80.5% | |
| internal/render | 80.5% | |
| internal/config | 80.0% | |
| internal/admin | 78.5% | login/CSRF/groups/lists/keys/password/export, lookup, simulate, cron endpoints, SPA (web_test.go) |
| cmd/engine | 74.5% | httptest pipeline + helpers + **e2e_test.go** (23 end-to-end scenarios) + geo sources, group links |
| cmd/apiclient | 71.8% | round-trip with a fake TDS (`newClientHandler`) |
| cmd/admin, cmd/cron | 0% | only the `main()` wiring; the logic is in internal/ |

Refactor for testability: hot-path handlers were extracted from `main()`
closures into `cmd/engine/handler.go` (`engineDeps.root`) and `cmd/apiclient`
(`newClientHandler`). ClickHouse tests are under the `integration` build tag
(`go test -tags=integration ./internal/store/`), skipped when CH is unavailable.

## Remaining
See `TODO.md`: a second factor for the admin login, a settings page for the
api client, a fourth device category, version ranges in OS/browser filters,
deployment packaging (Dockerfile, production compose, systemd units).

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

# cron (optional; add KUZTDS_CRON_FILE / KUZTDS_GEO_DB / KUZTDS_ASN_DB to the admin too,
# and the two geo paths to the engine)
KUZTDS_CRON_FILE=/tmp/kuztds-cron.json KUZTDS_DATA_DIR=../database KUZTDS_KEYS_DIR=/tmp/kuztds-keys \
KUZTDS_GROUPS_FILE=configs/test_groups.json \
KUZTDS_GEO_DB=/tmp/kuztds-geo/city.mmdb KUZTDS_ASN_DB=/tmp/kuztds-geo/asn.mmdb \
KUZTDS_CLICKHOUSE_ADDR=localhost:9000 KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/cron
```
The ClickHouse schema is applied automatically from `migrations/clickhouse/*.sql`
on a fresh database; on an existing one the engine and the admin add the newer
columns themselves at start.

## Notes
- The admin and the engine must point to the SAME `KUZTDS_GROUPS_FILE`. The
  admin reads and writes it live; the engine re-reads it when it changes, on the
  `KUZTDS_RELOAD_INTERVAL` ticker. No restart needed.
- Group: `id` = identifier and address `/<id>`; `name` — optional display name.
