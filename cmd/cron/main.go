// Command cron — the KuzTDS background service: list and geo database
// updates, VirusTotal checks, disk monitoring, keyword cleanup, conversion
// notifications. See internal/cron.
//
// It shares the engine's environment: the same KUZTDS_DATA_DIR,
// KUZTDS_GROUPS_FILE, KUZTDS_KEYS_DIR, KUZTDS_GEO_DB and KUZTDS_ASN_DB, plus
// KUZTDS_CRON_FILE for its own config (edited in the admin panel).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/egerkuzma/kuztds/internal/cron"
	"github.com/egerkuzma/kuztds/internal/store"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	paths := cron.Paths{
		Config:  getenv("KUZTDS_CRON_FILE", "cron.json"),
		DataDir: getenv("KUZTDS_DATA_DIR", "./data"),
		KeysDir: getenv("KUZTDS_KEYS_DIR", "keys"),
		Groups:  os.Getenv("KUZTDS_GROUPS_FILE"),
		CityDB:  os.Getenv("KUZTDS_GEO_DB"),
		ASNDB:   os.Getenv("KUZTDS_ASN_DB"),
	}

	// ClickHouse is only needed to announce conversions.
	var pb cron.PostbackSource
	if a := os.Getenv("KUZTDS_CLICKHOUSE_ADDR"); a != "" {
		ch, err := store.OpenCH(a, getenv("KUZTDS_CLICKHOUSE_DB", "kuztds"),
			getenv("KUZTDS_CLICKHOUSE_USER", "kuztds"), os.Getenv("KUZTDS_CLICKHOUSE_PASSWORD"))
		if err != nil {
			log.Warn("clickhouse not connected, conversion notifications unavailable", "err", err)
		} else {
			defer ch.Close()
			pb = ch
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("cron started", "config", paths.Config, "data", paths.DataDir)
	cron.New(paths, pb, log).Run(ctx, 5*time.Second)
	log.Info("cron stopped")
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
