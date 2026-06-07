// Package detect — device and bot detection. Pure logic without external
// dependencies; signature
// lists (signature_ua/ref, ua_blacklist) are loaded from .dat with hot-reload.
package detect

import "strings"

// Device categories.
const (
	Computer = "computer"
	Phone    = "phone"
	Tablet   = "tablet"
)

// tabletSignals — tablet markers.
var tabletSignals = []string{
	"ipad", "tablet", "kindle", "silk", "playbook", "rim tablet",
	"nexus 7", "nexus 9", "nexus 10", "sm-t", "gt-p", "transformer",
	"xoom", "mediapad", "nook",
}

// phoneSignals — phone markers.
var phoneSignals = []string{
	"iphone", "ipod", "windows phone", "blackberry", "bb10",
	"opera mini", "opera mobi", "iemobile", "webos", "palm",
	"symbian", "fennec", "minimo",
}

// Device classifies the device by User-Agent.
// Priority: tablet > phone > computer.
// Android without "mobile" is treated as a tablet, with "mobile" — as a phone.
func Device(ua string) string {
	if ua == "" {
		return Computer
	}
	s := strings.ToLower(ua)
	if isTablet(s) {
		return Tablet
	}
	if isPhone(s) {
		return Phone
	}
	return Computer
}

func isTablet(s string) bool {
	if containsAny(s, tabletSignals) {
		return true
	}
	// Android tablet: has "android" but no "mobile".
	if strings.Contains(s, "android") && !strings.Contains(s, "mobile") {
		return true
	}
	return false
}

func isPhone(s string) bool {
	if containsAny(s, phoneSignals) {
		return true
	}
	if strings.Contains(s, "android") && strings.Contains(s, "mobile") {
		return true
	}
	// General mobile marker (after the tablet check).
	return strings.Contains(s, "mobile")
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
