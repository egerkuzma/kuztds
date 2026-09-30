package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/egerkuzma/kuztds/internal/atomicfile"
	"github.com/egerkuzma/kuztds/internal/config"
)

// runVirusTotal asks VirusTotal about every domain the streams send traffic
// to. A domain that antivirus vendors flag is a domain browsers are about to
// warn visitors about: the operator is told once, and — if configured — the
// streams pointing at it are switched off.
func (r *Runner) runVirusTotal(ctx context.Context, cfg Config) (string, error) {
	vt := cfg.VirusTotal
	if vt.APIKey == "" {
		return "", errors.New("no VirusTotal API key")
	}
	threshold := vt.Threshold
	if threshold < 1 {
		threshold = 1
	}

	var groups []config.Group
	if r.paths.Groups != "" {
		if b, err := os.ReadFile(r.paths.Groups); err == nil {
			if err := json.Unmarshal(b, &groups); err != nil {
				return "", fmt.Errorf("groups file: %w", err)
			}
		}
	}
	set := map[string]bool{}
	for _, d := range vt.Domains {
		set[strings.ToLower(d)] = true
	}
	if vt.FromGroups {
		for _, g := range groups {
			for _, d := range hostsIn(g.Out) {
				set[d] = true
			}
			for _, s := range g.Streams {
				for _, d := range hostsIn(s.Out.Out, s.Bots.Out, s.Remote.URL) {
					set[d] = true
				}
			}
		}
	}
	domains := make([]string, 0, len(set))
	for d := range set {
		domains = append(domains, d)
	}
	sort.Strings(domains)
	if len(domains) == 0 {
		return "no domains to check", nil
	}

	flagged := map[string]int{}
	failed := map[string]bool{}
	var errs []error
	for i, d := range domains {
		if i > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(r.vtPause):
			}
		}
		n, err := r.vtVerdicts(ctx, vt.APIKey, d)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", d, err))
			failed[d] = true
			continue
		}
		if n >= threshold {
			flagged[d] = n
		}
	}

	// Tell about domains that are newly flagged; stay quiet about the ones
	// already reported.
	var prev map[string]int
	r.state(func(st *Status) { prev = st.Flagged })
	var fresh []string
	for d, n := range flagged {
		if _, was := prev[d]; !was {
			fresh = append(fresh, fmt.Sprintf("%s (%d)", d, n))
		}
	}
	sort.Strings(fresh)

	msg := fmt.Sprintf("%d domain(s) checked, %d flagged", len(domains)-len(failed), len(flagged))
	if len(flagged) > 0 {
		names := make([]string, 0, len(flagged))
		for d, n := range flagged {
			names = append(names, fmt.Sprintf("%s (%d)", d, n))
		}
		sort.Strings(names)
		msg += ": " + strings.Join(names, ", ")
	}

	var disabled []string
	if vt.DisableStreams && len(flagged) > 0 {
		var err error
		if disabled, err = r.disableStreams(flagged); err != nil {
			errs = append(errs, fmt.Errorf("disabling streams: %w", err))
		} else if len(disabled) > 0 {
			msg += "; switched off: " + strings.Join(disabled, ", ")
		}
	}
	if len(fresh) > 0 {
		text := "🛑 KuzTDS: VirusTotal flags " + strings.Join(fresh, ", ") + "."
		if len(disabled) > 0 {
			text += "\nStreams switched off: " + strings.Join(disabled, ", ") + "."
		}
		msg += r.notifyIfConfigured(ctx, cfg.Telegram, text)
	}
	// A domain whose check failed keeps its previous state: an API hiccup
	// must not make it look clean and re-alert on the next run.
	r.state(func(st *Status) {
		next := map[string]int{}
		for d, n := range flagged {
			next[d] = n
		}
		for d, n := range st.Flagged {
			if failed[d] {
				next[d] = n
			}
		}
		st.Flagged = next
		if len(disabled) > 0 {
			st.DisabledByVT = disabled
		}
	})
	return msg, errors.Join(errs...)
}

// vtVerdicts returns how many engines call the domain malicious or suspicious.
func (r *Runner) vtVerdicts(ctx context.Context, key, domain string) (int, error) {
	body, err := r.get(ctx, r.vtBase+"/api/v3/domains/"+url.PathEscape(domain),
		map[string]string{"x-apikey": key}, "", "", 4<<20)
	if err != nil {
		return 0, err
	}
	defer body.Close()
	var doc struct {
		Data struct {
			Attributes struct {
				Stats struct {
					Malicious  int `json:"malicious"`
					Suspicious int `json:"suspicious"`
				} `json:"last_analysis_stats"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.NewDecoder(body).Decode(&doc); err != nil {
		return 0, errors.New("unexpected response")
	}
	s := doc.Data.Attributes.Stats
	return s.Malicious + s.Suspicious, nil
}

var hostRe = regexp.MustCompile(`(?i)https?://([a-z0-9][a-z0-9.-]*\.[a-z]{2,})`)

// hostsIn finds the hosts of the URLs written in stream outputs. A host built
// from a macro ("https://[PAR-1].example") does not match and is skipped.
func hostsIn(texts ...string) []string {
	var out []string
	for _, t := range texts {
		for _, m := range hostRe.FindAllStringSubmatch(t, -1) {
			out = append(out, strings.ToLower(m[1]))
		}
	}
	return out
}

// disableStreams switches off every active stream whose output mentions a
// flagged domain and saves the groups file. The engine picks the change up on
// its reload interval.
//
// The file is read again here rather than reused from the start of the run:
// a pass over many domains takes minutes, and writing back the copy from
// before it would undo whatever the operator saved in the meantime.
func (r *Runner) disableStreams(flagged map[string]int) ([]string, error) {
	b, err := os.ReadFile(r.paths.Groups)
	if err != nil {
		return nil, err
	}
	var groups []config.Group
	if err := json.Unmarshal(b, &groups); err != nil {
		return nil, err
	}
	var off []string
	for gi := range groups {
		for si := range groups[gi].Streams {
			s := &groups[gi].Streams[si]
			if !s.Status {
				continue
			}
			for _, d := range hostsIn(s.Out.Out) {
				if _, bad := flagged[d]; bad {
					s.Status = false
					off = append(off, groups[gi].ID+"/"+s.Name)
					break
				}
			}
		}
	}
	if len(off) == 0 {
		return nil, nil
	}
	if b, err = json.MarshalIndent(groups, "", "  "); err != nil {
		return nil, err
	}
	return off, atomicfile.Write(r.paths.Groups, b, 0o644)
}
