// Command inzhur2gf syncs your own Inzhur transactions into Ghostfolio.
//
// The command does one sync and then stops. Use a scheduler, for example
// cron or a Kubernetes CronJob, to start it again.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blobor/ghostfolio-inzhur/ghostfolio"
	"github.com/blobor/ghostfolio-inzhur/internal/state"
	"github.com/blobor/ghostfolio-inzhur/inzhur"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	if err := run(context.Background(), logger, os.Getenv); err != nil {
		logger.Error("sync failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger, getenv func(string) string) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
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

	store, err := state.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	gf, err := ghostfolio.NewClient(cfg.GhostfolioURL.String(), cfg.GhostfolioAccessToken)
	if err != nil {
		return err
	}

	s := &syncer{
		cfg:        cfg,
		logger:     logger,
		inzhur:     inzhur.NewClient(),
		ghostfolio: gf,
		store:      store,
		now:        time.Now,
	}
	return s.run(ctx)
}
