// Command engine — the kuztds hot path (visitor traffic processing).
//
// Full request pipeline:
//
//	realip → blacklist (ipindex) → antiflood (Redis) → geo (mmdb/CF) →
//	detect (device+bots) → uniqueness (Redis) → router (stream selection) →
//	render (macros + redirect type) → async log (ClickHouse).
//
// All state (IP lists, signatures, group config) is kept in memory and
// refreshed in the background — without reading files on every request. Stores
// (Redis/ClickHouse) and the JSON group config are connected optionally via KUZTDS_*.
// See docs/ARCHITECTURE.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/fetch"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/logbuf"
	"github.com/egerkuzma/kuztds/internal/render"
	"github.com/egerkuzma/kuztds/internal/router"
	"github.com/egerkuzma/kuztds/internal/seplist"
	"github.com/egerkuzma/kuztds/internal/server"
	"github.com/egerkuzma/kuztds/internal/store"
)

// IP lists the engine keeps in memory (names = <name>.dat files).
var ipLists = []string{
	"ip_blacklist",
	"ip_google", "ip_bing", "ip_yandex", "ip_yahoo", "ip_mail", "ip_baidu",
	"ip_others",
	"wap", // carriers (with labels like #beeline etc.)
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	dataDir := getenv("KUZTDS_DATA_DIR", "./data")
	addr := getenv("KUZTDS_LISTEN", ":8080")
	reload := getdur("KUZTDS_RELOAD_INTERVAL", time.Minute)

	// Load all IP lists into memory (phase 1).
	lists := ipindex.NewSet(dataDir, log)
	lists.Load(ipLists...)

	// Bot signatures (phase 2): UA/referer/ua_blacklist with hot-reload.
	sigs := detect.NewSignatures(dataDir, log)

	// Geo (phase 2): mmdb if KUZTDS_GEO_DB is set, otherwise Nop (geo filter disabled).
	var geores geo.Resolver = geo.Nop{}
	if p := os.Getenv("KUZTDS_GEO_DB"); p != "" {
		if m, err := geo.OpenMMDB(p); err != nil {
			log.Warn("geo db not loaded, using Nop", "err", err)
		} else {
			geores = m
			log.Info("geo db loaded", "path", p)
		}
	}

	// Background hot-reload of changed files.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go lists.Watch(ctx, reload)
	go sigs.Watch(ctx, reload)

	// Redis counters (phase 4): uniq/firewall/limit. Optional.
	var counters *store.Counters
	if a := os.Getenv("KUZTDS_REDIS_ADDR"); a != "" {
		if rdb, err := store.OpenRedis(a, os.Getenv("KUZTDS_REDIS_PASSWORD"), 0); err != nil {
			log.Warn("redis not connected", "err", err)
		} else {
			counters = store.NewCounters(rdb)
			log.Info("redis connected", "addr", a)
		}
	}

	// trash modes for unknown/disabled groups (like trash()).
	trashMode := getenv("KUZTDS_TRASH_MODE", "0") // 0=200,1=redirect,2=403,3=404
	trashURL := os.Getenv("KUZTDS_TRASH_URL")

	// Postback and keyword collection.
	postbackKey := os.Getenv("KUZTDS_POSTBACK_KEY")
	keysDir := getenv("KUZTDS_KEYS_DIR", "keys")
	apiKey := os.Getenv("KUZTDS_API_KEY") // key for ?api= mode (clients)

	// HTTP fetcher for CURL redirect and [REMOTE] (with in-memory cache).
	fetcher := fetch.New(getenv("KUZTDS_CURL_UA", "Mozilla/5.0 (Windows NT 6.1; Win64; x64; rv:71.0) Gecko/20100101 Firefox/71.0"))
	curlCacheMin := 60
	if v := os.Getenv("KUZTDS_CURL_CACHE"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			curlCacheMin = n
		}
	}

	// Group config (phase 6): JSON file. Without it — the built-in demo group.
	var groups *config.Groups
	groupsFile := os.Getenv("KUZTDS_GROUPS_FILE")
	if groupsFile != "" {
		if gg, err := config.LoadGroups(groupsFile); err != nil {
			log.Warn("groups file not loaded, using demo", "err", err)
		} else {
			groups = gg
			log.Info("groups loaded", "count", len(gg.List()))
		}
	}

	// Separation lists, held in memory for the same reason as the ip lists: the
	// previous implementation re-read and re-scanned the whole file on every
	// request that carried a keyword.
	seps := seplist.NewSet(dataDir, log)
	seps.Load(separationFiles(groups)...)
	go seps.Watch(ctx, reload)

	// Load the custom ip_list files referenced by group streams
	// (besides the standard ipLists), so the per-stream IP filter works.
	known := append([]string(nil), ipLists...)
	if extra := ipListFiles(groups); len(extra) > 0 {
		lists.Load(extra...)
		known = append(known, extra...)
		log.Info("extra ip lists loaded", "count", len(extra))
	}

	// Hot-reload of the groups config, like the .dat lists and signatures:
	// edits saved in the admin panel go live within the reload interval
	// instead of waiting for a restart.
	if groups != nil {
		go newGroupsWatcher(groupsFile, groups, lists, seps, log, known).watch(ctx, reload)
	}

	// ClickHouse logs (phase 4): asynchronous batch writes. Optional.
	var logs *logbuf.Buffer
	var chConn *store.CH
	if a := os.Getenv("KUZTDS_CLICKHOUSE_ADDR"); a != "" {
		ch, err := store.OpenCH(a, getenv("KUZTDS_CLICKHOUSE_DB", "kuztds"),
			getenv("KUZTDS_CLICKHOUSE_USER", "kuztds"), os.Getenv("KUZTDS_CLICKHOUSE_PASSWORD"))
		if err != nil {
			log.Warn("clickhouse not connected", "err", err)
		} else {
			chConn = ch
			logs = logbuf.New(ch, 10000, 1000, time.Second, log)
			go logs.Run(ctx)
			log.Info("clickhouse connected", "addr", a)
		}
	}

	// realip: trust XFF/CF only from configured proxies (spoofing protection).
	realIP, err := server.NewRealIP(trustedProxies())
	if err != nil {
		log.Error("realip init", "err", err)
		os.Exit(1)
	}

	d := &engineDeps{
		log: log, lists: lists, sigs: sigs, seps: seps, geores: geores, counters: counters,
		groups: groups, logs: logs, chConn: chConn, fetcher: fetcher,
		dataDir: dataDir, keysDir: keysDir, postbackKey: postbackKey, apiKey: apiKey,
		trashMode: trashMode, trashURL: trashURL, curlCacheMin: curlCacheMin,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Body stays exactly "ok" for existing probes; the loss counter rides
		// along in a header so it can be scraped without a metrics endpoint.
		if logs != nil {
			l := logs.Losses()
			w.Header().Set("X-Events-Lost", strconv.FormatInt(l.Total(), 10))
			w.Header().Set("X-Events-Lost-Detail", fmt.Sprintf("full=%d insert=%d late=%d", l.Full, l.Insert, l.Late))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", d.root)

	srv := &http.Server{
		Addr:              addr,
		Handler:           realIP.Middleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("engine listening", "addr", addr, "lists", len(ipLists), "reload", reload.String())
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	// Stop serving before stopping the log buffer, otherwise in-flight requests
	// keep pushing events into a buffer nobody drains any more.
	_ = srv.Shutdown(sctx)
	cancel()
	if logs != nil {
		// Wait for the final flush, but only within the shutdown budget: a dead
		// ClickHouse would otherwise hold the process for its own 30s timeout.
		select {
		case <-logs.Done():
		case <-sctx.Done():
			log.Warn("logbuf: final flush did not finish within the shutdown deadline")
		}
		l := logs.Losses()
		log.Info("engine stopped", "events_lost", l.Total(),
			"lost_full", l.Full, "lost_insert", l.Insert, "lost_late", l.Late)
		return
	}
	log.Info("engine stopped")
}

// demoGroup — built-in demo configuration used when KUZTDS_GROUPS_FILE is not set.
var demoGroup = &config.Group{
	ID: "demo", Name: "demo", Status: true,
	Redirect:    "stop", // group default when no stream is selected
	UniqSeconds: 86400,
	Firewall:    config.FirewallRule{Enabled: false, Queries: 100, Seconds: 60},
	Streams: []config.Stream{
		// phones from RU only
		{Name: "mobile_ru", Status: true,
			Rules: config.Rules{Computer: config.FlagA, Tablet: config.FlagA,
				Country: config.ListFilter{Flag: config.FlagB, Raw: "ru", Values: []string{"ru"}}},
			Out:  config.Output{Redirect: "http_redirect", Out: "https://m.example.com/?k=[KEY]&r=[RANDNUM-1000-9999]"},
			Bots: config.Bots{CheckUA: true, EmptyUA: true, IPGoogle: true, IPYandex: true, IPOthers: true, Redirect: "404_not_found"}},
		// desktop (phones/tablets are blocked)
		{Name: "desktop", Status: true,
			Rules: config.Rules{Phone: config.FlagA, Tablet: config.FlagA},
			Out:   config.Output{Redirect: "http_redirect", Out: "https://example.com/?k=[KEY]"},
			Bots:  config.Bots{CheckUA: true, IPGoogle: true, IPYandex: true, IPOthers: true, Redirect: "404_not_found"}},
		// catch-all
		{Name: "default", Status: true,
			Out: config.Output{Redirect: "show_text", Out: "no offer"}},
	},
}

// cookieUnique implements "cookie-based" uniqueness: a dedicated cookie used
// only for uniqueness (not mixed with the rotator counter), with HttpOnly/SameSite
// flags and a correct TTL = UniqSeconds.
// First visit (no cookie) → unique and the cookie is set; repeat → not unique.
func cookieUnique(w http.ResponseWriter, r *http.Request, grp *config.Group) bool {
	name := "ztu_" + grp.ID
	if _, err := r.Cookie(name); err == nil {
		return false
	}
	maxAge := grp.UniqSeconds
	if maxAge <= 0 {
		maxAge = 86400
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "1", Path: "/", MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	return true
}

// newCID generates a short click identifier (for [CID]/postback).
func newCID() string {
	const cs = "0123456789abcdef"
	b := make([]byte, 10)
	for i := range b {
		b[i] = cs[rand.Intn(len(cs))]
	}
	return string(b)
}

// extraParams returns GET parameter values (except q) in URL order, up to 5.
func extraParams(r *http.Request) []string {
	var out []string
	for _, pair := range strings.Split(r.URL.RawQuery, "&") {
		if pair == "" {
			continue
		}
		k, v, _ := strings.Cut(pair, "=")
		if k == "q" {
			continue
		}
		dv, err := url.QueryUnescape(v)
		if err != nil {
			dv = v
		}
		out = append(out, dv)
		if len(out) == 5 {
			break
		}
	}
	return out
}

// pickOut selects an output variant from out, separated by |||, according to the
// distribution type: rotator (cookie), evenly (Redis counter), random (default).
func pickOut(w http.ResponseWriter, r *http.Request, counters *store.Counters, ctx context.Context, groupID, stream, raw, dist string) string {
	if !strings.Contains(raw, "|||") {
		return raw
	}
	var parts []string
	for _, p := range strings.Split(raw, "|||") {
		parts = append(parts, strings.TrimSpace(p))
	}
	switch dist {
	case "rotator":
		name := "ztrot_" + groupID + "_" + stream
		idx := 0
		if c, err := r.Cookie(name); err == nil {
			if n, e := strconv.Atoi(c.Value); e == nil {
				idx = variantIndex(n+1, len(parts))
			}
		}
		http.SetCookie(w, &http.Cookie{Name: name, Value: strconv.Itoa(idx), Path: "/", HttpOnly: true, MaxAge: 86400})
		return parts[idx]
	case "evenly":
		if counters != nil {
			return parts[variantIndex(counters.Rotate(ctx, groupID, stream, len(parts)), len(parts))]
		}
		fallthrough
	default:
		return parts[rand.Intn(len(parts))]
	}
}

// variantIndex folds i into [0,n), restarting the cycle on anything out of
// range. Neither rotator nor evenly produces its index locally: the rotator
// reads it back from a cookie the visitor may rewrite, and evenly gets it from
// a Redis counter. A cookie of "-5" used to index the variant slice at -4 and
// take the whole request down with a panic.
func variantIndex(i, n int) int {
	if n <= 0 || i < 0 || i >= n {
		return 0
	}
	return i
}

// trashResult builds the response for an unknown/disabled group (trash modes).
func trashResult(mode, url string) render.Result {
	switch mode {
	case "1":
		if url != "" {
			return render.Result{Status: http.StatusFound, Location: url}
		}
	case "2":
		return render.Do("403_forbidden", "", render.Options{})
	case "3":
		return render.Do("404_not_found", "", render.Options{})
	}
	return render.Result{Status: http.StatusOK}
}

// limiter adapts store.Counters to the router.Limiter interface (per-request).
type limiter struct {
	c   *store.Counters
	ctx context.Context
	id  string
}

func (l limiter) Allowed(stream string, rule config.LimitRule) bool {
	ok, err := l.c.LimitAllowed(l.ctx, l.id, stream, rule)
	if err != nil {
		return true // fail-open
	}
	return ok
}

func secs(n int) time.Duration { return time.Duration(n) * time.Second }

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func b2yn(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func langOf(r *http.Request) string {
	if al := r.Header.Get("Accept-Language"); len(al) >= 2 {
		return strings.ToLower(al[:2])
	}
	return router.Empty
}

func refOrEmpty(ref string) string {
	if ref == "" {
		return router.Empty
	}
	return ref
}

func domainOf(ref string) string {
	if ref == "" {
		return "unknown"
	}
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		return u.Host
	}
	return "unknown"
}

// trustedProxies returns the CIDRs of trusted proxies from KUZTDS_TRUSTED_PROXIES
// (comma-separated). Defaults to localhost only (XFF from outside is not accepted).
func trustedProxies() []string {
	if v := os.Getenv("KUZTDS_TRUSTED_PROXIES"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"127.0.0.1/32", "::1/128"}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getdur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
