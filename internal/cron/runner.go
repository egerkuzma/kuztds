package cron

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egerkuzma/kuztds/internal/atomicfile"
	"github.com/egerkuzma/kuztds/internal/store"
)

// Paths — the files the service reads and writes.
type Paths struct {
	Config  string // cron config (JSON), edited by the admin panel
	DataDir string // .dat lists
	KeysDir string // collected keywords
	Groups  string // groups config (for VirusTotal: hosts to check, streams to disable)
	CityDB  string // target of geo source "city"
	ASNDB   string // target of geo source "asn"
}

// StatusPath is where the service keeps its state, next to the config.
func StatusPath(config string) string { return config + ".status.json" }

// TriggerPath is the "run now" queue: one job name per line.
func TriggerPath(config string) string { return config + ".run" }

// JobStatus is what one job last did.
type JobStatus struct {
	LastRun    time.Time `json:"last_run"`
	OK         bool      `json:"ok"`
	Message    string    `json:"message"`
	DurationMS int64     `json:"duration_ms"`
	NextRun    time.Time `json:"next_run"`
	Running    bool      `json:"running"`
}

// Status is the persisted state of the service.
type Status struct {
	Heartbeat time.Time            `json:"heartbeat"` // the service is alive if this is recent
	Jobs      map[string]JobStatus `json:"jobs"`

	// Memory the jobs need across runs.
	Flagged     map[string]int `json:"flagged,omitempty"`      // virustotal: domain → verdicts when last alerted
	DiskAlerted map[string]int `json:"disk_alerted,omitempty"` // disk: path → free percent when last alerted
	ConvSince   time.Time      `json:"conv_since,omitempty"`   // conversions: announced up to here
	GeoBuilt    map[string]int `json:"geo_built,omitempty"`    // geo: kind → build epoch installed
	// geo: kind → what the source said about the file last downloaded
	GeoSeen      map[string]GeoSeen `json:"geo_seen,omitempty"`
	DisabledByVT []string           `json:"disabled_by_vt,omitempty"` // "group/stream" switched off by the last check
}

// GeoSeen remembers a download well enough to ask "anything newer?" next time.
type GeoSeen struct {
	URL          string `json:"url"`
	LastModified string `json:"last_modified"`
}

// ReadStatus loads the status file; a missing file is an empty status.
func ReadStatus(path string) (Status, error) {
	var st Status
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Status{Jobs: map[string]JobStatus{}}, nil
		}
		return st, err
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return Status{Jobs: map[string]JobStatus{}}, err
	}
	if st.Jobs == nil {
		st.Jobs = map[string]JobStatus{}
	}
	return st, nil
}

// RequestRun asks the running service to execute a job at its next tick.
func RequestRun(config, job string) error {
	if job != JobTelegramTest && !known(job) {
		return fmt.Errorf("unknown job %q", job)
	}
	f, err := os.OpenFile(TriggerPath(config), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(job + "\n")
	return err
}

func known(job string) bool {
	for _, j := range Jobs {
		if j == job {
			return true
		}
	}
	return false
}

// PostbackSource is the part of the statistics store the conversions job uses.
type PostbackSource interface {
	Postbacks(ctx context.Context, from, to time.Time, group string, limit int) ([]store.PostbackRow, float64, error)
}

// Runner executes the jobs on schedule.
type Runner struct {
	paths Paths
	log   *slog.Logger
	http  *http.Client
	pb    PostbackSource // nil: the conversions job reports that it has no store

	// Endpoints and pauses, replaceable in tests.
	telegramBase string
	vtBase       string
	vtPause      time.Duration
	now          func() time.Time
	disk         func(path string) (free, total uint64, err error)

	cfgErr string // the config problem last reported (Tick runs on one goroutine)

	mu      sync.Mutex
	saveMu  sync.Mutex // orders writes of the status file, see save
	st      Status
	running map[string]bool
	wg      sync.WaitGroup
}

// New builds a runner. pb may be nil.
func New(paths Paths, pb PostbackSource, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.Default()
	}
	st, err := ReadStatus(StatusPath(paths.Config))
	if err != nil {
		log.Warn("cron: status file unreadable, starting clean", "err", err)
	}
	// A job cannot have survived the process that was running it.
	for k, j := range st.Jobs {
		j.Running = false
		st.Jobs[k] = j
	}
	return &Runner{
		paths: paths, log: log, pb: pb,
		http:         &http.Client{Timeout: 5 * time.Minute},
		telegramBase: "https://api.telegram.org",
		vtBase:       "https://www.virustotal.com",
		vtPause:      16 * time.Second, // the public API allows 4 requests a minute
		now:          time.Now,
		disk:         diskFree,
		st:           st,
		running:      map[string]bool{},
	}
}

// Run ticks until ctx is done, then waits for the jobs in flight.
func (r *Runner) Run(ctx context.Context, tick time.Duration) {
	if tick <= 0 {
		tick = 5 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	r.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			r.wg.Wait()
			return
		case <-t.C:
			r.Tick(ctx)
		}
	}
}

