package ghostfolio

import (
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	t.Parallel()

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

		// Ghostfolio stored the first activity. It does not return the
		// second activity, which is a duplicate.
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"activities":[{"id":"a"}]}`))
	})
	_, client := newFakeServer(t, mux)

	result, err := client.Import(t.Context(), testActivities(), false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if want := (ImportResult{Imported: 1, Duplicates: 1}); result != want {
		t.Errorf("Import() = %+v, want %+v", result, want)
	}
}

func TestImportDryRun(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("dryRun"); got != "true" {
			t.Errorf("dryRun = %q, want true", got)
		}
		_, _ = w.Write([]byte(`{"activities":[{},{"error":{"code":"IS_DUPLICATE"}}]}`))
	})
	_, client := newFakeServer(t, mux)

	result, err := client.Import(t.Context(), testActivities(), true)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if want := (ImportResult{Imported: 1, Duplicates: 1}); result != want {
		t.Errorf("Import() = %+v, want %+v", result, want)
	}
}

func TestImportOfOnlyDuplicatesReturnsNoActivities(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"activities":[]}`))
	})
	_, client := newFakeServer(t, mux)

	result, err := client.Import(t.Context(), testActivities(), false)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if want := (ImportResult{Duplicates: 2}); result != want {
		t.Errorf("Import() = %+v, want %+v", result, want)
	}
}

func TestDryRunMustReturnAllActivities(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"activities":[{}]}`))
	})
	_, client := newFakeServer(t, mux)

	_, err := client.Import(t.Context(), testActivities(), true)
	if err == nil || !strings.Contains(err.Error(), "dry run returned 1 activities, want 2") {
		t.Fatalf("Import() error = %v, want an error about the number of activities", err)
	}
}

func TestImportWithoutActivitiesDoesNoRequest(t *testing.T) {
	t.Parallel()

	fake, client := newFakeServer(t, http.NewServeMux())

	if _, err := client.Import(t.Context(), nil, false); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if got := fake.logins.Load(); got != 0 {
		t.Errorf("logins = %d, want 0", got)
	}
}

func TestImportErrors(t *testing.T) {
	t.Parallel()

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
		{"too many activities", http.StatusCreated, `{"activities":[{},{},{}]}`, "returned 3 activities"},
		{"duplicate in the result of an import", http.StatusCreated,
			`{"activities":[{},{"error":{"code":"IS_DUPLICATE"}}]}`, "1 duplicates"},
		{"not JSON", http.StatusCreated, `<html>`, "decode response"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/v1/import", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			_, client := newFakeServer(t, mux)

			_, err := client.Import(t.Context(), testActivities(), false)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Import() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestActivitiesReadsAllPages(t *testing.T) {
	t.Parallel()

	const total = activitiesPageSize + 2

	var requests []string
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/activities", func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		if got := r.URL.Query().Get("accounts"); got != "account-1" {
			t.Errorf("accounts = %q, want account-1", got)
		}

		skip, _ := strconv.Atoi(r.URL.Query().Get("skip"))
		take, _ := strconv.Atoi(r.URL.Query().Get("take"))

		type profile struct {
			Symbol     string `json:"symbol"`
			DataSource string `json:"dataSource"`
			Currency   string `json:"currency"`
		}
		type item struct {
			ID           string  `json:"id"`
			AccountID    string  `json:"accountId"`
			Comment      string  `json:"comment"`
			Date         string  `json:"date"`
			Fee          float64 `json:"fee"`
			Quantity     float64 `json:"quantity"`
			Type         string  `json:"type"`
			UnitPrice    float64 `json:"unitPrice"`
			AssetProfile profile `json:"assetProfile"`
		}
		var items []item
		for i := skip; i < total && i < skip+take; i++ {
			items = append(items, item{
				ID:           "id-" + strconv.Itoa(i),
				AccountID:    "account-1",
				Comment:      "inzhur:" + strconv.Itoa(1000+i),
				Date:         "2026-01-05T02:34:38.000Z",
				Fee:          1.5,
				Quantity:     10,
				Type:         "BUY",
				UnitPrice:    11.6178,
				AssetProfile: profile{Symbol: "GF_INZHUR-REIT", DataSource: "MANUAL", Currency: "UAH"},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"count": total, "activities": items})
	})
	_, client := newFakeServer(t, mux)

	got, err := client.Activities(t.Context(), "account-1")
	if err != nil {
		t.Fatalf("Activities() error = %v", err)
	}
	if len(got) != total || len(requests) != 2 {
		t.Fatalf("Activities() returned %d records in %d requests, want %d in 2", len(got), len(requests), total)
	}

	want := StoredActivity{
		ID: "id-501",
		Activity: Activity{
			AccountID:  "account-1",
			Comment:    "inzhur:1501",
			Currency:   "UAH",
			DataSource: DataSourceManual,
			Date:       time.Date(2026, 1, 5, 2, 34, 38, 0, time.UTC),
			Fee:        1.5,
			Quantity:   10,
			Symbol:     "GF_INZHUR-REIT",
			Type:       TypeBuy,
			UnitPrice:  11.6178,
		},
	}
	if last := got[total-1]; last != want {
		t.Errorf("last activity = %+v, want %+v", last, want)
	}
}

func TestActivitiesOfAnEmptyAccount(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/activities", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"activities":[],"count":0}`))
	})
	_, client := newFakeServer(t, mux)

	got, err := client.Activities(t.Context(), "account-1")
	if err != nil || len(got) != 0 {
		t.Fatalf("Activities() = %v, %v; want no activities and no error", got, err)
	}
}

