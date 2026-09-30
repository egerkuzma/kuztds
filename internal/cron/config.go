// Package cron is the background service of KuzTDS: the periodic chores the
// engine must not do on its hot path and the admin should not do in a request.
//
// Jobs: refresh bot IP lists, refresh the geo databases, check domains with
// VirusTotal, watch free disk space, delete old keyword files, and announce
// conversions — with Telegram as the one notification channel.
//
// The service talks to the rest of the system through files only. Its config
// is a JSON file the admin panel edits; its state is a JSON file the admin
// panel reads; a "run now" request is a line in a third file. What it
// produces are the same .dat, .mmdb and groups files the engine already
// hot-reloads, so nothing here needs a connection to the engine.
package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/atomicfile"
)

// Job names, as used in the config, the status file and "run now" requests.
const (
	JobIPLists     = "ip_lists"
	JobGeoDB       = "geo_db"
	JobVirusTotal  = "virustotal"
	JobDisk        = "disk"
	JobCleanup     = "cleanup"
	JobConversions = "conversions"
	// JobTelegramTest is not scheduled: it exists so the admin panel can ask
	// for a test message.
	JobTelegramTest = "telegram_test"
)

// Jobs lists the scheduled jobs in display order.
var Jobs = []string{JobIPLists, JobGeoDB, JobVirusTotal, JobDisk, JobCleanup, JobConversions}

// Config is the whole cron configuration (one JSON file).
type Config struct {
	Telegram    Telegram    `json:"telegram"`
	IPLists     IPLists     `json:"ip_lists"`
	GeoDB       GeoDB       `json:"geo_db"`
	VirusTotal  VirusTotal  `json:"virustotal"`
	Disk        Disk        `json:"disk"`
	Cleanup     Cleanup     `json:"cleanup"`
	Conversions Conversions `json:"conversions"`
}

// Telegram — where notifications go.
type Telegram struct {
	Token  string `json:"token"`   // bot token (secret)
	ChatID string `json:"chat_id"` // chat, group or channel id
}

// Configured reports whether a message can be sent at all.
func (t Telegram) Configured() bool { return t.Token != "" && t.ChatID != "" }

// IPLists — refresh .dat lists from URLs.
type IPLists struct {
	Enabled      bool       `json:"enabled"`
	EveryMinutes int        `json:"every_minutes"`
	Sources      []IPSource `json:"sources"`
}

// IPSource is one URL.
//
// With Target set, the whole response goes into that list: a plain text list
// (one IP, CIDR or range per line, "# label" lines kept — that is how wap.dat
// names its operators), or the JSON Google and Bing publish for their
// crawlers ({"prefixes":[{"ipv4Prefix":…}]}).
//
// With Target empty, the response is a sectioned text file: "# google",
// "# yandex"… each start a section, and section <name> goes to ip_<name>.
type IPSource struct {
	URL    string `json:"url"`
	Target string `json:"target"` // list name without .dat, or "" for a sectioned file
	Mode   string `json:"mode"`   // "replace" (default) | "merge"
}

// GeoDB — refresh the MaxMind-format databases.
type GeoDB struct {
	Enabled      bool        `json:"enabled"`
	EveryMinutes int         `json:"every_minutes"`
	Sources      []GeoSource `json:"sources"`
}

// GeoSource is one database download. Kind says which file it replaces: the
// paths themselves come from the service's environment (KUZTDS_GEO_DB,
// KUZTDS_ASN_DB), not from this config — the admin panel can change what is
// downloaded, never where it is written.
type GeoSource struct {
	Kind     string `json:"kind"`     // "city" | "asn"
	URL      string `json:"url"`      // .mmdb, .mmdb.gz or .tar.gz
	User     string `json:"user"`     // basic auth (MaxMind: account id)
	Password string `json:"password"` // basic auth (MaxMind: license key; secret)
}

