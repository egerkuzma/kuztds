// Package admin — KuzTDS management REST API (auth, statistics, groups, lists).
//
// Fixes the problems of the old admin panel: argon2id instead of MD5, server-side
// sessions in a store instead of an md5(ip+pass) cookie, CSRF tokens on mutations,
// login rate-limit without a blocking sleep, parameterized queries to ClickHouse,
// a safe .dat editor without path traversal.
package admin

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/config"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

// StatsProvider — source of statistics (implemented by store.CH).
type StatsProvider interface {
	Summary(ctx context.Context, from, to time.Time) (store.StatsSummary, error)
	TimeSeries(ctx context.Context, from, to time.Time, stepSec int) ([]store.TSPoint, error)
	Breakdown(ctx context.Context, from, to time.Time, dim string, limit int) ([]store.KV, error)
	Logs(ctx context.Context, f store.LogFilter) ([]store.LogRow, int64, error)
	Postbacks(ctx context.Context, from, to time.Time, group string, limit int) ([]store.PostbackRow, float64, error)
	DeleteGroupLogs(ctx context.Context, group string) error
}

// KeysReader reads the collected keywords.
type KeysReader interface {
	Read(group, date string, se bool) (string, error)
}

// GroupsStore — read and save the groups configuration.
type GroupsStore interface {
	List(ctx context.Context) ([]config.Group, error)
	Save(ctx context.Context, groups []config.Group) error
}

// ListsStore — management of .dat lists (IP/WAP operators/signatures).
type ListsStore interface {
	Names() ([]string, error)
	Read(name string) (string, error)
	Write(name, content string) error
}

// LoginLimiter limits the rate of login attempts per key (IP).
type LoginLimiter interface {
	Allow(ctx context.Context, key string) bool
}

// Config — dependencies and settings of the admin server.
type Config struct {
	AdminUser    string
	PasswordHash string // argon2id
	Sessions     security.SessionStore
	Stats        StatsProvider
	Groups       GroupsStore
	Lists        ListsStore
	Keys         KeysReader
	Limiter      LoginLimiter
	SessionTTL   time.Duration
	CookieSecure bool
	EngineURL    string // base URL of the engine for building group links
	PasswordFile string // file with the argon2id hash (overrides PasswordHash, written on change)
	Log          slogLogger
}

// slogLogger — minimal logger interface (to avoid pulling in a hard dependency).
type slogLogger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}

// Server — admin API.
type Server struct {
	cfg    Config
	mu     sync.RWMutex
	pwHash string // current password hash (may change at runtime)
}

const cookieName = "kuztds_admin"

// New creates an admin server.
func New(cfg Config) *Server {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	s := &Server{cfg: cfg, pwHash: cfg.PasswordHash}
	// The hash file takes priority (and is also written on password change).
	if cfg.PasswordFile != "" {
		if b, err := os.ReadFile(cfg.PasswordFile); err == nil {
			if h := strings.TrimSpace(string(b)); h != "" {
				s.pwHash = h
			}
		}
	}
	return s
}

func (s *Server) currentHash() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pwHash
}

