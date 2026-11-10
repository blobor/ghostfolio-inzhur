package main

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Names of the environment variables.
const (
	envInzhurRefreshToken    = "INZHUR_REFRESH_TOKEN"
	envGhostfolioURL         = "GHOSTFOLIO_URL"
	envGhostfolioAccessToken = "GHOSTFOLIO_ACCESS_TOKEN"
	envGhostfolioAccountID   = "GHOSTFOLIO_ACCOUNT_ID"
	envDataDir               = "DATA_DIR"
	envSymbolMap             = "SYMBOL_MAP"
	envDryRun                = "DRY_RUN"
)

// config holds the settings for one sync.
type config struct {
	// InzhurRefreshToken is the start token. The program uses it only when
	// its value is new. After that, the token in DataDir is used.
	InzhurRefreshToken string

	GhostfolioURL         *url.URL
	GhostfolioAccessToken string
	GhostfolioAccountID   string

	// DataDir is the directory for the state file.
	DataDir string

	// SymbolMap overrides the Ghostfolio symbol for an Inzhur asset ID.
	SymbolMap map[int]string

	// DryRun stops the program before it writes to Ghostfolio.
	DryRun bool
}

// loadConfig reads the configuration with getenv. It reports all problems
// in one error.
func loadConfig(getenv func(string) string) (config, error) {
	var (
		cfg  config
		errs []error
	)

	required := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is not set", name))
		}
		return v
	}

	cfg.InzhurRefreshToken = required(envInzhurRefreshToken)
	cfg.GhostfolioAccessToken = required(envGhostfolioAccessToken)
	cfg.GhostfolioAccountID = required(envGhostfolioAccountID)
	cfg.DataDir = required(envDataDir)

	if raw := required(envGhostfolioURL); raw != "" {
		u, err := parseBaseURL(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", envGhostfolioURL, err))
		}
		cfg.GhostfolioURL = u
	}

	symbols, err := parseSymbolMap(getenv(envSymbolMap))
	if err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", envSymbolMap, err))
	}
	cfg.SymbolMap = symbols

	if raw := strings.TrimSpace(getenv(envDryRun)); raw != "" {
		dryRun, err := strconv.ParseBool(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %q is not a boolean", envDryRun, raw))
		}
		cfg.DryRun = dryRun
	}

	if len(errs) > 0 {
		return config{}, errors.Join(errs...)
	}
	return cfg, nil
}

func parseBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("host is empty")
	}
	return u, nil
}

// parseSymbolMap parses a list such as "21=GF_INZHUR-REIT,36=GF_UA4000238976".
// The key is the Inzhur asset ID. The value is the Ghostfolio symbol.
func parseSymbolMap(raw string) (map[int]string, error) {
	symbols := make(map[int]string)

	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		key, symbol, ok := strings.Cut(pair, "=")
		key, symbol = strings.TrimSpace(key), strings.TrimSpace(symbol)
		if !ok || symbol == "" {
			return nil, fmt.Errorf("%q must have the form <asset id>=<symbol>", pair)
		}

		id, err := strconv.Atoi(key)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q: asset ID must be a positive integer", pair)
		}
		if _, dup := symbols[id]; dup {
			return nil, fmt.Errorf("asset ID %d occurs more than once", id)
		}
		symbols[id] = symbol
	}

	return symbols, nil
}
