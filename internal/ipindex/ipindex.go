// Package ipindex keeps large lists of IP ranges in memory and queries
// them with binary search in O(log n).
//
// A list is loaded once, normalized into a sorted
// non-overlapping set, and queried with binary search in O(log n).
// The index is immutable; updates are an atomic pointer swap (see Holder),
// so reads on the hot path run without locks.
//
// Supported line formats in .dat:
//
//	1.2.3.4               single IP (v4/v6)
//	1.2.3.0/24            CIDR
//	1.2.3.0-1.2.3.255     range
//	# beeline             label: applies to subsequent lines (wap.dat)
//	                      empty lines are ignored
package ipindex

import (
	"bufio"
	"fmt"
	"io"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync/atomic"
)

type rng struct {
	lo, hi netip.Addr
	label  string
}

// Index is an immutable sorted set of IP ranges.
// v4 and v6 are stored separately so comparisons stay within one family.
type Index struct {
	v4 []rng
	v6 []rng
}

// Lookup returns the label of the range ip belongs to and whether it was a hit.
// For lists without labels, label is empty and ok is true on a hit.
func (idx *Index) Lookup(ip netip.Addr) (label string, ok bool) {
	if idx == nil || !ip.IsValid() {
		return "", false
	}
	ip = ip.Unmap()
	rs := idx.v6
	if ip.Is4() {
		rs = idx.v4
	}
	// Ranges are non-overlapping and sorted by lo, so the only
	// candidate is the last range with lo <= ip.
	i := sort.Search(len(rs), func(i int) bool { return rs[i].lo.Compare(ip) > 0 }) - 1
	if i < 0 {
		return "", false
	}
	if ip.Compare(rs[i].hi) <= 0 {
		return rs[i].label, true
	}
	return "", false
}

// Len returns the number of ranges (for diagnostics/metrics).
func (idx *Index) Len() int {
	if idx == nil {
		return 0
	}
	return len(idx.v4) + len(idx.v6)
}

// Parse reads a list of ranges from an arbitrary source.
func Parse(r io.Reader) (*Index, error) {
	var v4, v6 []rng
	label := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for line := 1; sc.Scan(); line++ {
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "#") {
			label = strings.TrimSpace(strings.TrimPrefix(s, "#"))
			continue
		}
		lo, hi, err := parseRange(s)
		if err != nil {
			// Skip a malformed line, but don't fail loading the whole list.
			continue
		}
		r := rng{lo: lo, hi: hi, label: label}
		if lo.Is4() {
			v4 = append(v4, r)
		} else {
			v6 = append(v6, r)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &Index{v4: normalize(v4), v6: normalize(v6)}, nil
}

// LoadFile loads the index from a .dat file.
func LoadFile(path string) (*Index, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ipindex: open %s: %w", path, err)
	}
	defer f.Close()
	return Parse(f)
}

func parseRange(s string) (lo, hi netip.Addr, err error) {
	switch {
	case strings.ContainsRune(s, '/'):
		p, e := netip.ParsePrefix(s)
		if e != nil {
			return lo, hi, e
		}
		p = p.Masked()
		return p.Addr().Unmap(), lastAddr(p).Unmap(), nil
	case strings.ContainsRune(s, '-'):
		parts := strings.SplitN(s, "-", 2)
		a, e1 := netip.ParseAddr(strings.TrimSpace(parts[0]))
		b, e2 := netip.ParseAddr(strings.TrimSpace(parts[1]))
		if e1 != nil {
			return lo, hi, e1
		}
		if e2 != nil {
			return lo, hi, e2
		}
		a, b = a.Unmap(), b.Unmap()
		if a.Compare(b) > 0 {
			a, b = b, a
		}
		return a, b, nil
	default:
		a, e := netip.ParseAddr(s)
		if e != nil {
			return lo, hi, e
		}
		a = a.Unmap()
		return a, a, nil
	}
}

// lastAddr returns the last address of the prefix (all host bits set to 1).
func lastAddr(p netip.Prefix) netip.Addr {
	b := p.Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		b[i/8] |= 1 << (7 - uint(i%8))
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// normalize sorts ranges and merges overlapping/adjacent ones into a
// non-overlapping set. When ranges with different labels overlap, the
// label of the earlier one in order is kept (in real .dat files, ranges
// within one file don't overlap, so this is safe).
func normalize(in []rng) []rng {
	if len(in) == 0 {
		return in
	}
	sort.Slice(in, func(i, j int) bool {
		if c := in[i].lo.Compare(in[j].lo); c != 0 {
			return c < 0
		}
		return in[i].hi.Compare(in[j].hi) < 0
	})
	out := make([]rng, 0, len(in))
	out = append(out, in[0])
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		next := last.hi.Next()
		adjacent := next.IsValid() && r.lo.Compare(next) == 0
		if r.lo.Compare(last.hi) <= 0 || adjacent {
			if r.hi.Compare(last.hi) > 0 {
				last.hi = r.hi
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// Holder stores the current index and allows atomically swapping it on
// hot-reload without blocking readers on the hot path.
type Holder struct {
	p atomic.Pointer[Index]
}

// NewHolder creates a holder with an initial (possibly empty) index.
func NewHolder(initial *Index) *Holder {
	if initial == nil {
		initial = &Index{}
	}
	h := &Holder{}
	h.p.Store(initial)
	return h
}

// Load returns the current index (safe from any goroutine).
func (h *Holder) Load() *Index { return h.p.Load() }

// Store atomically replaces the index.
func (h *Holder) Store(idx *Index) {
	if idx == nil {
		idx = &Index{}
	}
	h.p.Store(idx)
}

// Lookup is a convenience wrapper over the current index.
func (h *Holder) Lookup(ip netip.Addr) (string, bool) { return h.Load().Lookup(ip) }
