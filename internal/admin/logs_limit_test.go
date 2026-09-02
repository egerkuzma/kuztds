package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

// recordingStats captures the last filter Logs was called with.
type recordingStats struct {
	fakeStats
	last store.LogFilter
}

func (r *recordingStats) Logs(_ context.Context, f store.LogFilter) ([]store.LogRow, int64, error) {
	r.last = f
	return []store.LogRow{{IP: "1.2.3.4"}}, 1, nil
}

func serverWithStats(t *testing.T, st StatsProvider) (string, *http.Client) {
	t.Helper()
	hash, _ := security.HashPassword("p@ss")
	s, err := New(Config{
		AdminUser: "admin", PasswordHash: hash,
		Sessions: security.NewMemoryStore(), Stats: st,
		Limiter: allowAll{}, SessionTTL: time.Hour, CookieSecure: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	c := newClient(t)
	if code, _ := login(t, c, srv.URL, "admin", "p@ss"); code != http.StatusOK {
		t.Fatalf("login → %d", code)
	}
	return srv.URL, c
}

// The interactive endpoint is paginated: a caller must not be able to pull the
// whole table into one JSON response by asking for a huge limit.
func TestLogsLimitIsClamped(t *testing.T) {
	st := &recordingStats{}
	base, c := serverWithStats(t, st)

	resp, err := c.Get(base + "/api/logs?limit=999999")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.last.Limit > maxPageRows {
		t.Errorf("/api/logs limit = %d, must be clamped to %d", st.last.Limit, maxPageRows)
	}
	if st.last.Limit <= 0 {
		t.Errorf("/api/logs limit = %d, must stay positive", st.last.Limit)
	}
}

// The CSV export deliberately asks for far more than a page — that request must
// reach the store intact, otherwise the export silently returns one page.
func TestLogsExportAsksForFullPeriod(t *testing.T) {
	st := &recordingStats{}
	base, c := serverWithStats(t, st)

	resp, err := c.Get(base + "/api/logs/export")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.last.Limit != exportRows {
		t.Errorf("export limit = %d, want %d", st.last.Limit, exportRows)
	}
	if st.last.Offset != 0 {
		t.Errorf("export offset = %d, want 0", st.last.Offset)
	}
}
