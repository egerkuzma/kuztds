// Command admin — KuzTDS management REST API (auth, statistics, groups).
//
// Run:   KUZTDS_ADMIN_PASSWORD_HASH=... go run ./cmd/admin
// Password hash:   go run ./cmd/admin -hash 'my-password'
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/egerkuzma/kuztds/internal/admin"
	"github.com/egerkuzma/kuztds/internal/security"
	"github.com/egerkuzma/kuztds/internal/store"
)

func main() {
	hashPw := flag.String("hash", "", "print the argon2id hash for a password and exit")
	flag.Parse()
	if *hashPw != "" {
		h, err := security.HashPassword(*hashPw)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		fmt.Println(h)
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg := admin.Config{
		AdminUser:    getenv("KUZTDS_ADMIN_USER", "admin"),
		PasswordHash: os.Getenv("KUZTDS_ADMIN_PASSWORD_HASH"),
		SessionTTL:   12 * time.Hour,
		CookieSecure: getenv("KUZTDS_ADMIN_COOKIE_SECURE", "true") == "true",
		EngineURL:    getenv("KUZTDS_ENGINE_URL", "http://localhost:8080"),
		PasswordFile: os.Getenv("KUZTDS_ADMIN_PASSWORD_FILE"),
		Sessions:     security.NewMemoryStore(), // overridden by Redis below
		Limiter:      allowAll{},
		Log:          log,
	}
	if cfg.PasswordHash == "" {
		log.Warn("KUZTDS_ADMIN_PASSWORD_HASH not set — login is impossible. Generate one: go run ./cmd/admin -hash 'password'")
	}

	// Redis: sessions (survive restart) + login rate-limit.
	if a := os.Getenv("KUZTDS_REDIS_ADDR"); a != "" {
		if rdb, err := store.OpenRedis(a, os.Getenv("KUZTDS_REDIS_PASSWORD"), 0); err != nil {
			log.Warn("redis not connected, using in-memory sessions", "err", err)
		} else {
			cfg.Sessions = store.NewRedisSessions(rdb)
			cfg.Limiter = limiterAdapter{c: store.NewCounters(rdb)}
			log.Info("redis connected", "addr", a)
		}
	}

	// Groups config (same JSON as the engine) — read and save.
	if f := os.Getenv("KUZTDS_GROUPS_FILE"); f != "" {
		cfg.Groups = admin.NewFileGroups(f)
		log.Info("groups file", "path", f)
	}

	// .dat lists (IP, WAP operators, signatures) — editor.
	if d := os.Getenv("KUZTDS_DATA_DIR"); d != "" {
		cfg.Lists = admin.NewFileLists(d)
		log.Info("lists dir", "path", d)
	}

	// Keywords.
	cfg.Keys = admin.NewFileKeys(getenv("KUZTDS_KEYS_DIR", "keys"))

	// ClickHouse: statistics (store.CH implements admin.StatsProvider directly).
	if a := os.Getenv("KUZTDS_CLICKHOUSE_ADDR"); a != "" {
		ch, err := store.OpenCH(a, getenv("KUZTDS_CLICKHOUSE_DB", "kuztds"),
			getenv("KUZTDS_CLICKHOUSE_USER", "kuztds"), os.Getenv("KUZTDS_CLICKHOUSE_PASSWORD"))
		if err != nil {
			log.Warn("clickhouse not connected, stats disabled", "err", err)
		} else {
			cfg.Stats = ch
			log.Info("clickhouse connected", "addr", a)
		}
	}

	srv := &http.Server{
		Addr:              getenv("KUZTDS_ADMIN_LISTEN", ":8090"),
		Handler:           admin.New(cfg).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info("admin api listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer scancel()
	_ = srv.Shutdown(sctx)
	log.Info("admin api stopped")
}

// limiterAdapter limits login to 10 attempts per minute per key (IP).
type limiterAdapter struct{ c *store.Counters }

func (a limiterAdapter) Allow(ctx context.Context, key string) bool {
	return a.c.LoginAllow(ctx, key, 10, time.Minute)
}

type allowAll struct{}

func (allowAll) Allow(context.Context, string) bool { return true }

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