// VirusTotal — check domains the streams send traffic to.
type VirusTotal struct {
	Enabled        bool     `json:"enabled"`
	EveryMinutes   int      `json:"every_minutes"`
	APIKey         string   `json:"api_key"`         // secret
	Domains        []string `json:"domains"`         // checked always
	FromGroups     bool     `json:"from_groups"`     // plus every host found in group/stream outputs
	Threshold      int      `json:"threshold"`       // malicious+suspicious verdicts that count as flagged (min 1)
	DisableStreams bool     `json:"disable_streams"` // switch off streams that send to a flagged domain
}

// Disk — warn when free space runs low.
type Disk struct {
	Enabled        bool     `json:"enabled"`
	EveryMinutes   int      `json:"every_minutes"`
	Paths          []string `json:"paths"`            // empty = the data directory
	MinFreePercent int      `json:"min_free_percent"` // alert below this (default 10)
}

// Cleanup — delete collected keyword files older than KeysDays.
type Cleanup struct {
	Enabled      bool `json:"enabled"`
	EveryMinutes int  `json:"every_minutes"`
	KeysDays     int  `json:"keys_days"`
}

// Conversions — announce new postbacks.
type Conversions struct {
	Enabled      bool   `json:"enabled"`
	EveryMinutes int    `json:"every_minutes"`
	Template     string `json:"template"` // [PROFIT] [GROUP] [STREAM] [COUNTRY] [CITY] [DEVICE] [DOMAIN] [CID]
}

// DefaultConversionTemplate is used when the template is empty.
const DefaultConversionTemplate = "💰 [PROFIT] — [GROUP] / [STREAM] · [COUNTRY] · [DEVICE]"

// Default returns the configuration a fresh installation starts with:
// everything off, intervals and sources filled in so that turning a job on
// is one switch.
func Default() Config {
	return Config{
		IPLists: IPLists{EveryMinutes: 24 * 60, Sources: []IPSource{
			{URL: "https://developers.google.com/static/search/apis/ipranges/googlebot.json", Target: "ip_google", Mode: "replace"},
			{URL: "https://www.bing.com/toolbox/bingbot.json", Target: "ip_bing", Mode: "replace"},
		}},
		GeoDB: GeoDB{EveryMinutes: 3 * 24 * 60, Sources: []GeoSource{
			{Kind: "city", URL: "https://download.maxmind.com/geoip/databases/GeoLite2-City/download?suffix=tar.gz"},
			{Kind: "asn", URL: "https://download.maxmind.com/geoip/databases/GeoLite2-ASN/download?suffix=tar.gz"},
		}},
		VirusTotal:  VirusTotal{EveryMinutes: 12 * 60, FromGroups: true, Threshold: 1},
		Disk:        Disk{EveryMinutes: 10, MinFreePercent: 10},
		Cleanup:     Cleanup{EveryMinutes: 24 * 60, KeysDays: 30},
		Conversions: Conversions{EveryMinutes: 1, Template: DefaultConversionTemplate},
	}
}

// minimum intervals: a typo must not turn a job into a tight loop against
// someone else's API.
var minInterval = map[string]int{
	JobIPLists: 10, JobGeoDB: 60, JobVirusTotal: 30, JobDisk: 1, JobCleanup: 60, JobConversions: 1,
}

// Interval returns the job's period, never below its minimum.
func (c Config) Interval(job string) time.Duration {
	var m int
	switch job {
	case JobIPLists:
		m = c.IPLists.EveryMinutes
	case JobGeoDB:
		m = c.GeoDB.EveryMinutes
	case JobVirusTotal:
		m = c.VirusTotal.EveryMinutes
	case JobDisk:
		m = c.Disk.EveryMinutes
	case JobCleanup:
		m = c.Cleanup.EveryMinutes
	case JobConversions:
		m = c.Conversions.EveryMinutes
	}
	if lo := minInterval[job]; m < lo {
		m = lo
	}
	return time.Duration(m) * time.Minute
}

// Enabled reports whether the job is switched on.
func (c Config) Enabled(job string) bool {
	switch job {
	case JobIPLists:
		return c.IPLists.Enabled
	case JobGeoDB:
		return c.GeoDB.Enabled
	case JobVirusTotal:
		return c.VirusTotal.Enabled
	case JobDisk:
		return c.Disk.Enabled
	case JobCleanup:
		return c.Cleanup.Enabled
	case JobConversions:
		return c.Conversions.Enabled
	}
	return false
}

