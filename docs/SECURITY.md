**English** · [Русский](SECURITY.ru.md)

# KuzTDS security model

A normative document: requirements + what's already done (✅) and what's planned
(⏳). Up to date as of 2026-06-07. The rest — in `TODO.md`.

## 1. Serialization and input
- ✅ **No deserialization of untrusted data** — JSON only (`encoding/json`),
  decoded into a fixed struct.
- ✅ Input IPs go through `netip.ParseAddr`; invalid ones never reach queries.
- ✅ `?api=` mode — base64(**JSON**), access by the `KUZTDS_API_KEY` key.
- ⏳ HMAC-SHA256 signing of packed `[ex]` parameters — NOT implemented; extra GET
  params are passed as-is into `[PAR-n]`.

## 2. Trusted proxies and client IP
- ✅ `server.RealIP`: trusts `X-Forwarded-For`/`CF-Connecting-IP` only if
  `RemoteAddr` is in `KUZTDS_TRUSTED_PROXIES` (CIDR); takes the rightmost
  untrusted address from XFF (CF-Connecting-IP has priority). Otherwise —
  `RemoteAddr`.
- ✅ `CF-IPCountry` is used as a country source (meaningful behind a trusted edge).
- ⏳ Auto-updating the Cloudflare CIDR list (currently set statically via env).

## 3. Admin authentication
- ✅ Passwords: `argon2id` (`golang.org/x/crypto/argon2`).
- ✅ Password change in the UI (`POST /api/password`), hash in
  `KUZTDS_ADMIN_PASSWORD_FILE`.
- ✅ Sessions: 256-bit token (`crypto/rand`), stored in Redis (TTL) or in-memory.
- ✅ Cookie: `HttpOnly`, `Secure` (configurable), `SameSite=Strict`, limited TTL.
- ✅ CSRF: double-submit token on unsafe methods (POST/PUT/DELETE).
- ✅ Login rate-limit: Redis sliding window; no blocking `sleep`.
- ⏳ Forced change of the default password on first start.
- ⏳ TOTP (RFC 6238) and admin IP allowlist.

## 4. Data queries
- ✅ ClickHouse: parameterized queries / typed batch insert only; breakdown
  dimensions — by a column whitelist (not from input).
- ✅ Redis: keys from validated identifiers (group/stream id).
- ✅ `.dat` file names are validated (`^[a-zA-Z0-9._-]+\.dat$`, no `..`); keys/
  dates in `/api/keys` — by a character whitelist.

## 5. Secrets and data
- ✅ Secrets — via `KUZTDS_*` environment variables, not in code/VCS.
- ✅ `.gitignore` excludes `*.local.yaml`, `*.env`, `secrets/`, `*.mmdb`, `.dat`
  (exception — the test `internal/geo/testdata/*.mmdb`).
- The groups config is a JSON file `KUZTDS_GROUPS_FILE` (not yaml); example —
  `configs/*.json`.

## 6. Network and headers
- ✅ Admin security headers: `X-Content-Type-Options: nosniff`, `Referrer-Policy`.
- ✅ Timeouts on external calls (CURL redirect, `[REMOTE]`, PTR lookup).
- ✅ Request body size limits (MaxBytesReader); log export is bounded.
- ⏳ TLS+HSTS (terminated at the edge — deployment), CSP/`X-Frame-Options` for the SPA.

## 7. File operations
- ✅ The engine has no web doc-root → `.dat/.db/.ini` are not reachable over HTTP
  at all.
- ✅ Data paths are not built from user input (no path traversal): group by a
  validated id, lists/keys by checked names.
- ⏳ Downloading IP-list updates over HTTPS with signature verification (part of
  the cron block).

## 8. Process
- ✅ `go vet`, `go test` — green; `govulncheck` — clean (run manually).
- ✅ Dependencies pinned (`go.sum`); errors handled explicitly, structured logs
  (slog).
- ⏳ CI pipeline (lint+test+vuln automatically), `golangci-lint` in the gate.
