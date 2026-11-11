package mapper

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blobor/ghostfolio-inzhur/ghostfolio"
	"github.com/blobor/ghostfolio-inzhur/inzhur"
)

const accountID = "00000000-0000-0000-0000-000000000000"

var (
	reit = &inzhur.Asset{ID: 21, Type: inzhur.AssetFund, Name: "Inzhur REIT"}
	bond = &inzhur.Asset{ID: 36, Type: inzhur.AssetBond, ISIN: "UA4000238976"}

	// now is the time of the sync in the tests.
	now = at("2026-02-01T00:00:00Z")
)

func at(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t
}

func options() Options {
	return Options{AccountID: accountID, Now: now, MinAge: time.Hour}
}

func trade(id int64, txType inzhur.TransactionType, asset *inzhur.Asset, when string,
	contractID int64, quantity, price float64, amount int64,
) inzhur.Transaction {
	return inzhur.Transaction{
		ID:        id,
		Amount:    amount,
		Type:      txType,
		CreatedAt: at(when),
		Asset:     asset,
		Contract:  &inzhur.Contract{ID: contractID, Quantity: quantity, PricePerUnit: price, AmountUAH: amount},
	}
}

func cash(id int64, txType inzhur.TransactionType, asset *inzhur.Asset, when string, amount int64) inzhur.Transaction {
	tx := inzhur.Transaction{ID: id, Amount: amount, Type: txType, CreatedAt: at(when), Asset: asset}
	switch txType {
	case inzhur.TypeDividendCrediting, inzhur.TypeDividendTax:
		tx.DistributionType = inzhur.DistributionDividend
	case inzhur.TypeDistributionCrediting:
		tx.DistributionType = inzhur.DistributionCoupon
	}
	return tx
}

func activity(comment string, activityType ghostfolio.ActivityType, symbol, when string,
	quantity, unitPrice, fee float64,
) ghostfolio.Activity {
	return ghostfolio.Activity{
		AccountID:  accountID,
		Comment:    comment,
		Currency:   "UAH",
		DataSource: "MANUAL",
		Date:       at(when),
		Fee:        fee,
		Quantity:   quantity,
		Symbol:     symbol,
		Type:       activityType,
		UnitPrice:  unitPrice,
	}
}

func mustMap(t *testing.T, transactions []inzhur.Transaction, opts Options) Result {
	t.Helper()

	result, err := Map(transactions, opts)
	if err != nil {
		t.Fatalf("Map() error = %v", err)
	}
	return result
}

func wantActivities(t *testing.T, got, want []ghostfolio.Activity) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("Map() returned %d activities, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("activity %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestMapAllTypes(t *testing.T) {
	transactions := []inzhur.Transaction{
		// The API returns the newest transaction first.
		trade(1009, inzhur.TypeSaleTax, reit, "2026-01-20T10:00:05Z", 503, 5, 11.7, 195),
		trade(1008, inzhur.TypeSale, reit, "2026-01-20T10:00:00Z", 503, 5, 11.7, 5850),
		cash(1007, inzhur.TypeWithdrawal, nil, "2026-01-18T09:00:00Z", 50000),
		cash(1006, inzhur.TypeDistributionCrediting, bond, "2026-01-15T08:00:00Z", 9800),
		cash(1005, inzhur.TypeDividendTax, reit, "2026-01-10T22:19:01Z", 180),
		cash(1004, inzhur.TypeDividendCrediting, reit, "2026-01-10T22:19:00Z", 1000),
		trade(1003, inzhur.TypeGiftAgreement, reit, "2026-01-07T12:00:00Z", 502, 20, 10.9966, 0),
		trade(1002, inzhur.TypePurchase, bond, "2026-01-06T09:30:00Z", 501, 2, 1022.47, 204494),
		trade(1001, inzhur.TypePurchase, reit, "2026-01-05T02:34:38Z", 500, 10, 11.6178, 11618),
		cash(1000, inzhur.TypeDeposit, nil, "2026-01-04T08:00:00Z", 300000),
	}

	result := mustMap(t, transactions, options())

	wantActivities(t, result.Activities, []ghostfolio.Activity{
		activity("inzhur:1001", ghostfolio.TypeBuy, "GF_INZHUR-REIT", "2026-01-05T02:34:38Z", 10, 11.6178, 0),
		activity("inzhur:1002", ghostfolio.TypeBuy, "GF_UA4000238976", "2026-01-06T09:30:00Z", 2, 1022.47, 0),
		activity("inzhur:1003", ghostfolio.TypeBuy, "GF_INZHUR-REIT", "2026-01-07T12:00:00Z", 20, 0, 0),
		activity("inzhur:1004", ghostfolio.TypeDividend, "GF_INZHUR-REIT", "2026-01-10T22:19:00Z", 1, 10, 1.8),
		activity("inzhur:1006", ghostfolio.TypeDividend, "GF_UA4000238976", "2026-01-15T08:00:00Z", 1, 98, 0),
		activity("inzhur:1008", ghostfolio.TypeSell, "GF_INZHUR-REIT", "2026-01-20T10:00:00Z", 5, 11.7, 1.95),
	})
	if result.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2", result.Skipped)
	}
	if result.Pending != 0 {
		t.Errorf("Pending = %d, want 0", result.Pending)
	}
}

