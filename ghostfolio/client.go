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
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
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
	if got := result.Imported + result.Duplicates; got != len(activities) {
		return ImportResult{}, fmt.Errorf("ghostfolio: import returned %d activities, want %d", got, len(activities))
	}
	return result, nil
}

// Holding is the current position in one symbol.
type Holding struct {
	Symbol     string  `json:"symbol"`
	DataSource string  `json:"dataSource"`
	Quantity   float64 `json:"quantity"`
}

// Holdings returns the current positions of the user, for all accounts.
func (c *Client) Holdings(ctx context.Context) ([]Holding, error) {
	var response struct {
		Holdings []Holding `json:"holdings"`
	}
	if err := c.call(ctx, http.MethodGet, "/api/v1/portfolio/holdings", nil, &response); err != nil {
		return nil, err
	}
	return response.Holdings, nil
}

// HasSymbol tells if Ghostfolio has an asset profile for the symbol.
//
// A dry run of the import does not report a missing asset profile. Use
// HasSymbol before the import if Ghostfolio must not make new profiles.
func (c *Client) HasSymbol(ctx context.Context, dataSource, symbol string) (bool, error) {
	path := "/api/v1/symbol/" + url.PathEscape(dataSource) + "/" + url.PathEscape(symbol)

	err := c.call(ctx, http.MethodGet, path, nil, nil)

	var statusErr *StatusError
	if errors.As(err, &statusErr) && statusErr.Status == http.StatusNotFound {
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

		var statusErr *StatusError
		if attempt == 0 && errors.As(err, &statusErr) && statusErr.Status == http.StatusUnauthorized {
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

	var statusErr *StatusError
	if errors.As(err, &statusErr) &&
		(statusErr.Status == http.StatusForbidden || statusErr.Status == http.StatusUnauthorized) {
		return "", ErrUnauthorized
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