// Handler returns an http.Handler with all routes and middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.Handle("POST /api/logout", s.auth(http.HandlerFunc(s.handleLogout)))
	mux.Handle("GET /api/me", s.auth(http.HandlerFunc(s.handleMe)))
	mux.Handle("POST /api/password", s.auth(http.HandlerFunc(s.handlePassword)))

	mux.Handle("GET /api/stats/summary", s.auth(http.HandlerFunc(s.handleSummary)))
	mux.Handle("GET /api/stats/timeseries", s.auth(http.HandlerFunc(s.handleTimeSeries)))
	mux.Handle("GET /api/stats/breakdown", s.auth(http.HandlerFunc(s.handleBreakdown)))
	mux.Handle("GET /api/logs", s.auth(http.HandlerFunc(s.handleLogs)))
	mux.Handle("GET /api/logs/filters", s.auth(http.HandlerFunc(s.handleLogFilters)))
	mux.Handle("GET /api/logs/export", s.auth(http.HandlerFunc(s.handleLogsExport)))
	mux.Handle("DELETE /api/logs", s.auth(http.HandlerFunc(s.handleDeleteLogs)))
	mux.Handle("GET /api/postbacks", s.auth(http.HandlerFunc(s.handlePostbacks)))
	mux.Handle("GET /api/keys", s.auth(http.HandlerFunc(s.handleKeys)))

	mux.Handle("GET /api/groups", s.auth(http.HandlerFunc(s.handleGroupsGet)))
	mux.Handle("PUT /api/groups", s.auth(http.HandlerFunc(s.handleGroupsSave)))

	mux.Handle("GET /api/lists", s.auth(http.HandlerFunc(s.handleListsIndex)))
	mux.Handle("GET /api/lists/{name}", s.auth(http.HandlerFunc(s.handleListRead)))
	mux.Handle("PUT /api/lists/{name}", s.auth(http.HandlerFunc(s.handleListWrite)))

	// Embedded web interface (SPA).
	mux.HandleFunc("GET /{$}", serveUI)
	return securityHeaders(mux)
}

// --- auth ---

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if s.cfg.Limiter != nil && !s.cfg.Limiter.Allow(r.Context(), ip) {
		writeErr(w, http.StatusTooManyRequests, "too many attempts")
		return
	}
	var body struct{ Login, Password string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if !security.EqualTokens(body.Login, s.cfg.AdminUser) || !security.VerifyPassword(body.Password, s.currentHash()) {
		s.logWarn("admin login failed", "ip", ip, "login", body.Login)
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	csrf, err := s.issueSession(w, r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"csrf": csrf})
}

// issueSession creates a fresh session bound to the current password, sets the
// cookie and returns the new CSRF token.
func (s *Server) issueSession(w http.ResponseWriter, r *http.Request) (string, error) {
	token, err1 := security.RandomToken()
	csrf, err2 := security.RandomToken()
	if err1 != nil {
		return "", err1
	}
	if err2 != nil {
		return "", err2
	}
	sess := security.Session{
		User:    s.cfg.AdminUser,
		CSRF:    csrf,
		Created: time.Now(),
		PwFP:    security.PasswordFingerprint(s.currentHash()),
	}
	if err := s.cfg.Sessions.Create(r.Context(), token, sess, s.cfg.SessionTTL); err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: token, Path: "/",
		HttpOnly: true, Secure: s.cfg.CookieSecure, SameSite: http.SameSiteStrictMode,
		Expires: time.Now().Add(s.cfg.SessionTTL),
	})
	return csrf, nil
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.cfg.Sessions.Delete(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	sess := sessionFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"user": sess.User, "csrf": sess.CSRF, "engine": s.cfg.EngineURL})
}

// handlePassword changes the administrator password (argon2id), saving the hash to a file.
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var body struct{ Old, New string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}
	if !security.VerifyPassword(body.Old, s.currentHash()) {
		writeErr(w, http.StatusUnauthorized, "incorrect current password")
		return
	}
	if len(body.New) < 6 {
		writeErr(w, http.StatusBadRequest, "password too short (min. 6)")
		return
	}
	hash, err := security.HashPassword(body.New)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Persist first, switch in memory second: a failed write must not leave the
	// process running on a password that survives no restart.
	if s.cfg.PasswordFile != "" {
		if err := os.WriteFile(s.cfg.PasswordFile, []byte(hash), 0o600); err != nil {
			s.logWarn("password file write failed", "err", err)
			writeErr(w, http.StatusInternalServerError, "failed to save (check KUZTDS_ADMIN_PASSWORD_FILE)")
			return
		}
	}
	s.mu.Lock()
	s.pwHash = hash
	s.mu.Unlock()

	// Every session issued under the old password is now invalid, including
	// this one — hand the caller a fresh cookie and CSRF token so the panel
	// keeps working, and drop the old token from the store.
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.cfg.Sessions.Delete(r.Context(), c.Value)
	}
	csrf, err := s.issueSession(w, r)
	if err != nil {
		s.logWarn("session reissue after password change failed", "err", err)
		writeJSON(w, http.StatusOK, map[string]string{"status": "changed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "changed", "csrf": csrf})
}

// --- stats ---

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	from, to := parseRange(r)
	sum, err := s.cfg.Stats.Summary(r.Context(), from, to)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stats error")
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleTimeSeries(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	from, to := parseRange(r)
	step := atoiDef(r.URL.Query().Get("step"), 3600)
	pts, err := s.cfg.Stats.TimeSeries(r.Context(), from, to, step)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "stats error")
		return
	}
	writeJSON(w, http.StatusOK, pts)
}