func TestUpdate(t *testing.T) {
	t.Parallel()

	var body map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/v1/activities/id-1", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		_, _ = w.Write([]byte(`{"id":"id-1"}`))
	})
	_, client := newFakeServer(t, mux)

	activity := Activity{
		AccountID:  "account-1",
		Comment:    "inzhur:1001",
		Currency:   "UAH",
		DataSource: DataSourceManual,
		Date:       time.Date(2026, 1, 5, 22, 0, 0, 0, time.UTC),
		Fee:        1.5,
		Quantity:   10,
		Symbol:     "GF_INZHUR-REIT",
		Type:       TypeBuy,
		UnitPrice:  11.6178,
	}
	if err := client.Update(t.Context(), "id-1", activity); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	want := map[string]any{
		"id":         "id-1",
		"accountId":  "account-1",
		"comment":    "inzhur:1001",
		"currency":   "UAH",
		"dataSource": "MANUAL",
		"date":       "2026-01-05T22:00:00Z",
		"fee":        1.5,
		"quantity":   10.0,
		"symbol":     "GF_INZHUR-REIT",
		"type":       "BUY",
		"unitPrice":  11.6178,
	}
	if !maps.Equal(body, want) {
		t.Errorf("request body:\n got %v\nwant %v", body, want)
	}
}

func TestUpdateOfAMissingActivityIsAnError(t *testing.T) {
	t.Parallel()

	_, client := newFakeServer(t, http.NewServeMux())

	err := client.Update(t.Context(), "id-missing", Activity{})

	if statusErr, ok := errors.AsType[*StatusError](err); !ok || statusErr.Status != http.StatusNotFound {
		t.Errorf("Update() error = %v, want status 404", err)
	}
	if err := client.Update(t.Context(), "", Activity{}); err == nil {
		t.Error("Update() with an empty ID: no error")
	}
}

func TestHoldings(t *testing.T) {
	t.Parallel()

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

	got, err := client.Holdings(t.Context())
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
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, _ *http.Request) {
		// This is the format of an older Ghostfolio version.
		_, _ = w.Write([]byte(`{"holdings":[{"symbol":"GF_INZHUR-REIT","dataSource":"MANUAL","quantity":10}]}`))
	})
	_, client := newFakeServer(t, mux)

	if _, err := client.Holdings(t.Context()); err == nil {
		t.Fatal("Holdings() error = nil, want an error")
	}
}

func TestHasSymbol(t *testing.T) {
	t.Parallel()

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
		t.Run(tt.symbol, func(t *testing.T) {
			t.Parallel()

			got, err := client.HasSymbol(t.Context(), DataSourceManual, tt.symbol)
			if (err != nil) != tt.wantErr {
				t.Errorf("HasSymbol() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("HasSymbol() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLoginIsDoneOneTime(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"holdings":[]}`))
	})
	fake, client := newFakeServer(t, mux)

	for range 3 {
		if _, err := client.Holdings(t.Context()); err != nil {
			t.Fatalf("Holdings() error = %v", err)
		}
	}
	if got := fake.logins.Load(); got != 1 {
		t.Errorf("logins = %d, want 1", got)
	}
}

func TestLoginIsDoneAgainAfterUnauthorized(t *testing.T) {
	t.Parallel()

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

	if _, err := client.Holdings(t.Context()); err != nil {
		t.Fatalf("Holdings() error = %v", err)
	}
	if got := fake.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestUnauthorizedTwoTimesIsAnError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/portfolio/holdings", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
	fake, client := newFakeServer(t, mux)

	_, err := client.Holdings(t.Context())

	if statusErr, ok := errors.AsType[*StatusError](err); !ok || statusErr.Status != http.StatusUnauthorized {
		t.Fatalf("Holdings() error = %v, want a StatusError with status 401", err)
	}
	if got := fake.logins.Load(); got != 2 {
		t.Errorf("logins = %d, want 2", got)
	}
}

func TestBadSecurityToken(t *testing.T) {
	t.Parallel()

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

	_, err = client.Holdings(t.Context())
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Holdings() error = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), "bad-secret-token") {
		t.Errorf("error contains the token: %v", err)
	}
}

func TestNewClientRejectsBadInput(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

			if _, err := NewClient(tt.baseURL, tt.token); err == nil {
				t.Fatal("NewClient() error = nil, want an error")
			}
		})
	}
}
