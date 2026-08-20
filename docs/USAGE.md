**English** · [Русский](USAGE.ru.md)

# KuzTDS — guide

What it is, how to run it, how to configure it.

> Current project state and resume commands — see **`docs/STATUS.md`**.
> What's left — `TODO.md`.

## What it is

A Traffic Distribution System (TDS): it takes a visitor, decides by rules where
to send them (redirect/iframe/JS/content/stub), separates bots from humans, and
records statistics. Written in Go for speed and security.

Three executables:

| Binary | Purpose | Default port |
|--------|---------|--------------|
| `cmd/engine` | engine (hot path — traffic handling) | 8080 |
| `cmd/admin` | REST API + admin web interface | 8090 |
| `cmd/apiclient` | client for a landing/donor page | 9090 |

Stores: **ClickHouse** (logs/conversions), **Redis** (uniqueness/limits/
firewall/sessions). Both are optional — without them the engine runs, skipping
the corresponding checks.

## Quick start

```bash
# dependencies
brew install go docker
cd kuztds

# infrastructure (ClickHouse + Redis), schema is applied automatically
make infra-up

# admin password hash
go run ./cmd/admin -hash 'my-password'   # prints $argon2id$...

# engine
KUZTDS_DATA_DIR=../database KUZTDS_GROUPS_FILE=configs/groups.example.json \
KUZTDS_TRUSTED_PROXIES=127.0.0.1/32 KUZTDS_POSTBACK_KEY=secret KUZTDS_API_KEY=apikey \
KUZTDS_REDIS_ADDR=localhost:6379 \
KUZTDS_CLICKHOUSE_ADDR=localhost:9000 KUZTDS_CLICKHOUSE_DB=kuztds \
KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/engine

# admin (open http://localhost:8090, login admin)
KUZTDS_ADMIN_PASSWORD_HASH='<hash>' KUZTDS_ADMIN_PASSWORD_FILE=./admin.hash \
KUZTDS_ENGINE_URL=http://localhost:8080 KUZTDS_GROUPS_FILE=configs/groups.example.json \
KUZTDS_DATA_DIR=../database KUZTDS_KEYS_DIR=./keys \
KUZTDS_REDIS_ADDR=localhost:6379 KUZTDS_CLICKHOUSE_ADDR=localhost:9000 \
KUZTDS_CLICKHOUSE_DB=kuztds KUZTDS_CLICKHOUSE_USER=kuztds KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/admin
```

## Environment variables

### engine
| Variable | Purpose |
|----------|---------|
| `KUZTDS_LISTEN` | listen address (`:8080`) |
| `KUZTDS_DATA_DIR` | directory of `.dat` lists (IP, wap, signatures) |
| `KUZTDS_GROUPS_FILE` | JSON groups config (without it — built-in demo) |
| `KUZTDS_TRUSTED_PROXIES` | trusted proxy CIDRs (for XFF/CF), comma-separated |
| `KUZTDS_GEO_DB` | path to a MaxMind `.mmdb` (without it, geo via CF-IPCountry). A test DB is at `internal/geo/testdata/GeoLite2-City-Test.mmdb` |
| `KUZTDS_REDIS_ADDR` / `KUZTDS_REDIS_PASSWORD` | Redis (uniq/limit/firewall) |
| `KUZTDS_CLICKHOUSE_ADDR` / `_DB` / `_USER` / `_PASSWORD` | ClickHouse (logs) |
| `KUZTDS_POSTBACK_KEY` | key for the `?pb=` postback |
| `KUZTDS_API_KEY` | key for the `?api=` mode (api clients) |
| `KUZTDS_KEYS_DIR` | directory for collected keywords |
| `KUZTDS_TRASH_MODE` / `KUZTDS_TRASH_URL` | behavior for an unknown group (0=200,1=redirect,2=403,3=404) |
| `KUZTDS_CURL_CACHE` | CURL redirect cache, minutes |
| `KUZTDS_RELOAD_INTERVAL` | `.dat` hot-reload period (`1m`) |

### admin
| Variable | Purpose |
|----------|---------|
| `KUZTDS_ADMIN_LISTEN` | address (`:8090`) |
| `KUZTDS_ADMIN_USER` | login (`admin`) |
| `KUZTDS_ADMIN_PASSWORD_HASH` | argon2id password hash |
| `KUZTDS_ADMIN_PASSWORD_FILE` | hash file (takes priority; password changes are written here) |
| `KUZTDS_ADMIN_COOKIE_SECURE` | Secure flag for the cookie (`true`; `false` locally) |
| `KUZTDS_ENGINE_URL` | engine base URL (for group links in the UI) |
| `KUZTDS_GROUPS_FILE` / `KUZTDS_DATA_DIR` / `KUZTDS_KEYS_DIR` | same paths as the engine |
| `KUZTDS_REDIS_ADDR` / `KUZTDS_CLICKHOUSE_*` | stores (sessions, statistics) |

