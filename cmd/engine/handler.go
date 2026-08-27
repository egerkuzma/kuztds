package main

import (
	"log/slog"
	"math/rand"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/fetch"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/logbuf"
	"github.com/egerkuzma/kuztds/internal/render"
	"github.com/egerkuzma/kuztds/internal/router"
	"github.com/egerkuzma/kuztds/internal/server"
	"github.com/egerkuzma/kuztds/internal/store"
)

// engineDeps — hot-path dependencies assembled in main() and passed to the
// handler. Extracted from a closure so the pipeline can be exercised through
// httptest (see handler_test.go).
type engineDeps struct {
	log          *slog.Logger
	lists        *ipindex.Set
	sigs         *detect.Signatures
	geores       geo.Resolver
	counters     *store.Counters
	groups       *config.Groups
	logs         *logbuf.Buffer
	chConn       *store.CH
	fetcher      *fetch.Client
	dataDir      string
	keysDir      string
	postbackKey  string
	apiKey       string
	trashMode    string
	trashURL     string
	curlCacheMin int
}

// ipListFiles collects the names of .dat lists referenced by stream ip_list
// filters (without the extension) so the engine loads them into ipindex.Set
// alongside the standard lists. Otherwise the per-stream IP filter would
// silently fail to fire.
func ipListFiles(groups *config.Groups) []string {
	if groups == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, g := range groups.List() {
		for _, s := range g.Streams {
			f := strings.TrimSuffix(s.Rules.IPList.File, ".dat")
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// hitPercent reports whether a p-percent chance fires: p <= 0 never, p >= 100
// always, and exactly p times in 100 in between.
//
// Written out because "p > rand.Intn(100)+1" reads right and is not: rand+1 is
// uniform over [1,100], so that form fires (p-1) times in 100 — prob 100 gives
// 99% and prob 1 never fires at all.
func hitPercent(p int) bool {
	if p <= 0 {
		return false
	}
	if p >= 100 {
		return true
	}
	return rand.Intn(100)+1 <= p
}

// root — hot-path handler. Full request pipeline:
// postback → api mode → blacklist → group/trash → antiflood → geo/detect →
// uniqueness → router → bots → separation/remote/chance → distribution →
// render → async log → key collection.
func (d *engineDeps) root(w http.ResponseWriter, r *http.Request) {
	// Postback pixel: ?pb=KEY&cid=<id;cid>&profit=X.
	if pb := r.URL.Query().Get("pb"); pb != "" {
		if d.postbackKey != "" && pb == d.postbackKey && d.chConn != nil {
			cid := r.URL.Query().Get("cid")
			profit, _ := strconv.ParseFloat(r.URL.Query().Get("profit"), 64)
			if cid != "" {
				if err := d.chConn.RecordPostback(r.Context(), cid, profit); err != nil {
					d.log.Warn("postback failed", "cid", cid, "err", err)
				}
			}
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	// API-client mode: ?api=base64(JSON). Input is taken from the client request.
	var apiReq *apiRequest
	apiMode := false
	if a := r.URL.Query().Get("api"); a != "" {
		req, ok := parseAPIRequest(a, d.apiKey)
		if !ok {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		apiReq, apiMode = req, true
	}

	var ip netip.Addr
	if apiMode {
		ip, _ = netip.ParseAddr(strings.TrimSpace(apiReq.IP))
	} else {
		ip = server.ClientIP(r.Context())
	}
	if !ip.IsValid() {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 1) Blacklist — instant rejection.
	if _, blocked := d.lists.Lookup("ip_blacklist", ip); blocked {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	rctx := r.Context()

	// Group: by ID/alias — from api.id, or from the FIRST segment of the request
	// path. Everything after it is ignored, so "/promo", "/promo/" and
	// "/promo/iphone-15-sale.html" all resolve to the group "promo".
	//
	// Matching the whole path instead would send every deep link to trash mode —
	// an empty 200 by default, indistinguishable from nothing happening. TDS
	// links are routinely dressed up as real pages, so the extra segments are
	// decoration and must not change routing.
	gid, _, _ := strings.Cut(strings.Trim(r.URL.Path, "/"), "/")
	if apiMode {
		gid = apiReq.ID
	}
	var grp *config.Group
	if d.groups != nil {
		grp, _ = d.groups.Get(gid)
	} else {
		grp = demoGroup
	}
	if grp == nil || !grp.Status {
		trashResult(d.trashMode, d.trashURL).Write(w)
		return
	}

	// 1.5) Antiflood (phase 4): no more than N requests from an IP per window.
	if d.counters != nil && grp.Firewall.Enabled {
		fw := grp.Firewall
		if ok, _ := d.counters.Firewall(rctx, grp.ID, ip, fw.Queries, secs(fw.Seconds)); !ok {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	ua := r.Header.Get("User-Agent")
	ref := r.Header.Get("Referer")
	if apiMode {
		ua, ref = apiReq.UserAgent, apiReq.Referer
	}

	// 2) Device/OS/browser/brand and geo (phases 2, 7).
	info := detect.Parse(ua)
	device := info.Device
	g := d.geores.Resolve(ip)
	// Country from Cloudflare if mmdb returned no answer.
	country := g.Country
	if apiMode && apiReq.CFCountry != "" && apiReq.CFCountry != "-" {
		country = strings.ToLower(apiReq.CFCountry)
	} else if country == geo.Empty {
		if cc := r.Header.Get("CF-IPCountry"); cc != "" {
			country = strings.ToLower(cc)
		}
	}

	// 3.5) Uniqueness: the api client sends its own; otherwise cookie/Redis.
	unique := true
	if apiMode {
		unique = apiReq.Uniq == "yes"
	} else if grp.UniqMethod == "cookie" {
		unique = cookieUnique(w, r, grp)
	} else if d.counters != nil {
		if u, _ := d.counters.Unique(rctx, grp.ID, ip, secs(grp.UniqSeconds)); !u {
			unique = false
		}
	}

	// Input (key/lang/domain/extra params): from the api request or from HTTP.
	key := r.URL.Query().Get("q")
	lang := langOf(r)
	domain := domainOf(ref)
	pars := extraParams(r)
	if apiMode {
		key, lang, domain, pars = apiReq.Key, apiReq.Lang, apiReq.Domain, apiReq.Pars
	}

	// 4) Stream selection (phase 3). Operator — by the label from the wap list.
	operator := router.Empty
	if label, ok := d.lists.Lookup("wap", ip); ok && label != "" {
		operator = label
	}
	v := router.Visitor{
		Lang:       lang,
		Country:    country,
		City:       g.City,
		Region:     g.Region,
		UA:         ua,
		Referer:    refOrEmpty(ref),
		Domain:     domain,
		Key:        key,
		Device:     device,
		Operator:   operator,
		OS:         info.OS,
		OSVersion:  info.OSVersion,
		Browser:    info.Browser,
		BrowserVer: info.BrowserVer,
		Brand:      info.Brand,
		Unique:     unique,
		IP:         ip,
	}
	deps := router.Deps{IP: d.lists}
	if d.counters != nil {
		deps.Limiter = limiter{c: d.counters, ctx: rctx, id: grp.ID}
	}
	// Output defaults — from the group; the stream overrides them.
	streamName := "-"
	redirect := grp.Redirect
	outRaw := grp.Out
	ctype := grp.Header
	var selStream *config.Stream
	if s, ok := router.Select(grp, v, deps); ok {
		selStream = s
		streamName = s.Name
		if s.Out.Redirect != "" {
			redirect = s.Out.Redirect
			outRaw = s.Out.Out
		}
		if d.counters != nil && s.Rules.Limit.Enabled {
			_ = d.counters.RecordServe(rctx, grp.ID, s.Name, s.Rules.Limit)
		}
	}

	// 4.5) Bot detection by the selected stream's toggles (after selection).
	botsCfg := config.Bots{CheckUA: true} // no stream — signatures only (for the log)
	if selStream != nil {
		botsCfg = selStream.Bots
	}
	bot := detectBot(d.lists, d.sigs, d.dataDir, ip, ua, refOrEmpty(ref), v.Lang, botsCfg)
	// Separate serving for bots (bot_redirect). "skip"/"" → normal stream (the bot is still logged).
	botServed := false
	if bot != detect.BotNone && selStream != nil && botsCfg.Redirect != "" && botsCfg.Redirect != "skip" {
		redirect = botsCfg.Redirect
		outRaw = botsCfg.Out
		if botsCfg.Header != "" {
			ctype = botsCfg.Header
		}
		botServed = true
	}

	// separation: substitute the output by keyword (unless this is bot serving).
	if !botServed && selStream != nil && selStream.Separation.Enabled &&
		v.Key != "" && selStream.Separation.File != "" {
		if o := separationOut(d.dataDir, selStream.Separation.File, v.Key); o != "" {
			outRaw = o
		}
	}

	// remote: load [REMOTE] from an external URL (cache, regex, fallback).
	if !botServed && selStream != nil && selStream.Remote.Enabled && strings.Contains(outRaw, "[REMOTE]") {
		rv := remoteValue(d.fetcher, rctx, selStream.Remote, ip.String(), country, g.City, v.Lang, v.Key)
		outRaw = strings.ReplaceAll(outRaw, "[REMOTE]", rv)
	}

	// chance: for javascript/js_selection — show with probability chance %.
	// Here chance 0 means "not configured", so only a real 1..99 gates the
	// output; the draw itself goes through hitPercent, which carries the
	// off-by-one this comparison would otherwise have to get right by hand.
	if !botServed && selStream != nil && (redirect == "javascript" || redirect == "js_selection") {
		if ch := selStream.Out.Chance; ch > 0 && ch < 100 && !hitPercent(ch) {
			redirect = "stop"
			outRaw = ""
		}
	}

	// distribution of out variants (|||): random/rotator/evenly.
	dist := ""
	if selStream != nil {
		dist = selStream.Out.Distribution
	}
	outRaw = pickOut(w, r, d.counters, rctx, grp.ID, streamName, outRaw, dist)

	// 5) Render the output: macros, api_mac, CURL fetch, or a normal redirect.
	cid := newCID()
	md := render.MacroDeps{
		Key: v.Key, Path: r.Host, IP: ip.String(), Country: country, City: g.City,
		Region: g.Region, Lang: v.Lang, Device: device, Operator: operator,
		Domain: v.Domain, UserAgent: ua, CID: cid, Pars: pars,
		DataDir: d.dataDir}
	out := render.Expand(outRaw, md)

	// api_mac: mac code in the api response with probability Prob %.
	mac := ""
	if redirect == "api" && selStream != nil && selStream.APIMac.Enabled &&
		selStream.APIMac.Code != "" && hitPercent(selStream.APIMac.Prob) {
		mac = render.Expand(selStream.APIMac.Code, md)
	}

	// logRedirect is what the event carries. It tracks redirect except when a
	// curl fetch fails: the visitor is then served something other than the
	// partner's page, and the report must not call that an ordinary serve.
	logRedirect := redirect

	var res render.Result
	if redirect == "curl" {
		rules := ""
		if selStream != nil {
			if rules = selStream.Curl; botServed {
				rules = selStream.BotCurl
			}
		}
		ct := ctype
		if ct == "" {
			ct = "text/html; charset=utf-8"
		}
		body, ok := curlBody(d.fetcher, rctx, out, render.Expand(rules, md), cacheMinFor(outRaw, d.curlCacheMin))
		if ok {
			res = render.Result{Status: http.StatusOK, ContentType: ct, Body: []byte(body)}
		} else {
			// No stream-level fallback exists for curl (unlike Remote.Reserved),
			// so reuse the configured trash behaviour — the same answer the
			// engine already gives when it cannot serve a group. Anything is
			// better than a blank 200, which burns the click and hides it.
			logRedirect = "curl_error"
			res = trashResult(d.trashMode, d.trashURL)
			d.log.Error("engine: curl fetch failed", "group", grp.ID, "stream", streamName, "url", out)
		}
	} else {
		res = render.Do(redirect, out, render.Options{ContentType: ctype, Path: r.Host,
			Country: country, City: g.City, Region: g.Region, Device: device,
			Operator: operator, Bot: bot, Uniq: b2yn(unique), Lang: v.Lang, Mac: mac})
	}

	// 6) Event log — asynchronous, the response does not wait for the write (phase 4).
	if d.logs != nil {
		d.logs.Push(logbuf.Event{
			Ts: time.Now(), GroupID: grp.ID, GroupName: grp.Name,
			Stream: streamName, Out: out, Redirect: logRedirect, Device: device,
			Operator: operator, Country: country, City: g.City, Region: g.Region,
			Lang: v.Lang, Uniq: b2u8(unique), Bot: bot, IP: ip.String(),
			Referer: v.Referer, UserAgent: ua, Domain: v.Domain, Keyword: v.Key,
			OS: info.OS, OSVersion: info.OSVersion, Browser: info.Browser,
			BrowserV: info.BrowserVer, Brand: info.Brand, CID: cid,
		})
	}

	// Keyword collection.
	if bot == detect.BotNone {
		if grp.SaveKeys && v.Key != "" {
			appendKey(d.keysDir, grp.ID, v.Key, false)
		}
		if grp.SaveKeysSE {
			if kse := parseSEKeyword(ref); kse != "" {
				appendKey(d.keysDir, grp.ID, kse, true)
			}
		}
	}

	// Diagnostic headers (before writing the response) + render.
	h := w.Header()
	h.Set("X-Kuztds-Bot", emptyDash(bot))
	h.Set("X-Kuztds-Device", device)
	h.Set("X-Kuztds-Country", country)
	h.Set("X-Kuztds-Stream", streamName)
	h.Set("X-Kuztds-Uniq", b2yn(unique))
	res.Write(w)
}
