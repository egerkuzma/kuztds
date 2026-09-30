**English** · [Русский](USAGE.ru.md)

# KuzTDS — guide

What it is, how to run it, how to configure it.

> Current project state and resume commands — see **`docs/STATUS.md`**.
> What's left — `TODO.md`.

## What it is

A Traffic Distribution System (TDS): it takes a visitor, decides by rules where
to send them (redirect/iframe/JS/content/stub), separates bots from humans, and
records statistics. Written in Go for speed and security.

Four executables:

| Binary | Purpose | Default port |
|--------|---------|--------------|
| `cmd/engine` | engine (hot path — traffic handling) | 8080 |
| `cmd/admin` | REST API + admin web interface | 8090 |
| `cmd/cron` | background service: list and geo database updates, VirusTotal, disk, Telegram | — |
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

# cron service (optional) — configured afterwards on the Automation page.
# Give the engine and the admin the same KUZTDS_GEO_DB / KUZTDS_ASN_DB,
# and the admin the same KUZTDS_CRON_FILE.
KUZTDS_CRON_FILE=./cron.json KUZTDS_DATA_DIR=../database KUZTDS_KEYS_DIR=./keys \
KUZTDS_GROUPS_FILE=configs/groups.example.json \
KUZTDS_GEO_DB=./geo/city.mmdb KUZTDS_ASN_DB=./geo/asn.mmdb \
KUZTDS_CLICKHOUSE_ADDR=localhost:9000 KUZTDS_CLICKHOUSE_PASSWORD=devpassword \
go run ./cmd/cron
```

## Environment variables

### engine
| Variable | Purpose |
|----------|---------|
| `KUZTDS_LISTEN` | listen address (`:8080`) |
| `KUZTDS_DATA_DIR` | directory of `.dat` lists (IP, wap, signatures) |
| `KUZTDS_GROUPS_FILE` | JSON groups config (without it — built-in demo) |
| `KUZTDS_TRUSTED_PROXIES` | trusted proxy CIDRs (for XFF/CF), comma-separated |
| `KUZTDS_GEO_DB` | path to a MaxMind-format City or Country `.mmdb` (GeoLite2, GeoIP2, DB-IP Lite). Without it the country comes from `CF-IPCountry` and city/region/time zone are unknown. Test DBs are in `internal/geo/testdata/` |
| `KUZTDS_ASN_DB` | path to a MaxMind-format ASN `.mmdb`; without it the ASN / organization conditions never match |
| `KUZTDS_REDIS_ADDR` / `KUZTDS_REDIS_PASSWORD` | Redis (uniq/limit/firewall) |
| `KUZTDS_CLICKHOUSE_ADDR` / `_DB` / `_USER` / `_PASSWORD` | ClickHouse (logs) |
| `KUZTDS_POSTBACK_KEY` | key for the `?pb=` postback |
| `KUZTDS_API_KEY` | key for the `?api=` mode (api clients) |
| `KUZTDS_KEYS_DIR` | directory for collected keywords |
| `KUZTDS_TRASH_MODE` / `KUZTDS_TRASH_URL` | behavior for an unknown group (0=200,1=redirect,2=403,3=404) |
| `KUZTDS_CURL_CACHE` | CURL redirect cache, minutes |
| `KUZTDS_CURL_UA` | User-Agent the engine sends for CURL / `[REMOTE]` fetches |
| `KUZTDS_RELOAD_INTERVAL` | hot-reload period for lists, groups and geo databases (`1m`) |

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
| `KUZTDS_GEO_DB` / `KUZTDS_ASN_DB` | same paths as the engine — used by Tools (IP lookup) and "Test a visitor" |
| `KUZTDS_CRON_FILE` | the cron service's config file; without it the Automation page is read-only |

### cron
| Variable | Purpose |
|----------|---------|
| `KUZTDS_CRON_FILE` | config file (JSON, `cron.json` by default), written by the admin panel. The service keeps its state in `<file>.status.json` and takes "run now" requests from `<file>.run` |
| `KUZTDS_DATA_DIR` / `KUZTDS_KEYS_DIR` / `KUZTDS_GROUPS_FILE` | same paths as the engine |
| `KUZTDS_GEO_DB` / `KUZTDS_ASN_DB` | where a downloaded City / ASN database is written. The config chooses what to download; the target paths come only from here |
| `KUZTDS_CLICKHOUSE_*` | needed only for conversion notifications |

### apiclient
`KUZTDS_TDS_URL` (engine URL), `KUZTDS_API_KEY`, `KUZTDS_GROUP_ID`,
`KUZTDS_APICLIENT_LISTEN`, `KUZTDS_TRUSTED_PROXIES`, `KUZTDS_COOKIE` (name of
the uniqueness cookie the client sets, `ztu` by default).

## URLs the engine serves

- Group: `http://host/<id>` (and aliases `http://host/<alias>`)
- With a keyword: `http://host/<id>?q=KEYWORD`
- With extra params: `http://host/<id>?p1=...&p2=...` → macros `[PAR-1..5]`
- Postback pixel: `http://host/?pb=KEY&cid=[CID]&profit=1.50`
- Any query-string variable can be a stream condition ("URL parameter":
  `utm_source=fb`, or just `sub1` for "present").

