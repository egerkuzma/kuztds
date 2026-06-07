**English** · [Русский](TODO.ru.md)

# TODO — KuzTDS

## Large
- [ ] **Block 5 — cron service** (deferred by decision):
  - bot IP-list updates (`update_ip_url`, replace/merge modes, parsing `# se` sections)
  - VirusTotal domain checks (`key_vt`, schedule, actions on infection)
  - free disk space monitoring + Telegram notifications
  - cleanup of stale data (currently via TTL in CH — revisit if needed)

## Tests — DONE (2026-06-07)
Coverage added across all gaps; details and the table — in `docs/STATUS.md`.
- [x] cmd/engine — httptest pipeline run (extracted into `engineDeps.root`)
- [x] internal/fetch — Get/GetCached/TTL/timeout/errors
- [x] internal/admin — handlers (login/CSRF/groups/lists/keys/password/export) + file stores
- [x] internal/store — miniredis (sessions/LoginAllow/Rotate) + ClickHouse under `-tags=integration`
- [x] cmd/apiclient — round-trip with a fake TDS (extracted into `newClientHandler`)

## Minor / optional
- [ ] extra tests for the cmd/admin main() wiring (currently 0%; logic covered in internal/admin)
- [ ] captcha / TOTP (Google Authenticator) for admin login
- [ ] drag-and-drop stream reordering with the mouse (currently ↑/↓ buttons)
- [ ] apiset UI for the api client (currently config via env)
- [ ] per-stream Header/Comment in the stream form
- [ ] uniq_time in hours in the UI (currently in seconds)

## Possible future features
- [ ] Geo filters: ASN, organization (regex), UTC timezone (`+3,+5:30`)
- [ ] Filter by a GET variable (`get:str`)
- [ ] A 4th device category "Other" (Smart TV / TV Box)
- [ ] `eval` redirect type
- [ ] Telegram conversion notifications (macros [PROFIT][GROUP][STREAM]...)
- [ ] Versions in OS/browser filters like `windows:7;10`, `chrome:80;85` (currently "name version" by substring)
- [ ] Group cloning/migration

## Done — see docs/STATUS.md
