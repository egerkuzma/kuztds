package admin

import (
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/cron"
	"github.com/egerkuzma/kuztds/internal/detect"
	"github.com/egerkuzma/kuztds/internal/geo"
	"github.com/egerkuzma/kuztds/internal/ipindex"
	"github.com/egerkuzma/kuztds/internal/router"
)

// GeoInfo is what the panel needs from the geo resolver: answers and the list
// of loaded databases (geo.DB provides both).
type GeoInfo interface {
	geo.Resolver
	Databases() []geo.Info
}

// dirLists answers "is this IP in list <name>" straight from the .dat files.
// The engine keeps the lists in memory; the panel asks a few times a day, so
// it reads the file when asked and remembers it for the rest of the request.
type dirLists struct {
	dir   string
	cache map[string]*ipindex.Index
}

func (d *dirLists) Lookup(name string, ip netip.Addr) (string, bool) {
	if d.dir == "" || !safeSeg.MatchString(name) {
		return "", false
	}
	idx, ok := d.cache[name]
	if !ok {
		idx, _ = ipindex.LoadFile(filepath.Join(d.dir, name+".dat"))
		if d.cache == nil {
			d.cache = map[string]*ipindex.Index{}
		}
		d.cache[name] = idx
	}
	if idx == nil {
		return "", false
	}
	return idx.Lookup(ip)
}

// names lists the IP lists in the directory (files the index can parse at
// least one range from are IP lists; signatures and separation files are not).
func (d *dirLists) names() []string {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if n := e.Name(); !e.IsDir() && strings.HasSuffix(n, ".dat") {
			out = append(out, strings.TrimSuffix(n, ".dat"))
		}
	}
	sort.Strings(out)
	return out
}

type visitorInfo struct {
	IP       string `json:"ip"`
	Country  string `json:"country"`
	City     string `json:"city"`
	Region   string `json:"region"`
	Timezone string `json:"timezone"`
	UTC      string `json:"utc"`
	ASN      uint32 `json:"asn"`
	Org      string `json:"org"`
	Operator string `json:"operator"`
	Device   string `json:"device,omitempty"`
	OS       string `json:"os,omitempty"`
	Browser  string `json:"browser,omitempty"`
	Brand    string `json:"brand,omitempty"`
}

func (s *Server) resolve(ip netip.Addr, lists *dirLists) (geo.Geo, visitorInfo) {
	g := geo.None()
	if s.cfg.Geo != nil {
		g = s.cfg.Geo.Resolve(ip)
	}
	op := router.Empty
	if label, ok := lists.Lookup("wap", ip); ok && label != "" {
		op = label
	}
	return g, visitorInfo{
		IP: ip.String(), Country: g.Country, City: g.City, Region: g.Region,
		Timezone: g.Timezone, UTC: g.UTCOffset(time.Now()), ASN: g.ASN, Org: g.Org, Operator: op,
	}
}

// handleLookup — what the engine would learn from an IP address: geo, network,
// and which of the .dat lists it falls into.
func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request) {
	ip, err := netip.ParseAddr(strings.TrimSpace(r.URL.Query().Get("ip")))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not an IP address")
		return
	}
	ip = ip.Unmap()
	lists := &dirLists{dir: s.cfg.DataDir}
	_, vi := s.resolve(ip, lists)

	type hit struct {
		List  string `json:"list"`
		Label string `json:"label,omitempty"`
	}
	hits := []hit{}
	for _, name := range lists.names() {
		if label, ok := lists.Lookup(name, ip); ok {
			hits = append(hits, hit{List: name, Label: label})
		}
	}
	dbs := []geo.Info{}
	if s.cfg.Geo != nil {
		dbs = append(dbs, s.cfg.Geo.Databases()...)
	}
	writeJSON(w, http.StatusOK, map[string]any{"visitor": vi, "lists": hits, "databases": dbs})
}

type simRequest struct {
	Groups  []config.Group `json:"groups"` // as edited in the browser, saved or not
	Group   string         `json:"group"`
	Visitor struct {
		IP        string `json:"ip"`
		UserAgent string `json:"useragent"`
		Referer   string `json:"referer"`
		Lang      string `json:"lang"`
		Key       string `json:"key"`
		Query     string `json:"query"`
		Country   string `json:"country"` // overrides the database (what a CDN header would say)
		Repeat    bool   `json:"repeat"`  // a returning, non-unique visitor
	} `json:"visitor"`
}

type simStream struct {
	Name  string `json:"name"`
	On    bool   `json:"on"`
	Match bool   `json:"match"`
	Why   string `json:"why,omitempty"` // the rule that rejected the visitor
}

type simStep struct {
	Group    string      `json:"group"`
	Streams  []simStream `json:"streams"`
	Selected string      `json:"selected"` // stream name, or "-" for the group default
	Redirect string      `json:"redirect"`
	Out      string      `json:"out"`
	Error    string      `json:"error,omitempty"`
}

const simMaxHops = 3 // the engine's limit on "group" links

