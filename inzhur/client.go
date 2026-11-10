package inzhur

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Default addresses of the API.
const (
	DefaultAuthURL = "https://api.inzhur.reit/auth/api/v1"
	DefaultCoreURL = "https://core.inzhur.reit/api/v1"
)

const (
	refreshCookieName = "refreshToken"
	defaultUserAgent  = "ghostfolio-inzhur (+https://github.com/blobor/ghostfolio-inzhur)"
	defaultTimeout    = 30 * time.Second

	// The API has no pages. One request with a large page size returns the
	// full history.
	transactionsPageSize = 10000

	maxResponseBytes = 16 << 20
	maxErrorBodySize = 512
)

// ErrSessionExpired reports that Inzhur did not accept the token. The user
// must log in again and supply a new refresh token.
var ErrSessionExpired = errors.New("inzhur: the session is not valid, a new login is necessary")

// StatusError reports a response with a status that is not 200.
type StatusError struct {
	Method string
	Path   string
	Status int
	// Body is the start of the response body.
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("inzhur: %s %s: status %d: %s", e.Method, e.Path, e.Status, e.Body)
}

// Client reads data from the Inzhur cabinet API. A Client is safe for use
// by more than one goroutine.
type Client struct {
	httpClient *http.Client
	authURL    string
	coreURL    string
	userAgent  string
}

// Option changes a Client.
type Option func(*Client)

// WithHTTPClient sets the HTTP client. The default client has a timeout of
// 30 seconds.
func WithHTTPClient(c *http.Client) Option {
	return func(client *Client) { client.httpClient = c }
}

// WithBaseURLs sets the addresses of the auth API and the core API.
func WithBaseURLs(authURL, coreURL string) Option {
	return func(client *Client) {
		client.authURL = strings.TrimRight(authURL, "/")
		client.coreURL = strings.TrimRight(coreURL, "/")
	}
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(userAgent string) Option {
	return func(client *Client) { client.userAgent = userAgent }
}

// NewClient returns a Client.
func NewClient(opts ...Option) *Client {
	c := &Client{
		httpClient: &http.Client{Timeout: defaultTimeout},
		authURL:    DefaultAuthURL,
		coreURL:    DefaultCoreURL,
		userAgent:  defaultUserAgent,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Refresh exchanges a refresh token for an access token and a new refresh
// token. It returns ErrSessionExpired if Inzhur does not accept the token.
//
// Inzhur replaces the refresh token on each refresh. Save
// Session.RefreshToken immediately, before all other work.
func (c *Client) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	if refreshToken == "" {
		return Session{}, errors.New("inzhur: refresh token is empty")
	}

	const path = "/auth/refresh"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL+path, http.NoBody)
	if err != nil {
		return Session{}, fmt.Errorf("inzhur: %w", err)
	}
	//nolint:gosec // G124: Cookie attributes have no effect in a request.
	req.AddCookie(&http.Cookie{Name: refreshCookieName, Value: refreshToken})

	resp, err := c.do(req)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Inzhur answers 401 for a refresh token that is not valid. A different
	// status, for example 403 from a firewall, is not a problem of the
	// session.
	if resp.StatusCode == http.StatusUnauthorized {
		return Session{}, ErrSessionExpired
	}
	if resp.StatusCode != http.StatusOK {
		return Session{}, statusError(req, path, resp)
	}

	var body struct {
		AccessToken string `json:"accessToken"`
	}
	if err := decode(resp.Body, &body); err != nil {
		return Session{}, fmt.Errorf("inzhur: decode refresh response: %w", err)
	}
	if body.AccessToken == "" {
		return Session{}, errors.New("inzhur: refresh response has no access token")
	}

	session := Session{AccessToken: body.AccessToken, RefreshToken: refreshToken}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == refreshCookieName && cookie.Value != "" {
			session.RefreshToken = cookie.Value
		}
	}
	return session, nil
}

// Transactions returns the full history of the account, newest first.
func (c *Client) Transactions(ctx context.Context, accessToken string) ([]Transaction, error) {
	path := "/users/transactions?pageSize=" + strconv.Itoa(transactionsPageSize)

	var transactions []Transaction
	if err := c.get(ctx, accessToken, path, &transactions); err != nil {
		return nil, err
	}

	if len(transactions) >= transactionsPageSize {
		return nil, fmt.Errorf("inzhur: the history has %d records or more, thus it is possibly not complete",
			transactionsPageSize)
	}
	return transactions, nil
}

// Holdings returns the current positions of the account.
func (c *Client) Holdings(ctx context.Context, accessToken string) ([]Holding, error) {
	var body struct {
		Assets []struct {
			ID     int       `json:"id"`
			Type   AssetType `json:"type"`
			Prices struct {
				BuyUAH  float64 `json:"buyUAH"`
				SellUAH float64 `json:"sellUAH"`
				NavUAH  float64 `json:"navUAH"`
			} `json:"prices"`
			Details struct {
				Owned          float64 `json:"certificatesOwnedQuantity"`
				SaleRestricted float64 `json:"certificatesSaleRestrictedQuantity"`
			} `json:"details"`
		} `json:"assets"`
	}
	if err := c.get(ctx, accessToken, "/user-assets", &body); err != nil {
		return nil, err
	}

	holdings := make([]Holding, 0, len(body.Assets))
	for _, a := range body.Assets {
		holdings = append(holdings, Holding{
			AssetID:                a.ID,
			Type:                   a.Type,
			Quantity:               a.Details.Owned,
			SaleRestrictedQuantity: a.Details.SaleRestricted,
			BuyPrice:               a.Prices.BuyUAH,
			SellPrice:              a.Prices.SellUAH,
			NAV:                    a.Prices.NavUAH,
		})
	}
	return holdings, nil
}

func (c *Client) get(ctx context.Context, accessToken, path string, v any) error {
	if accessToken == "" {
		return errors.New("inzhur: access token is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.coreURL+path, http.NoBody)
	if err != nil {
		return fmt.Errorf("inzhur: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrSessionExpired
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(req, path, resp)
	}

	if err := decode(resp.Body, v); err != nil {
		return fmt.Errorf("inzhur: decode response of %s: %w", pathOnly(path), err)
	}
	return nil
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inzhur: %s %s: %w", req.Method, req.URL.Path, err)
	}
	return resp, nil
}

func decode(r io.Reader, v any) error {
	return json.NewDecoder(io.LimitReader(r, maxResponseBytes)).Decode(v)
}

func statusError(req *http.Request, path string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	return &StatusError{
		Method: req.Method,
		Path:   pathOnly(path),
		Status: resp.StatusCode,
		Body:   strings.TrimSpace(string(body)),
	}
}

func pathOnly(path string) string {
	p, _, _ := strings.Cut(path, "?")
	return p
}
