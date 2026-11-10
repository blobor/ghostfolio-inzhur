package ghostfolio

import "time"

// ActivityType is the type of an activity.
type ActivityType string

// Activity types that the importer uses.
const (
	TypeBuy      ActivityType = "BUY"
	TypeSell     ActivityType = "SELL"
	TypeDividend ActivityType = "DIVIDEND"
)

// DataSourceManual is the data source of an asset profile that the user
// made in Ghostfolio. A symbol of this data source starts with "GF_".
const DataSourceManual = "MANUAL"

// Activity is one activity for the import.
//
// Ghostfolio skips an activity if all fields but AccountID are equal to an
// activity that it already has.
type Activity struct {
	AccountID  string       `json:"accountId"`
	Comment    string       `json:"comment,omitempty"`
	Currency   string       `json:"currency"`
	DataSource string       `json:"dataSource"`
	Date       time.Time    `json:"date"`
	Fee        float64      `json:"fee"`
	Quantity   float64      `json:"quantity"`
	Symbol     string       `json:"symbol"`
	Type       ActivityType `json:"type"`
	UnitPrice  float64      `json:"unitPrice"`
}