The group is taken from the **first path segment**; anything after it is
ignored. `/promo`, `/promo/` and `/promo/iphone-15-sale.html` all reach the
group `promo`, so a link can be dressed up as a real page without changing where
it routes. An unknown first segment falls through to the trash mode.

## Admin web interface

One **top bar**: navigation, period picker, theme toggle, user menu (Settings,
Log out). No sidebars; every page is full width and has its own address
(`#/flows/promo`, `#/tools/8.8.8.8`), so back/forward and links work. Light and
dark theme: the sun/moon button switches and remembers the choice in the
browser (`localStorage`); until a choice is made the panel follows
`prefers-color-scheme`.

**Saving.** Flows and the automation settings are edited in a working copy and
compared with what was last saved. Whenever they differ a bar appears at the
bottom of any page: **Save** (`Ctrl`/`Cmd`+`S`) or **Discard**. Drafts survive
moving between pages; closing the tab with unsaved changes asks first. Empty,
duplicate and malformed flow IDs are refused before the request is sent.

Pages: **Dashboard** (six KPI tiles — visits, unique, bots, conversions,
profit, CR; a time chart with a hover tooltip; the **performance table** by
flow → stream from `GET /api/stats/performance`, sortable, CSV, a row opens the
flow; breakdowns by country / device / OS / browser / source / network),
**Flows** (below), **Logs** (filters as dropdowns with checkboxes, values
loaded from the data of the period via `GET /api/logs/filters`; IP field;
humans/bots; CSV; a row opens the visit's detail — routing, location, network,
User-Agent, referer), **Conversions**, **Keywords**, **Lists** (`.dat` editor),
**Tools** (below), **Automation** (below), **Settings**.

### Flows

A *flow* is a group of the config. The list shows a card per flow: status, the
period's visits / bots / conversions / profit, the first streams, and the flows
it links to. The card menu turns a flow on/off, duplicates or deletes it.

**A flow's page** draws the flow as the engine runs it:

- the **entry** at the top with the number of visitors;
- a **trunk** going down — visitors fall along it, stream by stream;
- each **stream** is a node on the trunk (number, name, switch, its conditions
  as chips, its note) with a **branch** to its destination on the right (type,
  the first line of the output, variants, what bots get);
- the **default** at the bottom — what everyone left over gets.

Line widths and the labels on the branches are the traffic of the selected
period (count and share), so the picture is also the report: how much each
stream takes, and how much falls through.

Working with it: **drag** a stream by its handle to change the order (the
first match wins, so order is the logic); flip the **switch** to turn it off in
place; the **⋯** menu duplicates it, moves it to another flow or deletes it;
**+ Stream** adds one; the header has the live link with a copy button, the
flow's switch, and a dropdown to jump to another flow.

**The stream editor** is a drawer that slides over the page — the canvas stays
where it was and updates behind it. Sections:

1. **When** — only the conditions the stream carries, each with ✕; **Add
   condition** offers the rest: *Geo* country, city, region, time zone;
   *Network* ASN, organization, mobile operator, IP list; *Device* type, brand,
   OS, browser, Yandex Browser; *Request* language, referer, referer present,
   source domain, keyword, URL parameter, User-Agent; *Visit* unique, days of
   week (Monday first), impression limit. A list condition reads as a sentence
   — `is` / `is not`, or `contains` / `does not contain` for text such as the
   organization or the User-Agent — and its values are chips: type one and
   press Enter or a comma, or paste a whole list; there are no separators to
   type. A text condition can switch to a regular expression. One that is only
   a list of alternatives (`/amazon|google|ovh/i`) is shown and edited as words
   and saved as the same kind of expression, flags included. An expression the
   engine could not compile is flagged while it is typed and marked on the
   canvas. An empty list means no condition.
2. **Then** — the type (six common ones as cards, all of them in the list) and
   the output with the macro list. Several outputs are rows: **Add a variant**,
   and a choice appears of which one a visit gets (at random, in turn per
   visitor, evenly in turn); the canvas lists them one per line. **Proxy a
   page** adds find → replace rows for the fetched page. **Another flow**
   replaces the output with a flow picker; a fixed answer (Stop, 404…) has
   nothing to fill in.
3. **Bots** — detection signals as toggles, what bots get and, when they get an
   answer of their own, its Content-Type and output (variants, and find →
   replace for a proxied page); whether to add detected IPs to the lists.
4. **Advanced** — show chance, Content-Type override, separation, `[REMOTE]`,
   API mac code. A **note** at the end is shown on the canvas.

**Flow settings** (a drawer as well): ID, name, aliases (chips); the default
(type, Content-Type, output — of several variants a random one is served — or
another flow); where the country comes from (CDN
header first / geo database first), uniqueness (IP or cookie, window in hours),
keyword collection; antiflood; the links the engine serves; duplicate, clear
logged visits, delete. Renaming a flow updates the streams that link to it.

