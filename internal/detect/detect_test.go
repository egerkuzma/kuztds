package detect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDevice(t *testing.T) {
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) Firefox/120.0", Computer},
		{"", Computer},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) Mobile/15E148", Phone},
		{"Mozilla/5.0 (Linux; Android 13; Pixel 7) AppleWebKit Mobile Safari", Phone},
		{"Mozilla/5.0 (iPad; CPU OS 16_0 like Mac OS X) AppleWebKit", Tablet},
		{"Mozilla/5.0 (Linux; Android 12; SM-T870) AppleWebKit Safari", Tablet}, // Samsung Tab tablet
		{"Mozilla/5.0 (Linux; Android 13) AppleWebKit Safari", Tablet},          // Android without mobile
		{"Mozilla/5.0 (Windows Phone 10.0; Lumia 950)", Phone},
	}
	for _, c := range cases {
		if got := Device(c.ua); got != c.want {
			t.Errorf("Device(%q) = %q; want %q", c.ua, got, c.want)
		}
	}
}

func TestBotByUA(t *testing.T) {
	sigs := []string{"AhrefsBot", "SemrushBot"}
	cases := []struct {
		ua   string
		want string
	}{
		{"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", BotGoogle},
		{"Mozilla/5.0 (compatible; bingbot/2.0; +http://www.bing.com/bingbot.htm)", BotBing},
		{"Mozilla/5.0 (compatible; YandexBot/3.0; +http://yandex.com/bots)", BotYandex},
		{"Mozilla/5.0 (compatible; Baiduspider/2.0)", BotBaidu},
		{"Mozilla/5.0 (compatible; AhrefsBot/7.0)", BotSignUA},
		{"Mozilla/5.0 (Windows NT 10.0) Firefox/120.0", BotNone},
		{"", BotNone},
	}
	for _, c := range cases {
		if got := BotByUA(c.ua, sigs); got != c.want {
			t.Errorf("BotByUA(%q) = %q; want %q", c.ua, got, c.want)
		}
	}
}

func TestBotByPTRAndReferer(t *testing.T) {
	if got := BotByPTR("crawl-66-249-66-1.googlebot.com"); got != BotGoogle {
		t.Errorf("PTR google = %q", got)
	}
	if got := BotByReferer("https://spam-site.example/", []string{"spam-site.example"}); got != BotSignRef {
		t.Errorf("referer sign = %q", got)
	}
	if got := BotByReferer("https://google.com/", []string{"spam-site.example"}); got != BotNone {
		t.Errorf("clean referer = %q", got)
	}
}

func TestInUABlacklist(t *testing.T) {
	list := []string{"BadBot/1.0", "Evil/2.0"}
	if !InUABlacklist("BadBot/1.0", list) {
		t.Error("an exact match should fire")
	}
	if InUABlacklist("BadBot/1.0 extra", list) {
		t.Error("ua_blacklist — exact match only")
	}
}

func TestSignaturesLoadAndReload(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name+".dat")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(fileUA, "# bots\nAhrefsBot\nSemrushBot\n")

	s := NewSignatures(dir, nil)
	if got := len(s.UA()); got != 2 {
		t.Fatalf("expected 2 UA signatures, got %d", got)
	}
	if BotByUA("x AhrefsBot y", s.UA()) != BotSignUA {
		t.Error("AhrefsBot should be caught as sign_ua")
	}
}
