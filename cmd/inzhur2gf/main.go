// Command inzhur2gf syncs your own Inzhur transactions into Ghostfolio.
//
// The command does one sync and then stops. Use a scheduler, for example
// cron or a Kubernetes CronJob, to start it again.
package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

var errNotImplemented = errors.New("sync is not implemented yet")

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(context.Background(), logger, os.Getenv); err != nil {
		logger.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	// The context is not used until the sync is implemented.
	_, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig(getenv)
	if err != nil {
		return err
	}

	logger.Info("starting sync",
		"version", version,
		"ghostfolio", cfg.GhostfolioURL.Host,
		"data_dir", cfg.DataDir,
		"dry_run", cfg.DryRun,
	)

	return errNotImplemented
}
