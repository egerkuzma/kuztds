package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// appendKey appends a keyword to keysDir/<group>/<date>[-se].dat
// (/save_keys_se()).
func appendKey(keysDir, group, value string, se bool) {
	if keysDir == "" || group == "" || value == "" {
		return
	}
	dir := filepath.Join(keysDir, group)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := time.Now().Format("2006-01-02")
	if se {
		name += "-se"
	}
	f, err := os.OpenFile(filepath.Join(dir, name+".dat"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(value + "\n")
	_ = f.Close()
}

// parseSEKeyword extracts the search query from the referer.
func parseSEKeyword(referer string) string {
	if referer == "" || referer == "-" {
		return ""
	}
	u, err := url.Parse(referer)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	q := u.Query()
	switch {
	case strings.Contains(host, "yandex"):
		return strings.TrimSpace(q.Get("text"))
	case strings.Contains(host, "rambler"), strings.Contains(host, "tut.by"):
		return strings.TrimSpace(q.Get("query"))
	case strings.Contains(host, "yahoo"):
		return strings.TrimSpace(q.Get("p"))
	case strings.Contains(host, "google"), strings.Contains(host, "mail.ru"), strings.Contains(host, "bing"):
		return strings.TrimSpace(q.Get("q"))
	}
	return ""
}