func TestMapIsDeterministic(t *testing.T) {
	transactions := []inzhur.Transaction{
		cash(1005, inzhur.TypeDividendTax, reit, "2026-01-10T22:19:01Z", 180),
		cash(1004, inzhur.TypeDividendCrediting, reit, "2026-01-10T22:19:00Z", 1000),
		trade(1002, inzhur.TypePurchase, reit, "2026-01-05T02:34:38Z", 501, 3, 11.6, 3480),
		trade(1001, inzhur.TypePurchase, reit, "2026-01-05T02:34:38Z", 500, 10, 11.6178, 11618),
	}
	reversed := slices.Clone(transactions)
	slices.Reverse(reversed)

	first := mustMap(t, transactions, options())
	second := mustMap(t, reversed, options())

	wantActivities(t, second.Activities, first.Activities)
	if got := first.Activities[0].Comment; got != "inzhur:1001" {
		t.Errorf("first activity = %s, want inzhur:1001", got)
	}
}

func TestMapMatchesEachDividendTaxToItsDividend(t *testing.T) {
	energy := &inzhur.Asset{ID: 13, Type: inzhur.AssetFund, Name: "Inzhur Energy"}
	transactions := []inzhur.Transaction{
		// Two funds pay a dividend in the same second.
		cash(1004, inzhur.TypeDividendTax, energy, "2026-01-10T22:19:01Z", 90),
		cash(1003, inzhur.TypeDividendTax, reit, "2026-01-10T22:19:01Z", 180),
		cash(1002, inzhur.TypeDividendCrediting, energy, "2026-01-10T22:19:00Z", 500),
		cash(1001, inzhur.TypeDividendCrediting, reit, "2026-01-10T22:19:00Z", 1000),
		// The same fund pays again one month before.
		cash(901, inzhur.TypeDividendTax, reit, "2025-12-10T22:19:01Z", 162),
		cash(900, inzhur.TypeDividendCrediting, reit, "2025-12-10T22:19:00Z", 900),
	}

	result := mustMap(t, transactions, options())

	wantActivities(t, result.Activities, []ghostfolio.Activity{
		activity("inzhur:900", ghostfolio.TypeDividend, "GF_INZHUR-REIT", "2025-12-10T22:19:00Z", 1, 9, 1.62),
		activity("inzhur:1001", ghostfolio.TypeDividend, "GF_INZHUR-REIT", "2026-01-10T22:19:00Z", 1, 10, 1.8),
		activity("inzhur:1002", ghostfolio.TypeDividend, "GF_INZHUR-ENERGY", "2026-01-10T22:19:00Z", 1, 5, 0.9),
	})
}

func TestMapHoldsBackYoungTransactions(t *testing.T) {
	transactions := []inzhur.Transaction{
		// Younger than one hour.
		trade(1004, inzhur.TypePurchase, reit, "2026-01-31T23:30:00Z", 502, 1, 11.7, 1170),
		// The dividend is older than one hour, but its tax is not.
		cash(1003, inzhur.TypeDividendTax, reit, "2026-01-31T23:00:01Z", 180),
		cash(1002, inzhur.TypeDividendCrediting, reit, "2026-01-31T23:00:00Z", 1000),
		trade(1001, inzhur.TypePurchase, reit, "2026-01-31T22:00:00Z", 501, 10, 11.6178, 11618),
	}

	result := mustMap(t, transactions, options())

	wantActivities(t, result.Activities, []ghostfolio.Activity{
		activity("inzhur:1001", ghostfolio.TypeBuy, "GF_INZHUR-REIT", "2026-01-31T22:00:00Z", 10, 11.6178, 0),
	})
	if result.Pending != 3 {
		t.Errorf("Pending = %d, want 3", result.Pending)
	}
}

func TestMapUsesSymbolMap(t *testing.T) {
	opts := options()
	opts.SymbolMap = map[int]string{21: "GF_MY-REIT"}
	transactions := []inzhur.Transaction{
		trade(1001, inzhur.TypePurchase, reit, "2026-01-05T02:34:38Z", 500, 10, 11.6178, 11618),
	}

	result := mustMap(t, transactions, opts)

	if got := result.Activities[0].Symbol; got != "GF_MY-REIT" {
		t.Errorf("Symbol = %q, want GF_MY-REIT", got)
	}
}

