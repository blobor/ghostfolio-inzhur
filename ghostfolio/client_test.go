package ghostfolio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const securityToken = "security-token"

// fakeServer is a Ghostfolio API for the tests.
type fakeServer struct {
	logins atomic.Int32
	// authToken is the token that the login returns.
	authToken string
}

func newFakeServer(t *testing.T, mux *http.ServeMux) (*fakeServer, *Client) {
	t.Helper()

	fake := &fakeServer{authToken: "auth-1"}
	mux.HandleFunc("POST /api/v1/auth/anonymous", func(w http.ResponseWriter, r *http.Request) {
		fake.logins.Add(1)

		var body struct {
			AccessToken string `json:"accessToken"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AccessToken != securityToken {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"authToken": fake.authToken})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client, err := NewClient(server.URL+"/", securityToken, WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return fake, client
}

func testActivities() []Activity {
	return []Activity{
		{
			AccountID:  "account-1",
			Comment:    "inzhur:1001",
			Currency:   "UAH",
			DataSource: DataSourceManual,
			Date:       time.Date(2026, 1, 5, 2, 34, 38, 0, time.UTC),
			Quantity:   10,
			Symbol:     "GF_INZHUR-REIT",
			Type:       TypeBuy,
			UnitPrice:  11.6178,
		},
		{
			AccountID:  "account-1",
			Comment:    "inzhur:1004",
			Currency:   "UAH",
			DataSource: DataSourceManual,
			Date:       time.Date(2026, 1, 10, 22, 19, 0, 0, time.UTC),
			Fee:        1.8,
			Quantity:   1,
			Symbol:     "GF_INZHUR-REIT",
			Type:       TypeDividend,
			UnitPrice:  10,
		},
	}
}

func TestImport(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer auth-1" {
			t.Errorf("Authorization header = %q, want Bearer auth-1", got)
		}
		if got := r.URL.Query().Get("dryRun"); got != "" {
			t.Errorf("dryRun = %q, want no parameter", got)
		}

		body, _ := io.ReadAll(r.Body)
		want := `{"activities":[` +
			`{"accountId":"account-1","comment":"inzhur:1001","currency":"UAH","dataSource":"MANUAL",` +
			`"date":"2026-01-05T02:34:38Z","fee":0,"quantity":10,"symbol":"GF_INZHUR-REIT","type":"BUY","unitPrice":11.6178},` +
			`{"accountId":"account-1","comment":"inzhur:1004","currency":"UAH","dataSource":"MANUAL",` +
			`"date":"2026-01-10T22:19:00Z","fee":1.8,"quantity":1,"symbol":"GF_INZHUR-REIT","type":"DIVIDEND","unitPrice":10}]}`
		if string(body) != want {
			t.Errorf("request body:\n got %s\nwant %s", body, want)
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"activities":[{"id":"a"},{"id":"b","error":{"code":"IS_DUPLICATE"}}]}`))
	})
	_, client := newFakeServer(t, mux)

	result, err := client.Import(context.Background(), testActivities(), false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if want := (ImportResult{Imported: 1, Duplicates: 1}); result != want {
		t.Errorf("Import() = %+v, want %+v", result, want)
	}
}

func TestImportDryRun(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("dryRun"); got != "true" {
			t.Errorf("dryRun = %q, want true", got)
		}
		_, _ = w.Write([]byte(`{"activities":[{},{}]}`))
	})
	_, client := newFakeServer(t, mux)

	result, err := client.Import(context.Background(), testActivities(), true)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if want := (ImportResult{Imported: 2}); result != want {
		t.Errorf("Import() = %+v, want %+v", result, want)
	}
}

func TestImportWithoutActivitiesDoesNoRequest(t *testing.T) {
	fake, client := newFakeServer(t, http.NewServeMux())

	if _, err := client.Import(context.Background(), nil, false); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if got := fake.logins.Load(); got != 0 {
		t.Errorf("logins = %d, want 0", got)
	}
}

func TestImportErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"validation error", http.StatusBadRequest,
			`{"error":"Bad Request","message":["activities.0.symbol (\"X\") is not valid"]}`, "is not valid"},
		{"unknown error code", http.StatusCreated,
			`{"activities":[{},{"error":{"code":"IS_BROKEN"}}]}`, "IS_BROKEN"},
		{"wrong number of activities", http.StatusCreated, `{"activities":[{}]}`, "returned 1 activities, want 2"},
		{"not JSON", http.StatusCreated, `<html>`, "decode response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, client := newFakeServer(t, mux)

			_, err := client.Import(context.Background(), testActivities(), false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Import() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestHoldings(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer auth-1" {
			t.Errorf("Authorization header = %q, want Bearer auth-1", got)
		}
		_, _ = w.Write([]byte(`{"holdings":[
			{"quantity":10,"activitiesCount":2,"assetProfile":{"symbol":"GF_INZHUR-REIT","dataSource":"MANUAL","currency":"UAH"}},
			{"quantity":0,"activitiesCount":5,"assetProfile":{"symbol":"GF_UA4000239016","dataSource":"MANUAL"}},
			{"quantity":1.5,"assetProfile":{"symbol":"VWCE.DE","dataSource":"YAHOO"}}
		]}`))
	})
	_, client := newFakeServer(t, mux)

	got, err := client.Holdings(context.Background())
	if err != nil {
		t.Fatalf("Holdings() error = %v", err)
	}

	want := []Holding{
		{Symbol: "GF_INZHUR-REIT", DataSource: "MANUAL", Quantity: 10},
		{Symbol: "GF_UA4000239016", DataSource: "MANUAL", Quantity: 0},
		{Symbol: "VWCE.DE", DataSource: "YAHOO", Quantity: 1.5},
	}
	if len(got) != len(want) {
		t.Fatalf("Holdings() returned %d records, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Holdings()[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestHoldingsRejectsAHoldingWithoutSymbol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, _ *http.Request) {
		// This is the format of an older Ghostfolio version.
		_, _ = w.Write([]byte(`{"holdings":[{"symbol":"GF_INZHUR-REIT","dataSource":"MANUAL","quantity":10}]}`))
	})
	_, client := newFakeServer(t, mux)

	if _, err := client.Holdings(context.Background()); err == nil {
		t.Fatal("Holdings() error = nil, want an error")
	}
}

func TestHasSymbol(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/symbol/MANUAL/GF_INZHUR-REIT", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"symbol":"GF_INZHUR-REIT","dataSource":"MANUAL","currency":"UAH"}`))
	})
	mux.HandleFunc("GET /api/v1/symbol/MANUAL/GF_BROKEN", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	})
	_, client := newFakeServer(t, mux)

	tests := []struct {
		symbol  string
		want    bool
		wantErr bool
	}{
		{symbol: "GF_INZHUR-REIT", want: true},
		{symbol: "GF_MISSING", want: false},
		{symbol: "GF_BROKEN", wantErr: true},
	}
	for _, tt := range tests {
		got, err := client.HasSymbol(context.Background(), DataSourceManual, tt.symbol)
		if (err != nil) != tt.wantErr {
			t.Errorf("HasSymbol(%s) error = %v, wantErr %v", tt.symbol, err, tt.wantErr)
		}
		if got != tt.want {
			t.Errorf("HasSymbol(%s) = %v, want %v", tt.symbol, got, tt.want)
		}
	}
}

func TestLoginIsDoneOneTime(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"holdings":[]}`))
	})
	fake, client := newFakeServer(t, mux)

	for range 3 {
		if _, err := client.Holdings(context.Background()); err != nil {
			t.Fatalf("Holdings() error = %v", err)
		}
	}
	if got := fake.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

func TestLoginIsDoneAgainAfterUnauthorized(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"holdings":[]}`))
	})
	fake, client := newFakeServer(t, mux)

	if _, err := client.Holdings(context.Background()); err != nil {
		t.Fatalf("Holdings() error = %v", err)
	}
	if got := fake.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestUnauthorizedTwoTimesIsAnError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
	fake, client := newFakeServer(t, mux)

	_, err := client.Holdings(context.Background())

	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.Status != http.StatusUnauthorized {
		t.Fatalf("Holdings() error = %v, want a StatusError with status 401", err)
	}
	if got := fake.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestBadSecurityToken(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	mux.HandleFunc("POST /api/v1/auth/anonymous", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	})

	client, err := NewClient(server.URL, "bad-secret-token", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.Holdings(context.Background())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Holdings() error = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "bad-secret-token") {
		t.Errorf("error contains the token: %v", err)
	}
}

func TestNewClientRejectsBadInput(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		token   string
	}{
		{"no scheme", "ghostfolio:3333", securityToken},
		{"no host", "http://", securityToken},
		{"empty token", "http://ghostfolio:3333", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(tt.baseURL, tt.token); err == nil {
				t.Fatal("NewClient() error = nil, want an error")
			}
		})
	}
}
