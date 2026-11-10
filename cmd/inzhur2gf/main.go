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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, logger, os.Getenv); err != nil {
		logger.Error("sync failed", "error", err)
		stop()
		os.Exit(1)
	}
}

func run(_ context.Context, logger *slog.Logger, getenv func(string) string) error {
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
