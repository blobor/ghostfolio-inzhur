package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/blobor/ghostfolio-inzhur/ghostfolio"
	"github.com/blobor/ghostfolio-inzhur/internal/mapper"
	"github.com/blobor/ghostfolio-inzhur/internal/state"
	"github.com/blobor/ghostfolio-inzhur/inzhur"
)

const (
	// minAge is the minimum age of a transaction for the import. A tax
	// comes a short time after its dividend or sale.
	minAge = time.Hour

	// quantityTolerance is the largest difference between two quantities
	// that are equal.
	quantityTolerance = 1e-6
)

// inzhurAPI is the part of the Inzhur client that the sync uses.
type inzhurAPI interface {
	Refresh(ctx context.Context, refreshToken string) (inzhur.Session, error)
	Transactions(ctx context.Context, accessToken string) ([]inzhur.Transaction, error)
	Holdings(ctx context.Context, accessToken string) ([]inzhur.Holding, error)
}

// ghostfolioAPI is the part of the Ghostfolio client that the sync uses.
type ghostfolioAPI interface {
	HasSymbol(ctx context.Context, dataSource, symbol string) (bool, error)
	Import(ctx context.Context, activities []ghostfolio.Activity, dryRun bool) (ghostfolio.ImportResult, error)
	Holdings(ctx context.Context) ([]ghostfolio.Holding, error)
}

// tokenStore keeps the refresh token between runs.
type tokenStore interface {
	Token(startToken string) (string, state.Source, error)
	Save(refreshToken, startToken string) error
}

// syncer does one sync.
type syncer struct {
	cfg        config
	logger     *slog.Logger
	inzhur     inzhurAPI
	ghostfolio ghostfolioAPI
	store      tokenStore
	now        func() time.Time
}

// run does one sync: refresh the session, read Inzhur, convert, import
// into Ghostfolio, then compare the quantities.
func (s *syncer) run(ctx context.Context) error {
	accessToken, err := s.refresh(ctx)
	if err != nil {
		return err
	}

	transactions, err := s.inzhur.Transactions(ctx, accessToken)
	if err != nil {
		return fmt.Errorf("read transactions: %w", err)
	}
	holdings, err := s.inzhur.Holdings(ctx, accessToken)
	if err != nil {
		return fmt.Errorf("read holdings: %w", err)
	}

	result, err := mapper.Map(transactions, mapper.Options{
		AccountID: s.cfg.GhostfolioAccountID,
		SymbolMap: s.cfg.SymbolMap,
		Now:       s.now(),
		MinAge:    minAge,
	})
	if err != nil {
		return err
	}
	s.logger.Info("transactions converted",
		"transactions", len(transactions),
		"activities", len(result.Activities),
		"skipped", result.Skipped,
		"pending", result.Pending,
	)

	if err := s.checkSymbols(ctx, result.Activities); err != nil {
		return err
	}

	imported, err := s.ghostfolio.Import(ctx, result.Activities, s.cfg.DryRun)
	if err != nil {
		return fmt.Errorf("import activities: %w", err)
	}
	s.logger.Info("import complete",
		"dry_run", s.cfg.DryRun,
		"imported", imported.Imported,
		"duplicates", imported.Duplicates,
	)

	switch {
	case s.cfg.DryRun:
		s.logger.Info("quantities not compared, because this is a dry run")
	case result.Pending > 0:
		s.logger.Info("quantities not compared, because some transactions are pending")
	default:
		if err := s.compare(ctx, transactions, holdings); err != nil {
			return err
		}
	}
	return nil
}

// refresh gets an access token and saves the new refresh token.
func (s *syncer) refresh(ctx context.Context) (string, error) {
	refreshToken, source, err := s.store.Token(s.cfg.InzhurRefreshToken)
	if err != nil {
		return "", err
	}
	s.logger.Info("refresh token selected", "source", source)

	session, err := s.inzhur.Refresh(ctx, refreshToken)
	if errors.Is(err, inzhur.ErrSessionExpired) {
		return "", fmt.Errorf("%w: log in to Inzhur and set a new %s", err, envInzhurRefreshToken)
	}
	if err != nil {
		return "", fmt.Errorf("refresh session: %w", err)
	}

	// The old refresh token is not valid after the refresh. Save the new
	// token before all other work.
	if err := s.store.Save(session.RefreshToken, s.cfg.InzhurRefreshToken); err != nil {
		return "", fmt.Errorf("save refresh token: %w", err)
	}
	return session.AccessToken, nil
}

// checkSymbols makes sure that Ghostfolio has an asset profile for each
// symbol. Without this check, the import makes an empty profile.
func (s *syncer) checkSymbols(ctx context.Context, activities []ghostfolio.Activity) error {
	var symbols []string
	for _, a := range activities {
		if !slices.Contains(symbols, a.Symbol) {
			symbols = append(symbols, a.Symbol)
		}
	}
	slices.Sort(symbols)

	var missing []string
	for _, symbol := range symbols {
		ok, err := s.ghostfolio.HasSymbol(ctx, ghostfolio.DataSourceManual, symbol)
		if err != nil {
			return fmt.Errorf("examine symbol %s: %w", symbol, err)
		}
		if !ok {
			missing = append(missing, symbol)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("ghostfolio has no asset profile with the data source %s for: %s; "+
			"make the profiles or set %s",
			ghostfolio.DataSourceManual, strings.Join(missing, ", "), envSymbolMap)
	}
	return nil
}

// compare writes a warning for each asset whose quantity in Ghostfolio is
// different from the quantity in Inzhur.
func (s *syncer) compare(ctx context.Context, transactions []inzhur.Transaction, holdings []inzhur.Holding) error {
	gfHoldings, err := s.ghostfolio.Holdings(ctx)
	if err != nil {
		return fmt.Errorf("read ghostfolio holdings: %w", err)
	}

	inGhostfolio := make(map[string]float64)
	for _, h := range gfHoldings {
		if h.DataSource == ghostfolio.DataSourceManual {
			inGhostfolio[h.Symbol] += h.Quantity
		}
	}

	// The holdings of Inzhur have only the asset ID. The transactions have
	// the name or the ISIN, which the symbol needs.
	assets := make(map[int]inzhur.Asset)
	for _, tx := range transactions {
		if tx.Asset != nil {
			assets[tx.Asset.ID] = *tx.Asset
		}
	}

	differences := 0
	for _, h := range holdings {
		asset, ok := assets[h.AssetID]
		if !ok {
			asset = inzhur.Asset{ID: h.AssetID, Type: h.Type}
		}
		symbol, err := mapper.Symbol(asset, s.cfg.SymbolMap)
		if err != nil {
			s.logger.Warn("quantity not compared", "asset_id", h.AssetID, "reason", err.Error())
			differences++
			continue
		}

		if got := inGhostfolio[symbol]; math.Abs(got-h.Quantity) > quantityTolerance {
			s.logger.Warn("quantity is different",
				"symbol", symbol, "inzhur", h.Quantity, "ghostfolio", got)
			differences++
		}
	}

	if differences == 0 {
		s.logger.Info("quantities are equal", "assets", len(holdings))
	}
	return nil
}
