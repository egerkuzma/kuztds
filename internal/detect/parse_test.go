package detect

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		ua      string
		os      string
		browser string
		brand   string
		device  string
	}{
		{
			name: "iPhone Safari",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 16_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.0 Mobile/15E148 Safari/604.1",
			os:   "iOS", browser: "Safari", brand: "Apple", device: Phone,
		},
		{
			name: "Samsung Galaxy Chrome",
			ua:   "Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Mobile Safari/537.36",
			os:   "Android", browser: "Chrome", brand: "Samsung", device: Phone,
		},
		{
			name: "Xiaomi Redmi",
			ua:   "Mozilla/5.0 (Linux; Android 12; Redmi Note 10) AppleWebKit/537.36 Chrome/119.0 Mobile Safari/537.36",
			os:   "Android", browser: "Chrome", brand: "Xiaomi", device: Phone,
		},
		{
			name: "Windows Firefox desktop",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:120.0) Gecko/20100101 Firefox/120.0",
			os:   "Windows", browser: "Firefox", brand: "", device: Computer,
		},
		{
			name: "Pixel tablet-less",
			ua:   "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 Chrome/121.0 Mobile Safari/537.36",
			os:   "Android", browser: "Chrome", brand: "Google", device: Phone,
		},
	}
	for _, c := range cases {
		got := Parse(c.ua)
		if got.OS != c.os {
			t.Errorf("%s: OS=%q want %q", c.name, got.OS, c.os)
		}
		if got.Browser != c.browser {
			t.Errorf("%s: Browser=%q want %q", c.name, got.Browser, c.browser)
		}
		if got.Brand != c.brand {
			t.Errorf("%s: Brand=%q want %q", c.name, got.Brand, c.brand)
		}
		if got.Device != c.device {
			t.Errorf("%s: Device=%q want %q", c.name, got.Device, c.device)
		}
		if got.OSVersion == "" && (c.os == "iOS" || c.os == "Android") {
			t.Errorf("%s: empty OS version", c.name)
		}
	}
}
