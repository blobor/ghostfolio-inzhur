package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
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

	stored         []ghostfolio.StoredActivity
	missingSymbols []string
	imported       []ghostfolio.Activity
	importDryRun   bool
	updated        map[string]ghostfolio.Activity
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

func (f *fakes) Activities(_ context.Context, accountID string) ([]ghostfolio.StoredActivity, error) {
	f.calls = append(f.calls, "activities:"+accountID)
	return f.stored, nil
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

func (f *fakes) Update(_ context.Context, id string, activity ghostfolio.Activity) error {
	f.calls = append(f.calls, "update:"+id)
	if f.updated == nil {
		f.updated = make(map[string]ghostfolio.Activity)
	}
	f.updated[id] = activity
	return nil
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
	t.Parallel()

	f := baseFakes()
	s, log := newSyncer(f, false)

	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	wantCalls := []string{
		"token",
		"refresh:start",
		"save:start+1",
		"transactions:access",
		"holdings:access",
		"activities:account-1",
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

// afterFirstSync does a sync into an empty account. Then it gives
// Ghostfolio the activities of that sync, with the IDs gf-1, gf-2 and so
// on.
func afterFirstSync(t *testing.T) *fakes {
	t.Helper()

	f := baseFakes()
	s, _ := newSyncer(f, false)
	if err := s.run(t.Context()); err != nil {
		t.Fatalf("first run() error = %v", err)
	}
	for i, a := range f.imported {
		f.stored = append(f.stored, ghostfolio.StoredActivity{ID: "gf-" + strconv.Itoa(i+1), Activity: a})
	}
	f.calls, f.imported = nil, nil
	return f
}

func wantLog(t *testing.T, log *bytes.Buffer, parts ...string) {
	t.Helper()
	for _, want := range parts {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log does not contain %q:\n%s", want, log)
		}
	}
}

func TestSyncCreatesOnlyTransactionsThatGhostfolioDoesNotHave(t *testing.T) {
	t.Parallel()

	f := afterFirstSync(t)
	f.stored = f.stored[:1]

	s, log := newSyncer(f, false)
	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if len(f.imported) != 1 || f.imported[0].Comment != "inzhur:1002" {
		t.Errorf("imported = %+v, want only inzhur:1002", f.imported)
	}
	if len(f.updated) != 0 {
		t.Errorf("updated = %+v, want nothing", f.updated)
	}
	wantLog(t, log, "create=1", "update=0", "duplicates=0", "extra=0")
}

func TestSyncUpdatesATransactionThatInzhurChanged(t *testing.T) {
	t.Parallel()

	f := afterFirstSync(t)

	// Inzhur moves the time of a transaction after the import.
	moved := f.transactions[0].CreatedAt.Add(19 * time.Hour)
	f.transactions[0].CreatedAt = moved

	s, log := newSyncer(f, false)
	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if len(f.imported) != 0 {
		t.Errorf("imported %d activities, want 0: %+v", len(f.imported), f.imported)
	}
	got, ok := f.updated["gf-2"]
	if len(f.updated) != 1 || !ok {
		t.Fatalf("updated = %+v, want only gf-2", f.updated)
	}
	if got.Comment != "inzhur:1002" || !got.Date.Equal(moved) || got.AccountID != "account-1" {
		t.Errorf("updated activity = %+v", got)
	}
	wantLog(t, log, "create=0", "update=1", "activity updated", "comment=inzhur:1002", "id=gf-2")
}

func TestSyncDryRunDoesNotUpdate(t *testing.T) {
	t.Parallel()

	f := afterFirstSync(t)
	f.transactions[0].CreatedAt = f.transactions[0].CreatedAt.Add(19 * time.Hour)

	s, log := newSyncer(f, true)
	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if len(f.updated) != 0 {
		t.Errorf("the dry run updated %+v", f.updated)
	}
	wantLog(t, log, "update=1", "activity updated", "dry_run=true")
}

func TestSyncDoesNotUpdateWhenGhostfolioHasTwoActivitiesForOneTransaction(t *testing.T) {
	t.Parallel()

	f := afterFirstSync(t)
	moved := f.stored[1]
	moved.ID = "gf-3"
	moved.Date = moved.Date.Add(19 * time.Hour)
	f.stored = append(f.stored, moved)

	s, log := newSyncer(f, false)
	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if len(f.imported) != 0 || len(f.updated) != 0 {
		t.Errorf("imported = %+v, updated = %+v, want nothing", f.imported, f.updated)
	}
	wantLog(t, log, "level=WARN", "more than one activity for a transaction",
		"comment=inzhur:1002", "ids=gf-2,gf-3", "update=0", "duplicates=1")
}

func TestSyncWarnsAboutAnActivityThatInzhurDoesNotHave(t *testing.T) {
	t.Parallel()

	f := afterFirstSync(t)
	gone := f.stored[0]
	gone.ID, gone.Comment = "gf-9", "inzhur:999"
	manual := f.stored[0]
	manual.ID, manual.Comment = "gf-10", "made by hand"
	f.stored = append(f.stored, gone, manual)

	s, log := newSyncer(f, false)
	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}

	if len(f.imported) != 0 || len(f.updated) != 0 {
		t.Errorf("imported = %+v, updated = %+v, want nothing", f.imported, f.updated)
	}
	wantLog(t, log, "extra=1", "level=WARN", "inzhur does not have", "comment=inzhur:999", "id=gf-9")
	if strings.Contains(log.String(), "gf-10") {
		t.Errorf("log has an activity that is not from this program:\n%s", log)
	}
}

func TestSyncUsesStoredToken(t *testing.T) {
	t.Parallel()

	f := baseFakes()
	f.storedToken = "stored"
	s, _ := newSyncer(f, false)

	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !slices.Contains(f.calls, "refresh:stored") || f.storedToken != "stored+1" {
		t.Errorf("calls = %v, stored token = %q", f.calls, f.storedToken)
	}
}

func TestSyncDryRun(t *testing.T) {
	t.Parallel()

	f := baseFakes()
	s, log := newSyncer(f, true)

	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if !f.importDryRun {
		t.Error("the import was not a dry run")
	}
	if slices.Contains(f.calls, "gf-holdings") {
		t.Error("the dry run read the holdings of Ghostfolio")
	}
	if !strings.Contains(log.String(), "quantities are equal") || !strings.Contains(log.String(), "source=activities") {
		t.Errorf("log does not report equal quantities from the activities:\n%s", log)
	}
	// A dry run uses the refresh token, thus it must save the new one.
	if !slices.Contains(f.calls, "save:start+1") {
		t.Error("the dry run did not save the new refresh token")
	}
}

func TestSyncDryRunWarnsWhenTheHistoryIsNotComplete(t *testing.T) {
	t.Parallel()

	f := baseFakes()
	f.holdings[0].Quantity = 20
	s, log := newSyncer(f, true)

	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"level=WARN", "symbol=GF_INZHUR-REIT", "inzhur=20", "activities=15"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log does not contain %q:\n%s", want, log)
		}
	}
}

