package store

import "testing"

// The interactive log table asks for a page, the CSV export asks for the whole
// period. Both go through Logs, so the clamp must bound absurd values without
// silently shrinking a legitimate export back to one page.
func TestClampLogLimit(t *testing.T) {
	cases := []struct{ in, want int }{
		{0, defaultLogRows},          // unset → one page
		{-1, defaultLogRows},         // nonsense → one page
		{100, 100},                   // a page as asked
		{1000, 1000},                 // a big page as asked
		{maxLogRows, maxLogRows},     // the export limit survives
		{maxLogRows + 1, maxLogRows}, // above the ceiling → the ceiling
		{1 << 30, maxLogRows},        // absurd → the ceiling
	}
	for _, c := range cases {
		if got := clampLogLimit(c.in); got != c.want {
			t.Errorf("clampLogLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}
