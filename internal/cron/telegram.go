package cron

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// notify sends a Telegram message. The bot token is part of the request URL,
// so errors are rebuilt without it: net/http puts the full URL into its error
// text, and that text ends up in the log and in the status file the admin
// panel shows.
func (r *Runner) notify(ctx context.Context, t Telegram, text string) error {
	if !t.Configured() {
		return errors.New("telegram is not configured")
	}
	if rs := []rune(text); len(rs) > 4000 { // the API limit is 4096 characters
		text = string(rs[:4000]) + "…"
	}
	form := url.Values{"chat_id": {t.ChatID}, "text": {text}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		r.telegramBase+"/bot"+t.Token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return errors.New("telegram: bad request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("telegram: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// notifyIfConfigured is for alerts that are worth sending but must not fail
// the job that raised them. It returns a suffix for the job's message.
func (r *Runner) notifyIfConfigured(ctx context.Context, t Telegram, text string) string {
	if !t.Configured() {
		return " (telegram not configured, nobody was told)"
	}
	if err := r.notify(ctx, t, text); err != nil {
		r.log.Warn("cron: notification not sent", "err", err)
		return " (notification failed: " + err.Error() + ")"
	}
	return ""
}
