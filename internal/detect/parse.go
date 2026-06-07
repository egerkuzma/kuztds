package detect

import (
	"strings"

	ua "github.com/mileusna/useragent"
)

// Info holds the parsed client characteristics for filters and logs.
type Info struct {
	OS         string // Android, iOS, Windows, macOS, Linux, ...
	OSVersion  string // 13, 16.0, 10, ...
	Browser    string // Chrome, Safari, Firefox, YandexBrowser, ...
	BrowserVer string
	Brand      string // Apple, Samsung, Xiaomi, ... ("" for desktop/unknown)
	Device     string // computer/phone/tablet (category)
}

// Parse parses the User-Agent: OS/browser/versions — via mileusna/useragent,
// device category — via our Device(),
// brand — via the brandOf() heuristic.
func Parse(uaStr string) Info {
	p := ua.Parse(uaStr)
	return Info{
		OS:         p.OS,
		OSVersion:  p.OSVersion,
		Browser:    p.Name,
		BrowserVer: p.Version,
		Brand:      brandOf(uaStr, p.OS),
		Device:     Device(uaStr),
	}
}

// brandPatterns maps UA tokens → brand (order matters: specific before general).
var brandPatterns = []struct{ token, brand string }{
	{"honor", "Honor"},
	{"huawei", "Huawei"},
	{"redmi", "Xiaomi"}, {"poco", "Xiaomi"}, {"xiaomi", "Xiaomi"},
	{"oneplus", "OnePlus"},
	{"realme", "Realme"}, {"rmx", "Realme"},
	{"oppo", "Oppo"}, {"cph", "Oppo"},
	{"vivo", "Vivo"},
	{"pixel", "Google"},
	{"nokia", "Nokia"},
	{"xperia", "Sony"}, {"sony", "Sony"},
	{"motorola", "Motorola"}, {"moto ", "Motorola"},
	{"samsung", "Samsung"}, {"sm-", "Samsung"}, {"gt-", "Samsung"}, {"galaxy", "Samsung"},
	{"lg-", "LG"}, {"lm-", "LG"}, {"nexus", "Google"},
}

// brandOf determines the device brand. Apple — by OS (iOS/macOS) and UA.
func brandOf(uaStr, os string) string {
	l := strings.ToLower(uaStr)
	if os == "iOS" || os == "macOS" || strings.Contains(l, "iphone") ||
		strings.Contains(l, "ipad") || strings.Contains(l, "ipod") || strings.Contains(l, "macintosh") {
		return "Apple"
	}
	for _, p := range brandPatterns {
		if strings.Contains(l, p.token) {
			return p.brand
		}
	}
	return ""
}
