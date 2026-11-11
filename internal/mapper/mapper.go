package mapper

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/blobor/ghostfolio-inzhur/ghostfolio"
	"github.com/blobor/ghostfolio-inzhur/inzhur"
)

const (
	currency     = "UAH"
	symbolPrefix = "GF_"

	// CommentPrefix starts the comment of each activity. The Inzhur
	// transaction ID comes after it.
	CommentPrefix = "inzhur:"

	// taxWindow is the maximum time between a dividend and its tax. In the
	// data of Inzhur the tax comes one second after the dividend.
	taxWindow = time.Minute
)

// Options controls the conversion.
type Options struct {
	// AccountID is the Ghostfolio account for all activities.
	AccountID string
	// SymbolMap overrides the symbol for an Inzhur asset ID.
	SymbolMap map[int]string
	// Now is the time of the sync.
	Now time.Time
	// MinAge is the minimum age of a transaction. A younger transaction is
	// not converted in this sync, because its related tax is possibly not
	// in the history yet.
	MinAge time.Duration
}

// Result is the output of Map.
type Result struct {
	// Activities are sorted by date, oldest first.
	Activities []ghostfolio.Activity
	// Skipped is the number of transactions that have no activity in
	// Ghostfolio: deposits and withdrawals.
	Skipped int
	// Pending is the number of transactions that are younger than MinAge.
	Pending int
}

// entry is an activity together with its source transactions.
type entry struct {
	activity ghostfolio.Activity
	tx       inzhur.Transaction
	// newest is the time of the newest source transaction.
	newest time.Time
	// fee is the sum of the related taxes, in kopiikas.
	fee     int64
	sources int
	hasTax  bool
}

// Map converts the transactions of Inzhur to activities for Ghostfolio.
//
// The result depends only on the input. Map returns an error and no
// activities if it cannot convert a transaction, for example because the
// type is not known.
func Map(transactions []inzhur.Transaction, opts Options) (Result, error) {
	if opts.AccountID == "" {
		return Result{}, errors.New("mapper: account ID is empty")
	}

	var (
		result   Result
		entries  []*entry
		saleTax  []inzhur.Transaction
		divTax   []inzhur.Transaction
		problems []error
	)

	for _, tx := range transactions {
		switch tx.Type {
		case inzhur.TypeDeposit, inzhur.TypeWithdrawal:
			result.Skipped++
		case inzhur.TypeSaleTax:
			saleTax = append(saleTax, tx)
		case inzhur.TypeDividendTax:
			divTax = append(divTax, tx)
		default:
			activity, err := convert(tx, opts)
			if err != nil {
				problems = append(problems, err)
				continue
			}
			entries = append(entries, &entry{activity: activity, tx: tx, newest: tx.CreatedAt, sources: 1})
		}
	}

	for _, tax := range saleTax {
		if err := addSaleTax(entries, tax); err != nil {
			problems = append(problems, err)
		}
	}
	for _, tax := range sortedByTime(divTax) {
		if err := addDividendTax(entries, tax); err != nil {
			problems = append(problems, err)
		}
	}

	if len(problems) > 0 {
		return Result{}, errors.Join(problems...)
	}

	cutoff := opts.Now.Add(-opts.MinAge)
	for _, e := range entries {
		if e.newest.After(cutoff) {
			result.Pending += e.sources
			continue
		}
		e.activity.Fee = toUAH(e.fee)
		result.Activities = append(result.Activities, e.activity)
	}

	slices.SortStableFunc(result.Activities, func(a, b ghostfolio.Activity) int {
		if c := a.Date.Compare(b.Date); c != 0 {
			return c
		}
		return strings.Compare(a.Comment, b.Comment)
	})
	return result, nil
}