**Test a visitor** (button on the flow's page): IP address, a device preset,
and optionally country, language, referer, keyword, URL parameters, "returning
visitor". The answer (`POST /api/simulate`) is drawn on the canvas — the path
in green, **matched** on the winning stream, and on every other stream the
condition that turned the visitor away, highlighted among its chips; streams
below the winner that would also match say so. It uses the flows as they are
on screen, saved or not, and follows links into other flows. Impression
limits, bot checks and the antiflood depend on live traffic and are not
simulated.

### Tools

Enter an IP address (or follow "look up" from a log row): country, city,
region, time zone and UTC offset; ASN and organization; mobile operator; which
`.dat` lists contain it; and which geo databases are loaded, with their build
dates.

### Automation

The page of the cron service. A pill at the top says whether the service is
running (it writes a heartbeat every few seconds). A card per job: switch,
settings, interval, the last result with its time, and **Run now**.

| Job | What it does |
|-----|--------------|
| Bot IP lists | Downloads each source into a `.dat` list. A source with a target writes that list (plain lines, or the `{"prefixes":[…]}` JSON Google and Bing publish); without a target the file is split by `# google`, `# yandex`… sections into `ip_<name>`. `replace` overwrites, `merge` only adds. A source that returns no addresses never empties a list |
| Geo databases | Downloads City and ASN databases (`.mmdb`, `.mmdb.gz`, `.tar.gz`; basic auth for MaxMind: account ID + license key). The download is opened and its type checked before it replaces the file; the engine reloads it on its own |
| VirusTotal | Asks for every domain in stream outputs (and any listed by hand). Newly flagged domains are reported once; optionally the streams sending to them are switched off |
| Disk space | Alerts when free space drops below the threshold, and once more when it recovers |
| Keyword cleanup | Deletes keyword files older than N days |
| Conversion alerts | Sends each new postback to Telegram by a template (`[PROFIT] [GROUP] [STREAM] [COUNTRY] [CITY] [DEVICE] [DOMAIN] [CID]`) |

Telegram (bot token + chat ID) is the notification channel, with a **Send a
test** button. Secrets are never sent back to the browser: a stored token or
key is shown as `********`, and saving it unchanged keeps the stored value.

## Groups config (JSON)

Examples: `configs/groups.example.json`, `configs/test_groups.json` (a set of
different groups for traffic runs). Structure: an array of groups `{id, name,
status, redirect, header, out, geo, uniq_method, uniq_seconds, firewall,
save_keys, save_keys_se, aliases, streams[]}`; a stream `{name, status, comment,
rules, out{redirect,out,chance,distribution,header}, bots, separation, remote,
api_mac, curl}`.

- `id` — identifier and address `/<id>`; `name` — optional display name.
- Group defaults (`redirect`/`out`/`header`) apply when a stream sets none.
- `geo` — which country source wins when both answer: `cf` = the CDN's
  `CF-IPCountry` header first, anything else (`db`) = the geo database first;
  the other is the fallback. The header is believed only when the request came
  through a proxy listed in `KUZTDS_TRUSTED_PROXIES`.
- `redirect: "group"` with `out: "<id>"` — on a stream or as the group default —
  hands the visitor to another group (up to three hops; a missing or disabled
  target answers like an unknown group). The visit is logged under the group
  that served it, and counted for the stream that sent it on as well.
- New rule keys: `asn` (numbers, `AS15169` or `15169`), `org` (substring or
  `/regex/`), `timezone` (`+3`, `+5:30`, or `Europe/Moscow`), `get`
  (`name` or `name=value`). `out.header` overrides the group's Content-Type;
  `comment` is a note for the operator.
- Edited via the UI ("Flows": canvas, drawer editor, one Save bar).
  The engine re-reads the file when it changes, on the `KUZTDS_RELOAD_INTERVAL`
  ticker (1 min by default), so edits from the UI and edits made to the file
  directly both go live without a restart. A file that fails to parse — or goes
  missing — is skipped and retried, leaving the running config in place.

## Output macros

`[KEY] [PATH] [IP] [COUNTRY] [CITY] [REGION] [ASN] [ORG] [TIMEZONE] [UTC]
[LANG] [DEVICE] [OPERATOR] [DOMAIN] [USERAGENT] [CID] [PAR-1..5]
[()COUNTRY()] [()CITY()]
[RANDNUM-a-b] [RANDSTR-(set)-n] [RANDLINE-(file)-n[/u]] [RANDDFL-(dir)-n[/u]]`

`[ASN]` is the number (empty when unknown), `[ORG]` the network's owner
(url-encoded, like `[KEY]`), `[TIMEZONE]` the zone name, `[UTC]` the current
offset (`+3`).

## Implementation notes

- A single long-running process; IP lists in memory (`O(log n)` lookup).
- ClickHouse for logs/conversions; Redis counters for uniqueness/limits/firewall.
- Security: argon2id, server-side sessions, CSRF, parameterized queries, JSON
  only, trusted proxies for XFF and for `CF-IPCountry`.
- Cookie-based uniqueness: a dedicated cookie with a correct TTL.

## Plans
See `TODO.md`.
