// Command apiclient — a TDS client to install on a landing/donor site
// .
//
// Collects visitor data, sends it to the engine (?api=base64(JSON)),
// receives a decision and applies it (redirect/content). The keyword is taken
// from ?q. Configuration is via the KUZTDS_* environment variables.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/server"
)

type apiRequest struct {
	KeyAPI    string   `json:"key_api"`
	ID        string   `json:"id"`
	IP        string   `json:"ip"`
	Referer   string   `json:"referer"`
	UserAgent string   `json:"useragent"`
	SE        string   `json:"se"`
	Lang      string   `json:"lang"`
	Uniq      string   `json:"uniq"`
	Key       string   `json:"key"`
	Domain    string   `json:"domain"`
	Page      string   `json:"page"`
	CFCountry string   `json:"cf_country"`
	Pars      []string `json:"pars"`
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	tdsURL := strings.TrimRight(getenv("KUZTDS_TDS_URL", "http://localhost:8080"), "/")
	apiKey := os.Getenv("KUZTDS_API_KEY")
	groupID := getenv("KUZTDS_GROUP_ID", "promo")
	cookieName := getenv("KUZTDS_COOKIE", "ztu")
	addr := getenv("KUZTDS_APICLIENT_LISTEN", ":9090")

	realIP, err := server.NewRealIP(trustedProxies())
	if err != nil {
		log.Error("realip", "err", err)
		os.Exit(1)
	}
	// http client that does NOT follow redirects (we need the engine's Location).
	// Reuse keep-alive connections to the engine aggressively: one outbound call
	// is made per inbound request, so a small idle pool would churn connections
	// and exhaust ports under load.
	hc := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			MaxIdleConns:        2048,
			MaxIdleConnsPerHost: 2048,
			IdleConnTimeout:     90 * time.Second,
			ForceAttemptHTTP2:   true,
		},
	}

	h := realIP.Middleware(newClientHandler(clientConfig{
		tdsURL: tdsURL, apiKey: apiKey, groupID: groupID, cookieName: cookieName, hc: hc,
	}))

	log.Info("apiclient listening", "addr", addr, "tds", tdsURL, "group", groupID)
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}

// clientConfig — client handler parameters (extracted for testability).
type clientConfig struct {
	tdsURL     string
	apiKey     string
	groupID    string
	cookieName string
	hc         *http.Client
}

// newClientHandler builds the handler: it collects visitor data, sends it to the
// engine (?api=base64(JSON)) and applies the response (redirect/content).
func newClientHandler(cfg clientConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := server.ClientIP(r.Context())

		// uniqueness by client cookie
		uniq := "no"
		if _, err := r.Cookie(cfg.cookieName); err != nil {
			uniq = "yes"
			http.SetCookie(w, &http.Cookie{Name: cfg.cookieName, Value: "1", Path: "/", HttpOnly: true, MaxAge: 86400})
		}
		ref := r.Header.Get("Referer")
		req := apiRequest{
			KeyAPI: cfg.apiKey, ID: cfg.groupID, IP: ip.String(),
			Referer: ref, UserAgent: r.Header.Get("User-Agent"),
			SE: seByReferer(ref), Lang: langOf(r), Uniq: uniq,
			Key: r.URL.Query().Get("q"), Domain: r.Host, Page: r.URL.RequestURI(),
			CFCountry: r.Header.Get("CF-IPCountry"), Pars: extraParams(r),
		}
		blob, _ := json.Marshal(req)
		param := base64.StdEncoding.EncodeToString(blob)

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.tdsURL+"/?api="+url.QueryEscape(param), nil)
		resp, err := cfg.hc.Do(hreq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// The engine returned a redirect — pass the Location through.
		if loc := resp.Header.Get("Location"); loc != "" {
			http.Redirect(w, r, loc, http.StatusFound)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		// api-type response: JSON {out,...} → redirect to out.
		var dec struct {
			Out string `json:"out"`
		}
		if json.Unmarshal(body, &dec) == nil && dec.Out != "" {
			http.Redirect(w, r, dec.Out, http.StatusFound)
			return
		}
		// Otherwise — serve the content as is.
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
	}
}

func seByReferer(ref string) string {
	if ref == "" {
		return "-"
	}
	u, err := url.Parse(ref)
	if err != nil {
		return "-"
	}
	h := strings.ToLower(u.Host)
	for _, se := range []string{"google", "yandex", "mail", "yahoo", "bing"} {
		if strings.Contains(h, se) {
			return se
		}
	}
	return "-"
}

func langOf(r *http.Request) string {
	if al := r.Header.Get("Accept-Language"); len(al) >= 2 {
		return strings.ToLower(al[:2])
	}
	return "-"
}

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

func trustedProxies() []string {
	if v := os.Getenv("KUZTDS_TRUSTED_PROXIES"); v != "" {
		return strings.Split(v, ",")
	}
	return []string{"127.0.0.1/32", "::1/128"}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