// handleSimulate walks a visitor through a group the way the engine's router
// would and reports, for every stream, whether it matches and which rule
// turned the visitor away. It works on the groups sent in the request, so an
// edit can be tried before it is saved.
//
// Not simulated: impression limits (they spend a counter), bot detection and
// the antiflood — those depend on live state.
func (s *Server) handleSimulate(w http.ResponseWriter, r *http.Request) {
	var req simRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(req.Visitor.IP))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "not an IP address")
		return
	}
	ip = ip.Unmap()
	find := func(id string) *config.Group {
		for i := range req.Groups {
			g := &req.Groups[i]
			if g.ID == id {
				return g
			}
			for _, a := range g.Aliases {
				if a == id {
					return g
				}
			}
		}
		return nil
	}
	grp := find(req.Group)
	if grp == nil {
		writeErr(w, http.StatusBadRequest, "unknown group")
		return
	}

	lists := &dirLists{dir: s.cfg.DataDir}
	g, vi := s.resolve(ip, lists)
	if cc := strings.ToLower(strings.TrimSpace(req.Visitor.Country)); len(cc) == 2 {
		if g.Country == geo.Empty || grp.Geo == "cf" {
			vi.Country = cc
		}
	}
	info := detect.Parse(req.Visitor.UserAgent)
	vi.Device, vi.OS, vi.Browser, vi.Brand = info.Device, strings.TrimSpace(info.OS+" "+info.OSVersion),
		strings.TrimSpace(info.Browser+" "+info.BrowserVer), info.Brand
	q, _ := url.ParseQuery(strings.TrimPrefix(req.Visitor.Query, "?"))
	ref := strings.TrimSpace(req.Visitor.Referer)
	domain := router.Empty
	if u, err := url.Parse(ref); err == nil && u.Host != "" {
		domain = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	}
	if ref == "" {
		ref = router.Empty
	}
	lang := strings.ToLower(strings.TrimSpace(req.Visitor.Lang))
	if lang == "" {
		lang = router.Empty
	}
	v := router.Visitor{
		Lang: lang, Country: vi.Country, City: g.City, Region: g.Region,
		UA: req.Visitor.UserAgent, Referer: ref, Domain: domain, Key: req.Visitor.Key,
		Device: info.Device, Operator: vi.Operator,
		OS: info.OS, OSVersion: info.OSVersion, Browser: info.Browser, BrowserVer: info.BrowserVer, Brand: info.Brand,
		Unique: !req.Visitor.Repeat, IP: ip,
		ASN: g.ASN, Org: g.Org, Timezone: g.Timezone, TZOffset: vi.UTC, Query: q,
	}
	deps := router.Deps{IP: lists} // no Limiter: asking must not spend a limit

	var path []simStep
	for hop := 0; ; hop++ {
		step := simStep{Group: grp.ID, Selected: "-", Redirect: grp.Redirect, Out: grp.Out, Streams: []simStream{}}
		for i := range grp.Streams {
			st := &grp.Streams[i]
			ss := simStream{Name: st.Name, On: st.Status}
			// Streams after the winner are still evaluated: "would this one
			// have matched too" is what explains a surprising order.
			if st.Status {
				ss.Why = router.Why(st, v, deps)
				ss.Match = ss.Why == ""
				if ss.Match && step.Selected == "-" {
					step.Selected = st.Name
					if st.Out.Redirect != "" {
						step.Redirect, step.Out = st.Out.Redirect, st.Out.Out
					}
				}
			}
			step.Streams = append(step.Streams, ss)
		}
		if step.Redirect != "group" {
			path = append(path, step)
			break
		}
		next := find(strings.TrimSpace(step.Out))
		switch {
		case next == nil:
			step.Error = "links to a group that does not exist: " + strconv.Quote(step.Out)
		case !next.Status:
			step.Error = "links to a disabled group: " + next.ID
		case next == grp:
			step.Error = "links to itself"
		case hop >= simMaxHops:
			step.Error = "too many group links in a row"
		}
		path = append(path, step)
		if step.Error != "" {
			break
		}
		grp = next
	}
	writeJSON(w, http.StatusOK, map[string]any{"visitor": vi, "unique": v.Unique, "lang": lang, "path": path})
}

// --- cron ---

// cronState is what the panel shows on the Automation page.
type cronState struct {
	Available bool        `json:"available"` // the admin knows where the cron config is
	Alive     bool        `json:"alive"`     // the service wrote a heartbeat recently
	Config    cron.Config `json:"config"`    // secrets masked
	Status    cron.Status `json:"status"`
	Jobs      []string    `json:"jobs"`
	GeoPaths  struct {
		City bool `json:"city"`
		ASN  bool `json:"asn"`
	} `json:"geo_paths"`
}

// cronAliveWindow — the service ticks every few seconds; a heartbeat older
// than this means it is not running.
const cronAliveWindow = 30 * time.Second

func (s *Server) handleCronGet(w http.ResponseWriter, r *http.Request) {
	st := cronState{Jobs: cron.Jobs, Config: cron.Default().Redacted(), Status: cron.Status{Jobs: map[string]cron.JobStatus{}}}
	st.GeoPaths.City, st.GeoPaths.ASN = s.cfg.GeoCityPath != "", s.cfg.GeoASNPath != ""
	if s.cfg.CronFile == "" {
		writeJSON(w, http.StatusOK, st)
		return
	}
	st.Available = true
	cfg, err := cron.Load(s.cfg.CronFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cron config unreadable")
		return
	}
	st.Config = cfg.Redacted()
	if status, err := cron.ReadStatus(cron.StatusPath(s.cfg.CronFile)); err == nil {
		st.Status = status
		st.Alive = time.Since(status.Heartbeat) < cronAliveWindow
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleCronSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CronFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "cron is not configured (KUZTDS_CRON_FILE)")
		return
	}
	var cfg cron.Config
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	stored, err := cron.Load(s.cfg.CronFile)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "cron config unreadable")
		return
	}
	cfg = cfg.KeepSecrets(stored)
	if err := cfg.Validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := cron.Save(s.cfg.CronFile, cfg); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

func (s *Server) handleCronRun(w http.ResponseWriter, r *http.Request) {
	if s.cfg.CronFile == "" {
		writeErr(w, http.StatusServiceUnavailable, "cron is not configured (KUZTDS_CRON_FILE)")
		return
	}
	var body struct {
		Job string `json:"job"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := cron.RequestRun(s.cfg.CronFile, body.Job); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "requested"})
}