func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	from, to := parseRange(r)
	dim := r.URL.Query().Get("dim")
	limit := atoiDef(r.URL.Query().Get("limit"), 50)
	kv, err := s.cfg.Stats.Breakdown(r.Context(), from, to, dim, limit)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad dimension")
		return
	}
	writeJSON(w, http.StatusOK, kv)
}

func (s *Server) logFilter(r *http.Request) store.LogFilter {
	q := r.URL.Query()
	from, to := parseRange(r)
	return store.LogFilter{
		From: from, To: to,
		Group: csvVals(q.Get("group")), Stream: csvVals(q.Get("stream")),
		Country: csvVals(q.Get("country")), Device: csvVals(q.Get("device")),
		OS: csvVals(q.Get("os")), Browser: csvVals(q.Get("browser")), Brand: csvVals(q.Get("brand")),
		IP:         q.Get("ip"),
		BotsOnly:   q.Get("bots") == "1",
		HumansOnly: q.Get("humans") == "1",
		Limit:      clampPage(atoiDef(q.Get("limit"), defaultPageRows)),
		Offset:     max(atoiDef(q.Get("offset"), 0), 0),
	}
}

const (
	// defaultPageRows — one page of the log table.
	defaultPageRows = 100
	// maxPageRows bounds the interactive log endpoint. It is paginated, so a
	// caller must not be able to turn one request into a full-table dump by
	// asking for a huge limit; whole-period reads go through the CSV export.
	maxPageRows = 1000
	// exportRows — how many rows the CSV export pulls for the selected period.
	// The store accepts this as-is (see store.maxLogRows).
	exportRows = 50000
)

// clampPage bounds a page size requested by the client.
func clampPage(n int) int {
	if n <= 0 {
		return defaultPageRows
	}
	if n > maxPageRows {
		return maxPageRows
	}
	return n
}

// csvVals splits "a,b,c" into a slice of non-empty trimmed values.
func csvVals(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// handleLogFilters returns the available log filter values for each dimension
// over the period (for the checkbox dropdowns in the UI).
func (s *Server) handleLogFilters(w http.ResponseWriter, r *http.Request) {
	out := map[string][]string{}
	if s.cfg.Stats == nil {
		writeJSON(w, http.StatusOK, out)
		return
	}
	from, to := parseRange(r)
	for _, dim := range []string{"group", "stream", "country", "device", "os", "browser", "brand"} {
		kv, err := s.cfg.Stats.Breakdown(r.Context(), from, to, dim, 1000)
		if err != nil {
			continue
		}
		vals := []string{}
		for _, x := range kv {
			if x.Key != "" && x.Key != "-" {
				vals = append(vals, x.Key)
			}
		}
		out[dim] = vals
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	rows, total, err := s.cfg.Stats.Logs(r.Context(), s.logFilter(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "logs error")
		return
	}
	if rows == nil {
		rows = []store.LogRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "rows": rows})
}

func (s *Server) handleLogsExport(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	f := s.logFilter(r)
	f.Limit, f.Offset = exportRows, 0
	rows, _, err := s.cfg.Stats.Logs(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "logs error")
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=logs.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ts", "group", "stream", "country", "city", "device", "os", "browser", "brand", "bot", "uniq", "ip", "keyword", "out"})
	for _, x := range rows {
		_ = cw.Write([]string{x.TS.Format(time.RFC3339), x.Group, x.Stream, x.Country, x.City, x.Device,
			x.OS, x.Browser, x.Brand, x.Bot, strconv.Itoa(int(x.Uniq)), x.IP, x.Keyword, x.Out})
	}
	cw.Flush()
}

func (s *Server) handleDeleteLogs(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	group := r.URL.Query().Get("group")
	if group == "" {
		writeErr(w, http.StatusBadRequest, "group required")
		return
	}
	if err := s.cfg.Stats.DeleteGroupLogs(r.Context(), group); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handlePostbacks(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Stats == nil {
		writeErr(w, http.StatusServiceUnavailable, "stats unavailable")
		return
	}
	from, to := parseRange(r)
	rows, total, err := s.cfg.Stats.Postbacks(r.Context(), from, to, r.URL.Query().Get("group"), atoiDef(r.URL.Query().Get("limit"), 200))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "postbacks error")
		return
	}
	if rows == nil {
		rows = []store.PostbackRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "rows": rows})
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Keys == nil {
		writeErr(w, http.StatusServiceUnavailable, "keys unavailable")
		return
	}
	q := r.URL.Query()
	content, err := s.cfg.Keys.Read(q.Get("group"), q.Get("date"), q.Get("se") == "1")
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"content": ""})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