var listNameRe = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Validate rejects a configuration the service could not run safely. It is
// called by the admin before saving and by the service after loading, so a
// file edited by hand gets the same checks.
func (c Config) Validate() error {
	for i, s := range c.IPLists.Sources {
		if err := httpURL(s.URL); err != nil {
			return fmt.Errorf("ip list source %d: %w", i+1, err)
		}
		if s.Target != "" && !listNameRe.MatchString(s.Target) {
			return fmt.Errorf("ip list source %d: target %q must be a list name like ip_google", i+1, s.Target)
		}
		if s.Mode != "" && s.Mode != "replace" && s.Mode != "merge" {
			return fmt.Errorf("ip list source %d: mode must be replace or merge", i+1)
		}
	}
	for i, s := range c.GeoDB.Sources {
		if s.Kind != "city" && s.Kind != "asn" {
			return fmt.Errorf("geo source %d: kind must be city or asn", i+1)
		}
		if err := httpURL(s.URL); err != nil {
			return fmt.Errorf("geo source %d: %w", i+1, err)
		}
	}
	for _, d := range c.VirusTotal.Domains {
		if !domainRe.MatchString(d) {
			return fmt.Errorf("virustotal: %q is not a domain", d)
		}
	}
	if p := c.Disk.MinFreePercent; p < 0 || p > 99 {
		return errors.New("disk: min free percent must be 0..99")
	}
	if c.Cleanup.KeysDays < 0 {
		return errors.New("cleanup: keys days must not be negative")
	}
	return nil
}

var domainRe = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

func httpURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%q is not an http(s) URL", raw)
	}
	return nil
}

// Load reads the config file. A missing file is not an error: it yields the
// defaults, which is what a fresh installation has.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return Config{}, err
	}
	c := Default()
	// Slices in the file replace the default ones wholesale (that is how
	// encoding/json treats them), so removing a default source sticks.
	if err := json.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("cron config %s: %w", path, err)
	}
	return c, nil
}

// Save writes the config file atomically, readable by the owner only: it
// holds a bot token and API keys.
func Save(path string, c Config) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b, 0o600)
}

// SecretMask stands in for a stored secret in what the admin API returns.
// Sending it back unchanged means "keep what is stored".
const SecretMask = "********"

func mask(s string) string {
	if s == "" {
		return ""
	}
	return SecretMask
}

// Redacted returns a copy with every secret replaced by SecretMask.
func (c Config) Redacted() Config {
	c.Telegram.Token = mask(c.Telegram.Token)
	c.VirusTotal.APIKey = mask(c.VirusTotal.APIKey)
	src := make([]GeoSource, len(c.GeoDB.Sources))
	for i, s := range c.GeoDB.Sources {
		s.Password = mask(s.Password)
		src[i] = s
	}
	c.GeoDB.Sources = src
	return c
}

// KeepSecrets fills every secret the client sent back as SecretMask from the
// stored config. A geo source is matched by kind and URL, not by position:
// the client may have reordered or removed rows, and handing one source's
// license key to another URL would send it to the wrong host.
func (c Config) KeepSecrets(stored Config) Config {
	if c.Telegram.Token == SecretMask {
		c.Telegram.Token = stored.Telegram.Token
	}
	if c.VirusTotal.APIKey == SecretMask {
		c.VirusTotal.APIKey = stored.VirusTotal.APIKey
	}
	src := make([]GeoSource, len(c.GeoDB.Sources))
	for i, s := range c.GeoDB.Sources {
		if s.Password == SecretMask {
			s.Password = ""
			for _, old := range stored.GeoDB.Sources {
				if old.Kind == s.Kind && old.URL == s.URL {
					s.Password = old.Password
					break
				}
			}
		}
		src[i] = s
	}
	c.GeoDB.Sources = src
	return c
}
