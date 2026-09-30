**English** · [Русский](TODO.ru.md)

# TODO — KuzTDS

## Not done
- [ ] TOTP (Google Authenticator) or a captcha for the admin login
- [ ] deployment packaging: Dockerfile, production compose with all four
      services and health checks, systemd units (`TimeoutStopSec` ≥ 20 s for the engine)
- [ ] apiset UI for the api client (currently config via env)
- [ ] a 4th device category "Other" (Smart TV / TV Box)
- [ ] versions in OS/browser filters like `windows:7;10`, `chrome:80;85` (currently "name version" by substring)
- [ ] per-stream time series for the dashboard table (a sparkline per row)
- [ ] outbound S2S postback to the traffic source, cost per visit
- [ ] extra tests for the `main()` wiring of cmd/admin and cmd/cron (the logic is covered in internal/)
- [ ] `gofmt`, `golangci-lint`, `govulncheck` and the tagged suites in CI
- [ ] `/metrics` (Prometheus) and `pprof`

## Decided against
- `eval` redirect type — executing configured code in the engine process is not
  something a Go rewrite should carry over; `javascript` and `curl` cover the uses.

## Done — see docs/STATUS.md
Block 5 (the cron service), geo filters by ASN / organization / time zone,
the URL-parameter filter, Telegram conversion notifications, drag-and-drop
reordering, per-stream note and Content-Type, uniqueness window in hours, flow
duplication, links between groups.