### apiclient
`KUZTDS_TDS_URL` (engine URL), `KUZTDS_API_KEY`, `KUZTDS_GROUP_ID`,
`KUZTDS_APICLIENT_LISTEN`, `KUZTDS_TRUSTED_PROXIES`.

## URLs the engine serves

- Group: `http://host/<id>` (and aliases `http://host/<alias>`)
- With a keyword: `http://host/<id>?q=KEYWORD`
- With extra params: `http://host/<id>?p1=...&p2=...` → macros `[PAR-1..5]`
- Postback pixel: `http://host/?pb=KEY&cid=[CID]&profit=1.50`

The group is matched against the **whole path**, not just its first segment:
`/promo` and `/promo/` reach the group `promo`, while `/promo/landing` does not
match anything and falls through to the trash mode. Put everything else in the
query string.

## Admin web interface

Shell: **left sidebar** (icon navigation), **top bar on the right** (period
picker, Settings gear, user chip, log-out button). Dark theme.

Sections: **Dashboard** (filled chart + breakdowns by country/device/OS/
browser/brand/group/source), **Logs** (filters as **dropdown lists with
checkboxes**: group/stream/country/device/OS/browser/brand — values are loaded
from data for the period via `GET /api/logs/filters`, multiple can be checked →
SQL `IN`; plus in-list search, IP field, humans/bots type, pagination, CSV
export), **Conversions**, **Keywords** (view collected keywords), **Groups**
(master–detail editor: group→stream tree + one form pane), **Lists** (`.dat` editor,
incl. WAP operators). Settings (password change) — via the gear on the right.

Groups — a **master–detail** editor. On the left, a collapsible group→stream
tree (chevron per group) with a search box over group and stream names. On the
right, a pane showing **exactly one form**: the group's, or the selected
stream's. Both panes scroll inside themselves and the page does not scroll, so
the form you pick always opens in the same place.

Clicking a group opens the group form (settings, anti-flood, an overview table
of its streams, and the links the engine serves). Clicking a stream — in the
tree, or via "edit →" in that table — replaces the pane with the stream form;
the back link in its header returns to the group.

Stream form tabs: Main · Devices · WAP · Geo · Filters · UA/OS/Brand · Schedule ·
Limit · Bots · Remote · API.

Edits live in the browser until **Save all** (`Ctrl`/`Cmd`+`S` also works). An
"unsaved changes" marker appears next to the button, and leaving the section or
closing the tab asks for confirmation first. Empty and duplicate group IDs are
caught before the request is sent.

The UI is a single embedded file (`internal/admin/web/index.html`, `go:embed`).
Tests: `internal/admin/web_test.go` (SPA serving + structural anchors + key
functions); a JS syntax check is `go test -tags=uitest ./internal/admin/` (needs
node; a single typo in the script breaks the whole SPA — this test catches it).

Filter flag semantics: **off / exclude (blacklist) / include (whitelist)**.

## Groups config (JSON)

Examples: `configs/groups.example.json`, `configs/test_groups.json` (a set of
different groups for traffic runs). Structure: an array of groups `{id, name,
status, redirect, header, out, geo, uniq_method, uniq_seconds, firewall,
save_keys, save_keys_se, aliases, streams[]}`; a stream `{name, status, rules,
out{redirect,out,chance,distribution}, bots, separation, remote, api_mac, curl}`.

- `id` — identifier and address `/<id>`; `name` — optional display name.
- Group defaults (`redirect`/`out`/`header`) apply when a stream sets none.
- Edited via the UI ("Groups": collapsible tree, stream builder, "Save all").
  The engine caches groups at startup — after editing the file directly, restart
  it; edits from the UI are picked up on reload/restart.

## Output macros

`[KEY] [PATH] [IP] [COUNTRY] [CITY] [REGION] [LANG] [DEVICE] [OPERATOR]
[DOMAIN] [USERAGENT] [CID] [PAR-1..5] [()COUNTRY()] [()CITY()]
[RANDNUM-a-b] [RANDSTR-(set)-n] [RANDLINE-(file)-n[/u]] [RANDDFL-(dir)-n[/u]]`

## Implementation notes

- A single long-running process; IP lists in memory (`O(log n)` lookup).
- ClickHouse for logs/conversions; Redis counters for uniqueness/limits/firewall.
- Security: argon2id, server-side sessions, CSRF, parameterized queries, JSON
  only, trusted proxies for XFF.
- Cookie-based uniqueness: a dedicated cookie with a correct TTL.

## Plans
See `TODO.md` (main one — a cron service: IP-list updates, VirusTotal, disk
monitoring/Telegram).