// convert makes the activity for a transaction that is not a tax.
func convert(tx inzhur.Transaction, opts Options) (ghostfolio.Activity, error) {
	fail := func(format string, args ...any) (ghostfolio.Activity, error) {
		return ghostfolio.Activity{}, fmt.Errorf("mapper: transaction %d (%s): %s",
			tx.ID, tx.Type, fmt.Sprintf(format, args...))
	}

	var activityType ghostfolio.ActivityType
	switch tx.Type {
	case inzhur.TypePurchase, inzhur.TypeGiftAgreement:
		activityType = ghostfolio.TypeBuy
	case inzhur.TypeSale:
		activityType = ghostfolio.TypeSell
	case inzhur.TypeDividendCrediting, inzhur.TypeDistributionCrediting:
		activityType = ghostfolio.TypeDividend
	default:
		return fail("the type is not known")
	}

	if tx.Asset == nil {
		return fail("the asset is missing")
	}
	symbol, err := Symbol(*tx.Asset, opts.SymbolMap)
	if err != nil {
		return fail("%v", err)
	}

	activity := ghostfolio.Activity{
		AccountID:  opts.AccountID,
		Comment:    fmt.Sprintf("%s%d", CommentPrefix, tx.ID),
		Currency:   currency,
		DataSource: ghostfolio.DataSourceManual,
		Date:       tx.CreatedAt.UTC(),
		Symbol:     symbol,
		Type:       activityType,
	}

	if activityType == ghostfolio.TypeDividend {
		if tx.Amount <= 0 {
			return fail("the amount is %d", tx.Amount)
		}
		activity.Quantity = 1
		activity.UnitPrice = toUAH(tx.Amount)
		return activity, nil
	}

	if tx.Contract == nil {
		return fail("the contract is missing")
	}
	if tx.Contract.Quantity <= 0 {
		return fail("the quantity is %v", tx.Contract.Quantity)
	}
	activity.Quantity = tx.Contract.Quantity

	// A gift has no cost. Its unit price stays 0.
	if tx.Type != inzhur.TypeGiftAgreement {
		if tx.Contract.PricePerUnit <= 0 {
			return fail("the unit price is %v", tx.Contract.PricePerUnit)
		}
		activity.UnitPrice = tx.Contract.PricePerUnit
	}
	return activity, nil
}

// addSaleTax adds a sale tax to the fee of the sale with the same contract.
func addSaleTax(entries []*entry, tax inzhur.Transaction) error {
	if tax.Contract == nil {
		return fmt.Errorf("mapper: transaction %d (%s): the contract is missing", tax.ID, tax.Type)
	}

	for _, e := range entries {
		if e.tx.Type == inzhur.TypeSale && e.tx.Contract != nil && e.tx.Contract.ID == tax.Contract.ID {
			e.add(tax)
			return nil
		}
	}
	return fmt.Errorf("mapper: transaction %d (%s): no sale has the contract %d",
		tax.ID, tax.Type, tax.Contract.ID)
}

// addDividendTax adds a dividend tax to the fee of the nearest dividend of
// the same asset. A dividend can have only one tax.
func addDividendTax(entries []*entry, tax inzhur.Transaction) error {
	if tax.Asset == nil {
		return fmt.Errorf("mapper: transaction %d (%s): the asset is missing", tax.ID, tax.Type)
	}

	var (
		best     *entry
		bestDiff time.Duration
		tie      bool
	)
	for _, e := range entries {
		if e.tx.Type != inzhur.TypeDividendCrediting || e.hasTax || e.tx.Asset.ID != tax.Asset.ID {
			continue
		}
		diff := tax.CreatedAt.Sub(e.tx.CreatedAt).Abs()
		if diff > taxWindow {
			continue
		}
		switch {
		case best == nil || diff < bestDiff:
			best, bestDiff, tie = e, diff, false
		case diff == bestDiff:
			tie = true
		}
	}

	if best == nil {
		return fmt.Errorf("mapper: transaction %d (%s): no dividend of asset %d is in the range of %s",
			tax.ID, tax.Type, tax.Asset.ID, taxWindow)
	}
	if tie {
		return fmt.Errorf("mapper: transaction %d (%s): more than one dividend of asset %d is a possible match",
			tax.ID, tax.Type, tax.Asset.ID)
	}

	best.add(tax)
	best.hasTax = true
	return nil
}

// add adds a tax to the fee of the activity.
func (e *entry) add(tax inzhur.Transaction) {
	e.fee += tax.Amount
	e.sources++
	if tax.CreatedAt.After(e.newest) {
		e.newest = tax.CreatedAt
	}
}

// Symbol returns the Ghostfolio symbol of an Inzhur asset.
//
// An entry in symbolMap has priority. If there is none, the symbol of a
// bond is "GF_" and the ISIN. The symbol of a fund is "GF_" and the name
// in capital letters, with hyphens between the words.
func Symbol(asset inzhur.Asset, symbolMap map[int]string) (string, error) {
	if symbol, ok := symbolMap[asset.ID]; ok {
		return symbol, nil
	}

	var name string
	switch asset.Type {
	case inzhur.AssetBond:
		name = asset.ISIN
	case inzhur.AssetFund:
		name = asset.Name
	default:
		return "", fmt.Errorf("asset %d has the unknown type %q", asset.ID, asset.Type)
	}

	words := strings.FieldsFunc(strings.ToUpper(name), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(words) == 0 {
		return "", fmt.Errorf("asset %d (%s) has no name or ISIN", asset.ID, asset.Type)
	}
	return symbolPrefix + strings.Join(words, "-"), nil
}

func sortedByTime(transactions []inzhur.Transaction) []inzhur.Transaction {
	sorted := slices.Clone(transactions)
	slices.SortStableFunc(sorted, func(a, b inzhur.Transaction) int {
		if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return sorted
}

// toUAH converts kopiikas to hryvnias.
func toUAH(kopiikas int64) float64 {
	return float64(kopiikas) / 100
}
