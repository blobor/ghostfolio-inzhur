package inzhur

import "time"

// TransactionType is the value of the field "type" of a transaction.
type TransactionType string

// Transaction types that the API returned on 2026-11-10.
const (
	TypePurchase              TransactionType = "Purchase"
	TypeSale                  TransactionType = "Sale"
	TypeSaleTax               TransactionType = "Sale tax"
	TypeDividendCrediting     TransactionType = "Dividend crediting"
	TypeDividendTax           TransactionType = "Dividend tax"
	TypeDistributionCrediting TransactionType = "Distribution crediting"
	TypeGiftAgreement         TransactionType = "Gift Agreement"
	TypeDeposit               TransactionType = "Deposit"
	TypeWithdrawal            TransactionType = "Withdrawal"
)

// Distribution types.
const (
	DistributionDividend = "DIVIDEND"
	DistributionCoupon   = "COUPON"
)

// AssetType is the type of an asset.
type AssetType string

// Asset types.
const (
	AssetFund AssetType = "fund"
	AssetBond AssetType = "bond"
)

// Transaction is one record of the account history.
//
// The API also returns the bank account of a withdrawal. The client does
// not decode it, because it is personal data.
type Transaction struct {
	// ID is stable. Use it to identify the transaction.
	ID int64 `json:"id"`
	// Amount is in kopiikas (UAH × 100).
	Amount int64           `json:"amount"`
	Type   TransactionType `json:"type"`
	// DistributionType is DistributionDividend, DistributionCoupon or empty.
	DistributionType string    `json:"distributionType"`
	CreatedAt        time.Time `json:"externalCreatedAt"`
	// Asset is nil for a deposit and a withdrawal.
	Asset *Asset `json:"asset"`
	// Contract is nil if the transaction is not a trade.
	Contract *Contract `json:"contract"`
}

// Asset identifies a fund or a bond.
type Asset struct {
	ID   int       `json:"id"`
	Type AssetType `json:"type"`
	// Name is set for a fund.
	Name string `json:"name"`
	// ISIN is set for a bond.
	ISIN string `json:"isin"`
}

// Contract is the trade of a transaction.
type Contract struct {
	ID     int64  `json:"id"`
	Number string `json:"number"`
	// Quantity is the number of units.
	Quantity float64 `json:"quantity"`
	// PricePerUnit is in UAH.
	PricePerUnit float64 `json:"pricePerUnit"`
	// AmountUAH is in kopiikas (UAH × 100).
	AmountUAH int64 `json:"amountUAH"`
	// AmountUSD is in cents (USD × 100).
	AmountUSD int64     `json:"amountUSD"`
	CreatedAt time.Time `json:"externalCreatedAt"`
}

// Holding is the current position in one asset.
type Holding struct {
	AssetID int
	Type    AssetType
	// Quantity is the number of units that the user owns.
	Quantity float64
}

// Session is the result of a refresh.
type Session struct {
	// AccessToken gives access to the data for a short time.
	AccessToken string
	// RefreshToken is the token for the next refresh. The previous refresh
	// token must not be used again.
	RefreshToken string
}
