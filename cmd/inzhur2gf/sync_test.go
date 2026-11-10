package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blobor/ghostfolio-inzhur/ghostfolio"
	"github.com/blobor/ghostfolio-inzhur/internal/state"
	"github.com/blobor/ghostfolio-inzhur/inzhur"
)

var syncTime = time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

// fakes records the calls of one sync, in sequence.
type fakes struct {
	calls []string

	storedToken string
	refreshErr  error

	transactions []inzhur.Transaction
	holdings     []inzhur.Holding

	missingSymbols []string
	imported       []ghostfolio.Activity
	importDryRun   bool
	gfHoldings     []ghostfolio.Holding
}

func (f *fakes) Token(startToken string) (string, state.Source, error) {
	f.calls = append(f.calls, "token")
	if f.storedToken != "" {
		return f.storedToken, state.SourceStored, nil
	}
	return startToken, state.SourceStart, nil
}

func (f *fakes) Save(refreshToken, _ string) error {
	f.calls = append(f.calls, "save:"+refreshToken)
	f.storedToken = refreshToken
	return nil
}

func (f *fakes) Refresh(_ context.Context, refreshToken string) (inzhur.Session, error) {
	f.calls = append(f.calls, "refresh:"+refreshToken)
	if f.refreshErr != nil {
		return inzhur.Session{}, f.refreshErr
	}
	return inzhur.Session{AccessToken: "access", RefreshToken: refreshToken + "+1"}, nil
}

func (f *fakes) Transactions(_ context.Context, accessToken string) ([]inzhur.Transaction, error) {
	f.calls = append(f.calls, "transactions:"+accessToken)
	return f.transactions, nil
}

func (f *fakes) inzhurHoldings(_ context.Context, accessToken string) ([]inzhur.Holding, error) {
	f.calls = append(f.calls, "holdings:"+accessToken)
	return f.holdings, nil
}

func (f *fakes) HasSymbol(_ context.Context, _, symbol string) (bool, error) {
	f.calls = append(f.calls, "symbol:"+symbol)
	return !slices.Contains(f.missingSymbols, symbol), nil
}

func (f *fakes) Import(_ context.Context, activities []ghostfolio.Activity, dryRun bool) (ghostfolio.ImportResult, error) {
	f.calls = append(f.calls, "import")
	f.imported = activities
	f.importDryRun = dryRun
	return ghostfolio.ImportResult{Imported: len(activities)}, nil
}

func (f *fakes) ghostfolioHoldings(_ context.Context) ([]ghostfolio.Holding, error) {
	f.calls = append(f.calls, "gf-holdings")
	return f.gfHoldings, nil
}

// The two clients have a method with the name Holdings. These types give
// each client its own method set.
type fakeInzhur struct{ *fakes }

func (f fakeInzhur) Holdings(ctx context.Context, accessToken string) ([]inzhur.Holding, error) {
	return f.inzhurHoldings(ctx, accessToken)
}

type fakeGhostfolio struct{ *fakes }

func (f fakeGhostfolio) Holdings(ctx context.Context) ([]ghostfolio.Holding, error) {
	return f.ghostfolioHoldings(ctx)
}

func newSyncer(f *fakes, dryRun bool) (*syncer, *bytes.Buffer) {
	var log bytes.Buffer
	return &syncer{
		cfg: config{
			InzhurRefreshToken:  "start",
			GhostfolioAccountID: "account-1",
			DryRun:              dryRun,
		},
		logger:     slog.New(slog.NewTextHandler(&log, nil)),
		inzhur:     fakeInzhur{f},
		ghostfolio: fakeGhostfolio{f},
		store:      f,
		now:        func() time.Time { return syncTime },
	}, &log
}

func reitPurchase(id int64, when time.Time, quantity float64) inzhur.Transaction {
	return inzhur.Transaction{
		ID:        id,
		Amount:    1000,
		Type:      inzhur.TypePurchase,
		CreatedAt: when,
		Asset:     &inzhur.Asset{ID: 21, Type: inzhur.AssetFund, Name: "Inzhur REIT"},
		Contract:  &inzhur.Contract{ID: id, Quantity: quantity, PricePerUnit: 11.6},
	}
}

func baseFakes() *fakes {
	return &fakes{
		transactions: []inzhur.Transaction{
			reitPurchase(1002, syncTime.Add(-48*time.Hour), 5),
			reitPurchase(1001, syncTime.Add(-72*time.Hour), 10),
		},
		holdings:   []inzhur.Holding{{AssetID: 21, Type: inzhur.AssetFund, Quantity: 15}},
		gfHoldings: []ghostfolio.Holding{{Symbol: "GF_INZHUR-REIT", DataSource: "MANUAL", Quantity: 15}},
	}
}

