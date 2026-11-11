package ghostfolio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultUserAgent = "ghostfolio-inzhur (+https://github.com/blobor/ghostfolio-inzhur)"
	defaultTimeout   = 2 * time.Minute

	maxResponseBytes = 64 << 20
	maxErrorBodySize = 1024

	// codeDuplicate is the error code of an activity that Ghostfolio
	// already has.
	codeDuplicate = "IS_DUPLICATE"
)

// ErrUnauthorized reports that Ghostfolio did not accept the security token.
var ErrUnauthorized = errors.New("ghostfolio: the security token is not valid")

// StatusError reports a response with a status that is not successful.
type StatusError struct {
	Method string
	Path   string
	Status int
	// Body is the start of the response body.
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("ghostfolio: %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Client talks to the REST API of one Ghostfolio instance. A Client is
// safe for use by more than one goroutine.
type Client struct {
	httpClient    *http.Client
	baseURL       string
	securityToken string
	userAgent     string

	mu        sync.Mutex
	authToken string
}

// Option changes a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client. The default client has a timeout of
// two minutes.
func WithHTTPClient(c *http.Client) Option {
	return func(client *Client) { client.httpClient = c }
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(userAgent string) Option {
	return func(client *Client) { client.userAgent = userAgent }
}

// NewClient returns a Client for the Ghostfolio instance at baseURL, for
// example "http://ghostfolio:3333". The security token is the token that
// the user uses to log in to Ghostfolio.
func NewClient(baseURL, securityToken string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("ghostfolio: base URL: %w", err)
	}
	isHTTP := u.Scheme == "http" || u.Scheme == "https"
	if !isHTTP || u.Host == "" {
		return nil, fmt.Errorf("ghostfolio: base URL %q must be an http or https address", baseURL)
	}
	if securityToken == "" {
		return nil, errors.New("ghostfolio: security token is empty")
	}

	c := &Client{
		httpClient:    &http.Client{Timeout: defaultTimeout},
		baseURL:       strings.TrimRight(baseURL, "/"),
		securityToken: securityToken,
		userAgent:     defaultUserAgent,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// ImportResult is the result of an import.
type ImportResult struct {
	// Imported is the number of activities that Ghostfolio accepted. In a
	// dry run, Ghostfolio does not store them.
	Imported int
	// Duplicates is the number of activities that Ghostfolio already has.
	Duplicates int
}

// Import sends the activities to Ghostfolio. Ghostfolio skips an activity
// that is equal to one it already has.
//
// In a dry run Ghostfolio examines the activities but stores nothing.
func (c *Client) Import(ctx context.Context, activities []Activity, dryRun bool) (ImportResult, error) {
	if len(activities) == 0 {
		return ImportResult{}, nil
	}

	path := "/api/v1/import"
	if dryRun {
		path += "?dryRun=true"
	}

	request := struct {
		Activities []Activity `json:"activities"`
	}{Activities: activities}

	var response struct {
		Activities []struct {
			Error *struct {
				Code string `json:"code"`
			} `json:"error"`
		} `json:"activities"`
	}
	if err := c.call(ctx, http.MethodPost, path, request, &response); err != nil {
		return ImportResult{}, err
	}

	var result ImportResult
	for _, a := range response.Activities {
		switch {
		case a.Error == nil:
			result.Imported++
		case a.Error.Code == codeDuplicate:
			result.Duplicates++
		default:
			return ImportResult{}, fmt.Errorf("ghostfolio: import returned the unknown error code %q", a.Error.Code)
		}
	}

	returned := len(response.Activities)
	if dryRun {
		// A dry run returns all activities. A duplicate has an error code.
		if returned != len(activities) {
			return ImportResult{}, fmt.Errorf("ghostfolio: dry run returned %d activities, want %d",
				returned, len(activities))
		}
		return result, nil
	}

	// An import returns only the activities that it stored. It does not
	// return the duplicates.
	if result.Duplicates > 0 || returned > len(activities) {
		return ImportResult{}, fmt.Errorf("ghostfolio: import returned %d activities (%d duplicates) for %d that were sent",
			returned, result.Duplicates, len(activities))
	}
	result.Duplicates = len(activities) - returned
	return result, nil
}

// activitiesPageSize is the number of activities in one request.
const activitiesPageSize = 500

// StoredActivity is an activity that Ghostfolio has.
type StoredActivity struct {
	// ID is the ID of the activity in Ghostfolio.
	ID string
	Activity
}

// Activities returns all activities of one account.
func (c *Client) Activities(ctx context.Context, accountID string) ([]StoredActivity, error) {
	if accountID == "" {
		return nil, errors.New("ghostfolio: account ID is empty")
	}

	var stored []StoredActivity
	for {
		query := url.Values{}
		query.Set("accounts", accountID)
		query.Set("take", strconv.Itoa(activitiesPageSize))
		query.Set("skip", strconv.Itoa(len(stored)))

		var response struct {
			Count      int `json:"count"`
			Activities []struct {
				ID           string       `json:"id"`
				AccountID    string       `json:"accountId"`
				Comment      string       `json:"comment"`
				Date         time.Time    `json:"date"`
				Fee          float64      `json:"fee"`
				Quantity     float64      `json:"quantity"`
				Type         ActivityType `json:"type"`
				UnitPrice    float64      `json:"unitPrice"`
				AssetProfile struct {
					Symbol     string `json:"symbol"`
					DataSource string `json:"dataSource"`
					Currency   string `json:"currency"`
				} `json:"assetProfile"`
			} `json:"activities"`
		}
		if err := c.call(ctx, http.MethodGet, "/api/v1/activities?"+query.Encode(), nil, &response); err != nil {
			return nil, err
		}

		for _, a := range response.Activities {
			stored = append(stored, StoredActivity{
				ID: a.ID,
				Activity: Activity{
					AccountID:  a.AccountID,
					Comment:    a.Comment,
					Currency:   a.AssetProfile.Currency,
					DataSource: a.AssetProfile.DataSource,
					Date:       a.Date,
					Fee:        a.Fee,
					Quantity:   a.Quantity,
					Symbol:     a.AssetProfile.Symbol,
					Type:       a.Type,
					UnitPrice:  a.UnitPrice,
				},
			})
		}

		if len(stored) >= response.Count {
			return stored, nil
		}
		if len(response.Activities) == 0 {
			return nil, fmt.Errorf("ghostfolio: read %d activities, but the account has %d", len(stored), response.Count)
		}
	}
}

// Update replaces the values of the activity with the given ID.
func (c *Client) Update(ctx context.Context, id string, activity Activity) error {
	if id == "" {
		return errors.New("ghostfolio: activity ID is empty")
	}

	request := struct {
		ID string `json:"id"`
		Activity
	}{ID: id, Activity: activity}

	return c.call(ctx, http.MethodPut, "/api/v1/activities/"+url.PathEscape(id), request, nil)
}

// Holding is the current position in one symbol.
type Holding struct {
	Symbol     string
	DataSource string
	Quantity   float64
}

// Holdings returns the current positions of the user, for all accounts.
// A position that the user sold completely has the quantity 0.
func (c *Client) Holdings(ctx context.Context) ([]Holding, error) {
	var response struct {
		Holdings []struct {
			Quantity     float64 `json:"quantity"`
			AssetProfile struct {
				Symbol     string `json:"symbol"`
				DataSource string `json:"dataSource"`
			} `json:"assetProfile"`
		} `json:"holdings"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/portfolio/holdings", nil, &response); err != nil {
		return nil, err
	}

	holdings := make([]Holding, 0, len(response.Holdings))
	for i, h := range response.Holdings {
		if h.AssetProfile.Symbol == "" || h.AssetProfile.DataSource == "" {
			return nil, fmt.Errorf("ghostfolio: holding %d has no symbol or data source", i)
		}
		holdings = append(holdings, Holding{
			Symbol:     h.AssetProfile.Symbol,
			DataSource: h.AssetProfile.DataSource,
			Quantity:   h.Quantity,
		})
	}
	return holdings, nil
}

// HasSymbol tells if Ghostfolio has an asset profile for the symbol.
//
// A dry run of the import does not report a missing asset profile. Use
// HasSymbol before the import if Ghostfolio must not make new profiles.
func (c *Client) HasSymbol(ctx context.Context, dataSource, symbol string) (bool, error) {
	path := "/api/v1/symbol/" + url.PathEscape(dataSource) + "/" + url.PathEscape(symbol)

	err := c.call(ctx, http.MethodGet, path, nil, nil)

	if statusErr, ok := errors.AsType[*StatusError](err); ok && statusErr.Status == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// call does a request with the token from the login. If Ghostfolio does
// not accept the token, call does the login again, one time.
func (c *Client) call(ctx context.Context, method, path string, in, out any) error {
	for attempt := 0; ; attempt++ {
		token, err := c.login(ctx)
		if err != nil {
			return err
		}

		err = c.do(ctx, method, path, token, in, out)

		statusErr, ok := errors.AsType[*StatusError](err)
		isRejected := ok && statusErr.Status == http.StatusUnauthorized
		if attempt == 0 && isRejected {
			c.forget(token)
			continue
		}
		return err
	}
}

// login returns the token for the API calls. It does the login only if
// the client has no token.
func (c *Client) login(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.authToken != "" {
		return c.authToken, nil
	}

	request := struct {
		AccessToken string `json:"accessToken"`
	}{AccessToken: c.securityToken}

	var response struct {
		AuthToken string `json:"authToken"`
	}
	err := c.do(ctx, http.MethodPost, "/api/v1/auth/anonymous", "", request, &response)

	if statusErr, ok := errors.AsType[*StatusError](err); ok {
		isRejected := statusErr.Status == http.StatusForbidden || statusErr.Status == http.StatusUnauthorized
		if isRejected {
			return "", ErrUnauthorized
		}
	}
	if err != nil {
		return "", err
	}
	if response.AuthToken == "" {
		return "", errors.New("ghostfolio: login response has no token")
	}

	c.authToken = response.AuthToken
	return c.authToken, nil
}

// forget removes the token if it is still the current one.
func (c *Client) forget(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.authToken == token {
		c.authToken = ""
	}
}

func (c *Client) do(ctx context.Context, method, path, token string, in, out any) error {
	var body io.Reader = http.NoBody
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("ghostfolio: encode request: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return fmt.Errorf("ghostfolio: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ghostfolio: %s %s: %w", method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
		return &StatusError{
			Method: method,
			Path:   req.URL.Path,
			Status: resp.StatusCode,
			Body:   strings.TrimSpace(string(data)),
		}
	}

	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(out); err != nil {
		return fmt.Errorf("ghostfolio: decode response of %s: %w", req.URL.Path, err)
	}
	return nil
}