func TestMapErrors(t *testing.T) {
	good := trade(1001, inzhur.TypePurchase, reit, "2026-01-05T02:34:38Z", 500, 10, 11.6178, 11618)

	noContract := good
	noContract.ID, noContract.Contract = 2001, nil

	noAsset := good
	noAsset.ID, noAsset.Asset = 2002, nil

	tests := []struct {
		name string
		tx   inzhur.Transaction
		want string
	}{
		{"unknown type", cash(2000, "Bonus crediting", reit, "2026-01-05T00:00:00Z", 100), "the type is not known"},
		{"purchase without contract", noContract, "the contract is missing"},
		{"purchase without asset", noAsset, "the asset is missing"},
		{"purchase with zero quantity",
			trade(2003, inzhur.TypePurchase, reit, "2026-01-05T00:00:00Z", 600, 0, 11.6, 0), "the quantity is 0"},
		{"purchase with zero price",
			trade(2004, inzhur.TypePurchase, reit, "2026-01-05T00:00:00Z", 601, 1, 0, 0), "the unit price is 0"},
		{"dividend with zero amount",
			cash(2005, inzhur.TypeDividendCrediting, reit, "2026-01-05T00:00:00Z", 0), "the amount is 0"},
		{"sale tax without sale",
			trade(2006, inzhur.TypeSaleTax, reit, "2026-01-05T00:00:00Z", 999, 1, 11.6, 10), "no sale has the contract 999"},
		{"dividend tax without dividend",
			cash(2007, inzhur.TypeDividendTax, reit, "2026-01-05T00:00:00Z", 10), "no dividend of asset 21"},
		{"asset without name",
			trade(2008, inzhur.TypePurchase, &inzhur.Asset{ID: 77, Type: inzhur.AssetFund}, "2026-01-05T00:00:00Z", 602, 1, 1, 100),
			"asset 77 (fund) has no name or ISIN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Map([]inzhur.Transaction{good, tt.tx}, options())
			if err == nil {
				t.Fatal("Map() error = nil, want an error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
			if len(result.Activities) != 0 {
				t.Errorf("Map() returned %d activities together with an error", len(result.Activities))
			}
		})
	}
}

func TestMapRejectsAmbiguousDividendTax(t *testing.T) {
	transactions := []inzhur.Transaction{
		cash(1003, inzhur.TypeDividendTax, reit, "2026-01-10T22:19:01Z", 180),
		cash(1002, inzhur.TypeDividendCrediting, reit, "2026-01-10T22:19:02Z", 1000),
		cash(1001, inzhur.TypeDividendCrediting, reit, "2026-01-10T22:19:00Z", 1000),
	}

	_, err := Map(transactions, options())
	if err == nil || !strings.Contains(err.Error(), "more than one dividend") {
		t.Fatalf("Map() error = %v, want an error about more than one dividend", err)
	}
}

func TestMapReportsAllProblems(t *testing.T) {
	transactions := []inzhur.Transaction{
		cash(2000, "Bonus crediting", reit, "2026-01-05T00:00:00Z", 100),
		cash(2001, "Other", reit, "2026-01-05T00:00:00Z", 100),
	}

	_, err := Map(transactions, options())
	if err == nil {
		t.Fatal("Map() error = nil, want an error")
	}
	for _, id := range []string{"transaction 2000", "transaction 2001"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error does not name %s: %v", id, err)
		}
	}
}

func TestMapRequiresAccountID(t *testing.T) {
	if _, err := Map(nil, Options{Now: now}); err == nil {
		t.Fatal("Map() error = nil, want an error")
	}
}

func TestSymbol(t *testing.T) {
	tests := []struct {
		asset inzhur.Asset
		want  string
	}{
		{inzhur.Asset{ID: 21, Type: inzhur.AssetFund, Name: "Inzhur REIT"}, "GF_INZHUR-REIT"},
		{inzhur.Asset{ID: 13, Type: inzhur.AssetFund, Name: "Inzhur Energy"}, "GF_INZHUR-ENERGY"},
		{inzhur.Asset{ID: 48, Type: inzhur.AssetFund, Name: "Inzhur MilTech"}, "GF_INZHUR-MILTECH"},
		{inzhur.Asset{ID: 36, Type: inzhur.AssetBond, ISIN: "UA4000238976"}, "GF_UA4000238976"},
		{inzhur.Asset{ID: 40, Type: inzhur.AssetBond, ISIN: "ua4000239016"}, "GF_UA4000239016"},
		{inzhur.Asset{ID: 50, Type: inzhur.AssetFund, Name: "  Inzhur  Real-Estate (2) "}, "GF_INZHUR-REAL-ESTATE-2"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got, err := Symbol(tt.asset, nil)
			if err != nil {
				t.Fatalf("Symbol(%+v) error = %v", tt.asset, err)
			}
			if got != tt.want {
				t.Errorf("Symbol(%+v) = %q, want %q", tt.asset, got, tt.want)
			}
		})
	}

	if _, err := Symbol(inzhur.Asset{ID: 60, Type: "stock", Name: "X"}, nil); err == nil {
		t.Error("Symbol() of an unknown asset type: error = nil, want an error")
	}
}
