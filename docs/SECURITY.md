**English** · [Русский](SECURITY.ru.md)

# KuzTDS security model

A normative document: requirements + what's already done (✅) and what's planned
(⏳). Up to date as of 2026-09-07. The rest — in `TODO.md`.

## 1. Serialization and input
- ✅ **No deserialization of untrusted data** — JSON only (`encoding/json`),
  decoded into a fixed struct.
- ✅ Input IPs go through `netip.ParseAddr`; invalid ones never reach queries.
- ✅ `?api=` mode — base64(**JSON**), access by the `KUZTDS_API_KEY` key. The
  key is compared in constant time (`security.EqualTokens`), like every other
  secret here.
- ✅ Values the engine wrote but the client hands back are re-validated, not
  trusted: the `ztrot_*` rotator cookie is an index into the output-variant
  list, and an out-of-range value restarts the cycle instead of indexing the
  slice (`variantIndex` in `cmd/engine/main.go`).
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
- ✅ A password change revokes every session issued under the previous
  password. Each session stores `PwFP` — a fingerprint of the argon2id hash it
  was issued under — and the auth middleware rejects a session whose
  fingerprint no longer matches the current hash.
  **The binding is to the hash, not to the password.** argon2id salts randomly,
  so a hash regenerated from the *same* password is a different hash and a
  different fingerprint. Replacing `KUZTDS_ADMIN_PASSWORD_HASH` (or the
  contents of `KUZTDS_ADMIN_PASSWORD_FILE`) therefore logs everyone out — even
  when the password itself has not changed. Expect a re-login after any
  redeploy that regenerates the hash.
- ✅ The login endpoint answers the same way whether or not the password was
  right when the stores are down. The limiter reports a storage failure instead
  of failing open into `true`, and the handler answers 500 on it **before** any
  password is compared — otherwise a wrong password (401) and a right one whose
  session could not be written (500) told an unauthenticated caller which was
  which, against a dead Redis, at no cost. Narrowed rather than removed: Redis
  can still die between the limiter and `issueSession`, and every probe of that
  residue costs one argon2 verification and a limiter slot.
- ✅ Verification cost is bounded. The parameters come out of the stored hash,
  so a hash with an absurd `m=` would otherwise let anyone make the process
  allocate that much per login attempt. Memory is clamped to 8 MiB…256 MiB and
  the other parameters likewise; a hash outside the range is rejected, not
  honoured.
- ✅ The submitted login name is no longer written to the log. A failed attempt
  records that one happened, not what was tried — passwords land in the name
  field often enough.
- ✅ The password hash has one writer: a shared tmp-file + rename helper (0600
  for the hash, 0644 for a list), so a crash or a full disk cannot leave the
  file empty or half-written. An unreadable or empty hash file is an error, not
  a silent fall back to the environment value — that fallback restored the old
  password.
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
- ✅ Timeouts on external calls (CURL redirect, `[REMOTE]`, PTR lookup), set by
  the caller from the cost of giving up: 2 s for `[REMOTE]` (falls back to
  `reserved`), 8 s for CURL (a failure burns the click).
- ✅ The outbound side is bounded: `MaxConnsPerHost` caps simultaneous
  connections to one partner, so a partner that stalls holds a fixed number of
  sockets instead of one per visitor. `net/http` has no default for it at all.
- ✅ The fetch cache is bounded by bytes (64 MiB over keys and values) and
  sweeps expired entries. Its keys are URLs after macro expansion, and `[IP]`,
  `[CID]` and `[PAR-n]` make a key asked for once and never again — unbounded,
  that map was a log of every URL the process had ever built.
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
- ✅ CI (`.github/workflows/ci.yml`) runs `go build`, `go vet` and `go test`
  on every push and pull request to `main`.
- ✅ Dependencies pinned (`go.sum`); errors handled explicitly, structured logs
  (slog).
- ⏳ `golangci-lint` and `govulncheck` in the gate — the `make lint` / `make vuln`
  targets exist but are run by hand, CI does not call them yet.
- ⏳ The `integration` and `uitest` tagged suites are not in CI either (they need
  ClickHouse/Redis and node); run them locally before a release.