// Tick starts every job that is due or was requested. Jobs run in their own
// goroutines, one instance of each at a time, so a slow VirusTotal pass does
// not hold up the disk check.
func (r *Runner) Tick(ctx context.Context) {
	cfg, err := Load(r.paths.Config)
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		// Said once per distinct problem, not once per tick.
		if msg := err.Error(); msg != r.cfgErr {
			r.cfgErr = msg
			r.log.Error("cron: config not usable, nothing runs until it is fixed", "err", err)
		}
		r.heartbeat()
		return
	}
	r.cfgErr = ""
	now := r.now()
	requested := r.takeRequests()
	for _, job := range Jobs {
		r.mu.Lock()
		js := r.st.Jobs[job]
		due := cfg.Enabled(job) && !now.Before(js.NextRun)
		busy := r.running[job]
		r.mu.Unlock()
		if busy || !(due || requested[job]) {
			continue
		}
		r.start(ctx, cfg, job)
	}
	if requested[JobTelegramTest] {
		r.start(ctx, cfg, JobTelegramTest)
	}
	r.heartbeat()
}

// Wait blocks until the jobs started so far have finished.
func (r *Runner) Wait() { r.wg.Wait() }

func (r *Runner) takeRequests() map[string]bool {
	out := map[string]bool{}
	// Rename first, read second: a request appended while the file is being
	// read would otherwise be deleted unread.
	p := TriggerPath(r.paths.Config)
	taken := p + ".taken"
	if err := os.Rename(p, taken); err != nil {
		return out
	}
	b, err := os.ReadFile(taken)
	_ = os.Remove(taken)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if j := strings.TrimSpace(line); j != "" {
			out[j] = true
		}
	}
	return out
}

func (r *Runner) start(ctx context.Context, cfg Config, job string) {
	r.mu.Lock()
	if r.running[job] {
		r.mu.Unlock()
		return
	}
	r.running[job] = true
	prev := r.st.Jobs[job]
	js := prev
	js.Running = true
	r.st.Jobs[job] = js
	r.mu.Unlock()
	r.save()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		began := r.now()
		msg, err := r.exec(ctx, cfg, job)
		took := r.now().Sub(began)

		r.mu.Lock()
		// A run cut short by shutdown did not happen as far as the schedule
		// is concerned: recording it would push the next attempt a whole
		// interval away — days, for the geo databases.
		if ctx.Err() != nil {
			r.st.Jobs[job] = prev
			delete(r.running, job)
			r.mu.Unlock()
			r.save()
			r.log.Info("cron: job interrupted by shutdown, it keeps its place in the schedule", "job", job)
			return
		}
		js := JobStatus{LastRun: began, OK: err == nil, Message: msg, DurationMS: took.Milliseconds()}
		if err != nil {
			js.Message = err.Error()
			if msg != "" {
				js.Message = msg + " — " + err.Error()
			}
		}
		// The interval counts from the end of a run. Counted from its start, a
		// job that takes longer than its interval — a VirusTotal pass over many
		// domains — would be due again the moment it finished and would spend
		// the whole day at the API's rate limit.
		if job != JobTelegramTest {
			js.NextRun = r.now().Add(cfg.Interval(job))
		}
		r.st.Jobs[job] = js
		delete(r.running, job)
		r.mu.Unlock()
		r.save()

		if err != nil {
			r.log.Error("cron: job failed", "job", job, "err", err, "took", took.String())
		} else {
			r.log.Info("cron: job done", "job", job, "result", msg, "took", took.String())
		}
	}()
}

func (r *Runner) exec(ctx context.Context, cfg Config, job string) (msg string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	switch job {
	case JobIPLists:
		return r.runIPLists(ctx, cfg.IPLists)
	case JobGeoDB:
		return r.runGeoDB(ctx, cfg.GeoDB)
	case JobVirusTotal:
		return r.runVirusTotal(ctx, cfg)
	case JobDisk:
		return r.runDisk(ctx, cfg)
	case JobCleanup:
		return r.runCleanup(cfg.Cleanup)
	case JobConversions:
		return r.runConversions(ctx, cfg)
	case JobTelegramTest:
		if !cfg.Telegram.Configured() {
			return "", errors.New("telegram is not configured: set the bot token and the chat id")
		}
		return "test message sent", r.notify(ctx, cfg.Telegram, "✅ KuzTDS: Telegram notifications work.")
	}
	return "", fmt.Errorf("unknown job %q", job)
}

func (r *Runner) heartbeat() {
	r.mu.Lock()
	r.st.Heartbeat = r.now()
	r.mu.Unlock()
	r.save()
}

// state runs f with the persisted state locked.
func (r *Runner) state(f func(*Status)) {
	r.mu.Lock()
	f(&r.st)
	r.mu.Unlock()
}

// save writes the state to the status file. Snapshot and write happen under
// one lock of their own: the ticker's heartbeat and a finishing job both save,
// and without it the one that took its snapshot first could rename last,
// leaving the file a step behind the state — "still running" on a finished
// job, or, across a restart, a job that runs again because its next-run time
// was never written.
func (r *Runner) save() {
	r.saveMu.Lock()
	defer r.saveMu.Unlock()
	r.mu.Lock()
	b, err := json.MarshalIndent(r.st, "", "  ")
	r.mu.Unlock()
	if err == nil {
		err = atomicfile.Write(StatusPath(r.paths.Config), b, 0o644)
	}
	if err != nil {
		r.log.Warn("cron: status not saved", "err", err)
	}
}