func TestSync(t *testing.T) {
	f := baseFakes()
	s, log := newSyncer(f, false)

	if err := s.run(context.Background()); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	wantCalls := []string{
		"token",
		"refresh:start",
		"save:start+1",
		"transactions:access",
		"holdings:access",
		"symbol:GF_INZHUR-REIT",
		"import",
		"gf-holdings",
	}
	if !slices.Equal(f.calls, wantCalls) {
		t.Errorf("calls:\n got %v\nwant %v", f.calls, wantCalls)
	}

	if len(f.imported) != 2 || f.importDryRun {
		t.Fatalf("imported %d activities (dry run %v), want 2 (false)", len(f.imported), f.importDryRun)
	}
	if got := f.imported[0]; got.Comment != "inzhur:1001" || got.AccountID != "account-1" {
		t.Errorf("first activity = %+v", got)
	}
	if !strings.Contains(log.String(), "quantities are equal") {
		t.Errorf("log does not report equal quantities:\n%s", log)
	}
}

func TestSyncUsesStoredToken(t *testing.T) {
	f := baseFakes()
	f.storedToken = "stored"
	s, _ := newSyncer(f, false)

	if err := s.run(context.Background()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !slices.Contains(f.calls, "refresh:stored") || f.storedToken != "stored+1" {
		t.Errorf("calls = %v, stored token = %q", f.calls, f.storedToken)
	}
}

func TestSyncDryRun(t *testing.T) {
	f := baseFakes()
	s, _ := newSyncer(f, true)

	if err := s.run(context.Background()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !f.importDryRun {
		t.Error("the import was not a dry run")
	}
	if slices.Contains(f.calls, "gf-holdings") {
		t.Error("the sync compared the quantities in a dry run")
	}
	// A dry run uses the refresh token, thus it must save the new one.
	if !slices.Contains(f.calls, "save:start+1") {
		t.Error("the dry run did not save the new refresh token")
	}
}

func TestSyncStopsWhenSessionIsExpired(t *testing.T) {
	f := baseFakes()
	f.refreshErr = inzhur.ErrSessionExpired
	s, _ := newSyncer(f, false)

	err := s.run(context.Background())
	if !errors.Is(err, inzhur.ErrSessionExpired) {
		t.Fatalf("run() error = %v, want ErrSessionExpired", err)
	}
	if !strings.Contains(err.Error(), envInzhurRefreshToken) {
		t.Errorf("error does not name %s: %v", envInzhurRefreshToken, err)
	}
	if want := []string{"token", "refresh:start"}; !slices.Equal(f.calls, want) {
		t.Errorf("calls = %v, want %v", f.calls, want)
	}
}

func TestSyncImportsNothingWhenAProfileIsMissing(t *testing.T) {
	f := baseFakes()
	f.missingSymbols = []string{"GF_INZHUR-REIT"}
	s, _ := newSyncer(f, false)

	err := s.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "GF_INZHUR-REIT") {
		t.Fatalf("run() error = %v, want an error that names GF_INZHUR-REIT", err)
	}
	if slices.Contains(f.calls, "import") {
		t.Error("the sync did the import")
	}
}

func TestSyncImportsNothingWhenATransactionIsNotKnown(t *testing.T) {
	f := baseFakes()
	f.transactions = append(f.transactions, inzhur.Transaction{
		ID: 2000, Type: "Bonus crediting", CreatedAt: syncTime.Add(-24 * time.Hour),
	})
	s, _ := newSyncer(f, false)

	err := s.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "transaction 2000") {
		t.Fatalf("run() error = %v, want an error that names transaction 2000", err)
	}
	if slices.Contains(f.calls, "import") || slices.Contains(f.calls, "symbol:GF_INZHUR-REIT") {
		t.Errorf("the sync called Ghostfolio: %v", f.calls)
	}
}

func TestSyncDoesNotCompareWhenTransactionsArePending(t *testing.T) {
	f := baseFakes()
	f.transactions = append(f.transactions, reitPurchase(1003, syncTime.Add(-10*time.Minute), 1))
	s, log := newSyncer(f, false)

	if err := s.run(context.Background()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if len(f.imported) != 2 {
		t.Errorf("imported %d activities, want 2", len(f.imported))
	}
	if slices.Contains(f.calls, "gf-holdings") {
		t.Error("the sync compared the quantities")
	}
	if !strings.Contains(log.String(), "pending=1") {
		t.Errorf("log does not report the pending transaction:\n%s", log)
	}
}

func TestSyncWarnsWhenQuantitiesAreDifferent(t *testing.T) {
	f := baseFakes()
	f.gfHoldings[0].Quantity = 14
	s, log := newSyncer(f, false)

	if err := s.run(context.Background()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"level=WARN", "quantity is different", "symbol=GF_INZHUR-REIT", "inzhur=15", "ghostfolio=14"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log does not contain %q:\n%s", want, log)
		}
	}
}