// --- groups ---

func (s *Server) handleGroupsGet(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeJSON(w, http.StatusOK, []config.Group{})
		return
	}
	groups, err := s.cfg.Groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "groups error")
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *Server) handleGroupsSave(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "groups store unavailable")
		return
	}
	var groups []config.Group
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&groups); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	seen := map[string]bool{}
	for _, g := range groups {
		if g.ID == "" {
			writeErr(w, http.StatusBadRequest, "group id required")
			return
		}
		if seen[g.ID] {
			writeErr(w, http.StatusBadRequest, "duplicate group id: "+g.ID)
			return
		}
		seen[g.ID] = true
	}
	if err := s.cfg.Groups.Save(r.Context(), groups); err != nil {
		writeErr(w, http.StatusInternalServerError, "save error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// --- lists (.dat editor) ---

func (s *Server) handleListsIndex(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lists == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	names, err := s.cfg.Lists.Names()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lists error")
		return
	}
	writeJSON(w, http.StatusOK, names)
}

func (s *Server) handleListRead(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lists == nil {
		writeErr(w, http.StatusServiceUnavailable, "lists unavailable")
		return
	}
	content, err := s.cfg.Lists.Read(r.PathValue("name"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": r.PathValue("name"), "content": content})
}

func (s *Server) handleListWrite(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Lists == nil {
		writeErr(w, http.StatusServiceUnavailable, "lists unavailable")
		return
	}
	var body struct{ Content string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json")
		return
	}
	if err := s.cfg.Lists.Write(r.PathValue("name"), body.Content); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// --- middleware ---

type ctxKey int

const sessionKey ctxKey = 0

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		sess, ok, err := s.cfg.Sessions.Get(r.Context(), c.Value)
		if err != nil || !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// A session issued under a previous password is dead: changing the
		// password must kick out everyone who holds an older cookie.
		if !security.EqualTokens(sess.PwFP, security.PasswordFingerprint(s.currentHash())) {
			_ = s.cfg.Sessions.Delete(r.Context(), c.Value)
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !isSafeMethod(r.Method) {
			if !security.EqualTokens(r.Header.Get("X-CSRF-Token"), sess.CSRF) {
				writeErr(w, http.StatusForbidden, "csrf")
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey, sess)))
	})
}

func sessionFrom(ctx context.Context) security.Session {
	if s, ok := ctx.Value(sessionKey).(security.Session); ok {
		return s
	}
	return security.Session{}
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// --- helpers ---

func (s *Server) logWarn(msg string, args ...any) {
	if s.cfg.Log != nil {
		s.cfg.Log.Warn(msg, args...)
	}
}

func isSafeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// parseRange reads from/to (RFC3339) from the query; defaults to the last 24 hours.
func parseRange(r *http.Request) (time.Time, time.Time) {
	to := time.Now()
	from := to.Add(-24 * time.Hour)
	if v := r.URL.Query().Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			from = t
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			to = t
		}
	}
	return from, to
}

func atoiDef(s string, def int) int {
	if s == "" {
		return def
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