func TestSyncWarnsWhenASoldAssetHasAQuantity(t *testing.T) {
	t.Parallel()

	f := baseFakes()
	// The history has a purchase of a bond, but Inzhur has no holding of it.
	f.transactions = append(f.transactions, inzhur.Transaction{
		ID:        1000,
		Amount:    204494,
		Type:      inzhur.TypePurchase,
		CreatedAt: syncTime.Add(-96 * time.Hour),
		Asset:     &inzhur.Asset{ID: 36, Type: inzhur.AssetBond, ISIN: "UA4000238976"},
		Contract:  &inzhur.Contract{ID: 1000, Quantity: 2, PricePerUnit: 1022.47},
	})
	s, log := newSyncer(f, true)

	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"level=WARN", "symbol=GF_UA4000238976", "inzhur=0", "activities=2"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log does not contain %q:\n%s", want, log)
		}
	}
}

func TestSyncStopsWhenSessionIsExpired(t *testing.T) {
	t.Parallel()

	f := baseFakes()
	f.refreshErr = inzhur.ErrSessionExpired
	s, _ := newSyncer(f, false)

	err := s.run(t.Context())
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
	t.Parallel()

	f := baseFakes()
	f.missingSymbols = []string{"GF_INZHUR-REIT"}
	s, _ := newSyncer(f, false)

	err := s.run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "GF_INZHUR-REIT") {
		t.Fatalf("run() error = %v, want an error that names GF_INZHUR-REIT", err)
	}
	if slices.Contains(f.calls, "import") {
		t.Error("the sync did the import")
	}
}

func TestSyncImportsNothingWhenATransactionIsNotKnown(t *testing.T) {
	t.Parallel()

	f := baseFakes()
	f.transactions = append(f.transactions, inzhur.Transaction{
		ID: 2000, Type: "Bonus crediting", CreatedAt: syncTime.Add(-24 * time.Hour),
	})
	s, _ := newSyncer(f, false)

	err := s.run(t.Context())
	if err == nil || !strings.Contains(err.Error(), "transaction 2000") {
		t.Fatalf("run() error = %v, want an error that names transaction 2000", err)
	}
	if slices.Contains(f.calls, "import") || slices.Contains(f.calls, "symbol:GF_INZHUR-REIT") {
		t.Errorf("the sync called Ghostfolio: %v", f.calls)
	}
}

func TestSyncDoesNotCompareWhenTransactionsArePending(t *testing.T) {
	t.Parallel()

	f := baseFakes()
	f.transactions = append(f.transactions, reitPurchase(1003, syncTime.Add(-10*time.Minute), 1))
	s, log := newSyncer(f, false)

	if err := s.run(t.Context()); err != nil {
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
	t.Parallel()

	f := baseFakes()
	f.gfHoldings[0].Quantity = 14
	s, log := newSyncer(f, false)

	if err := s.run(t.Context()); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	for _, want := range []string{"level=WARN", "quantity is different", "symbol=GF_INZHUR-REIT", "inzhur=15", "ghostfolio=14"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log does not contain %q:\n%s", want, log)
		}
	}
}
