package detect

import "strings"

// Bot labels.
const (
	BotNone        = ""
	BotBaidu       = "baidu"
	BotBing        = "bing"
	BotGoogle      = "google"
	BotMail        = "mail"
	BotYahoo       = "yahoo"
	BotYandex      = "yandex"
	BotSignUA      = "sign_ua"
	BotSignRef     = "sign_ref"
	BotUABlacklist = "ua_blacklist"
)

// searchEngineByUA matches known search engines by a substring of the UA.
func searchEngineByUA(l string) string {
	switch {
	case strings.Contains(l, "baidu"):
		return BotBaidu
	case strings.Contains(l, "bing"), strings.Contains(l, "msnbot"):
		return BotBing
	case strings.Contains(l, "google"):
		return BotGoogle
	case strings.Contains(l, "mail.ru"):
		return BotMail
	case strings.Contains(l, "yahoo"):
		return BotYahoo
	case strings.Contains(l, "yandex.com/bots"):
		return BotYandex
	}
	return BotNone
}

// BotByUA: first known search engines by UA, then signatures → sign_ua.
// sigs is the contents of signature_ua.dat. An empty UA is not considered a bot by UA.
func BotByUA(ua string, sigs []string) string {
	if ua == "" {
		return BotNone
	}
	l := strings.ToLower(ua)
	if se := searchEngineByUA(l); se != BotNone {
		return se
	}
	for _, s := range sigs {
		if s != "" && strings.Contains(l, strings.ToLower(s)) {
			return BotSignUA
		}
	}
	return BotNone
}

// BotByReferer: a substring from signature_ref.dat in the referer → sign_ref.
// sigs is the contents of signature_ref.dat. An empty referer is not checked by the caller.
func BotByReferer(referer string, sigs []string) string {
	if referer == "" {
		return BotNone
	}
	l := strings.ToLower(referer)
	for _, s := range sigs {
		if s != "" && strings.Contains(l, strings.ToLower(s)) {
			return BotSignRef
		}
	}
	return BotNone
}

// BotByPTR classifies a bot by reverse DNS (PTR).
func BotByPTR(ptr string) string {
	if ptr == "" {
		return BotNone
	}
	l := strings.ToLower(ptr)
	switch {
	case strings.Contains(l, "baidu"):
		return BotBaidu
	case strings.Contains(l, "bing"), strings.Contains(l, "msnbot"):
		return BotBing
	case strings.Contains(l, "google"):
		return BotGoogle
	case strings.Contains(l, "mail.ru"):
		return BotMail
	case strings.Contains(l, "yahoo"):
		return BotYahoo
	case strings.Contains(l, "yandex"):
		return BotYandex
	}
	return BotNone
}

// InUABlacklist: exact match of the UA against a line from ua_blacklist.dat.
func InUABlacklist(ua string, list []string) bool {
	for _, s := range list {
		if ua == s {
			return true
		}
	}
	return false
}
