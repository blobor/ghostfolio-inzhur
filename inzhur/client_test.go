package inzhur

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestClient returns a client for a test server with the paths /auth
// and /core.
func newTestClient(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return NewClient(
		WithHTTPClient(server.Client()),
		WithBaseURLs(server.URL+"/auth/", server.URL+"/core/"),
	)
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()

	//nolint:gosec // G304: The name is a constant in each test.
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRefresh(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("refreshToken")
		if err != nil || cookie.Value != "old-refresh" {
			t.Errorf("refresh cookie = %v, %v; want old-refresh", cookie, err)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header = %q, want empty", got)
		}
		if r.ContentLength != 0 {
			t.Errorf("request body has %d bytes, want 0", r.ContentLength)
		}

		// The attributes are the same as in the response of Inzhur.
		//nolint:gosec // G124: The test copies the cookie of Inzhur, which has SameSite=None.
		http.SetCookie(w, &http.Cookie{
			Name:     "refreshToken",
			Value:    "new-refresh",
			Path:     "/",
			MaxAge:   14400000,
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteNoneMode,
		})
		_, _ = w.Write([]byte(`{"accessToken":"access-1"}`))
	})
	client := newTestClient(t, mux)

	session, err := client.Refresh(t.Context(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	want := Session{AccessToken: "access-1", RefreshToken: "new-refresh"}
	if session != want {
		t.Errorf("Refresh() = %+v, want %+v", session, want)
	}
}

func TestRefreshKeepsTokenIfThereIsNoNewCookie(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/auth/refresh", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"accessToken":"access-1"}`))
	})
	client := newTestClient(t, mux)

	session, err := client.Refresh(t.Context(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if session.RefreshToken != "old-refresh" {
		t.Errorf("RefreshToken = %q, want old-refresh", session.RefreshToken)
	}
}

func TestRefreshErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		body        string
		wantExpired bool
		wantStatus  int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{}`, wantExpired: true},
		{name: "forbidden", status: http.StatusForbidden, body: "blocked", wantStatus: http.StatusForbidden},
		{name: "server error", status: http.StatusBadGateway, body: "bad gateway", wantStatus: http.StatusBadGateway},
		{name: "no access token", status: http.StatusOK, body: `{}`},
		{name: "not JSON", status: http.StatusOK, body: `<html>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("POST /auth/auth/refresh", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			client := newTestClient(t, mux)

			_, err := client.Refresh(t.Context(), "old-refresh")
			if err == nil {
				t.Fatal("Refresh() error = nil, want an error")
			}
			if got := errors.Is(err, ErrSessionExpired); got != tt.wantExpired {
				t.Errorf("errors.Is(err, ErrSessionExpired) = %v, want %v (err = %v)", got, tt.wantExpired, err)
			}
			if tt.wantStatus != 0 {
				if statusErr, ok := errors.AsType[*StatusError](err); !ok || statusErr.Status != tt.wantStatus {
					t.Errorf("error = %v, want a StatusError with status %d", err, tt.wantStatus)
				}
			}
		})
	}
}

func TestRefreshRejectsEmptyToken(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, http.NewServeMux())

	if _, err := client.Refresh(t.Context(), ""); err == nil {
		t.Fatal("Refresh() error = nil, want an error")
	}
}

func TestTransactions(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /core/users/transactions", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
			t.Errorf("Authorization header = %q, want Bearer access-1", got)
		}
		if got := r.URL.Query().Get("pageSize"); got != "10000" {
			t.Errorf("pageSize = %q, want 10000", got)
		}
		if _, err := r.Cookie("refreshToken"); err == nil {
			t.Error("the request has the refresh cookie")
		}
		_, _ = w.Write(fixture(t, "transactions.json"))
	})
	client := newTestClient(t, mux)

	got, err := client.Transactions(t.Context(), "access-1")
	if err != nil {
		t.Fatalf("Transactions() error = %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("Transactions() returned %d records, want 5", len(got))
	}

	sale := got[0]
	if sale.ID != 1005 || sale.Type != TypeSale || sale.Amount != 204494 {
		t.Errorf("sale = %+v", sale)
	}
	if want := time.Date(2026, 1, 15, 7, 8, 35, 0, time.UTC); !sale.CreatedAt.Equal(want) {
		t.Errorf("sale.CreatedAt = %v, want %v", sale.CreatedAt, want)
	}
	if sale.Asset == nil || *sale.Asset != (Asset{ID: 36, Type: AssetBond, ISIN: "UA4000238976"}) {
		t.Errorf("sale.Asset = %+v", sale.Asset)
	}
	if sale.Contract == nil || sale.Contract.ID != 505 || sale.Contract.Quantity != 2 ||
		sale.Contract.PricePerUnit != 1022.47 || sale.Contract.AmountUAH != 204494 {
		t.Errorf("sale.Contract = %+v", sale.Contract)
	}

	withdrawal := got[1]
	if withdrawal.Type != TypeWithdrawal || withdrawal.Asset != nil || withdrawal.Contract != nil {
		t.Errorf("withdrawal = %+v", withdrawal)
	}

	tax := got[2]
	if tax.Type != TypeDividendTax || tax.DistributionType != DistributionDividend || tax.Contract != nil {
		t.Errorf("dividend tax = %+v", tax)
	}
	if tax.Asset == nil || *tax.Asset != (Asset{ID: 21, Type: AssetFund, Name: "Inzhur REIT"}) {
		t.Errorf("tax.Asset = %+v", tax.Asset)
	}
}

func TestHoldings(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /core/user-assets", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer access-1" {
			t.Errorf("Authorization header = %q, want Bearer access-1", got)
		}
		_, _ = w.Write(fixture(t, "user-assets.json"))
	})
	client := newTestClient(t, mux)

	got, err := client.Holdings(t.Context(), "access-1")
	if err != nil {
		t.Fatalf("Holdings() error = %v", err)
	}

	want := []Holding{
		{AssetID: 21, Type: AssetFund, Quantity: 10, SaleRestrictedQuantity: 3,
			BuyPrice: 11.6468, SellPrice: 11.6178, NAV: 11.6178},
		{AssetID: 36, Type: AssetBond, Quantity: 4, BuyPrice: 1022.47, SellPrice: 1022.47},
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

func TestGetErrors(t *testing.T) {
	t.Parallel()

	t.Run("unauthorized", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("GET /core/users/transactions", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		client := newTestClient(t, mux)

		if _, err := client.Transactions(t.Context(), "access-1"); !errors.Is(err, ErrSessionExpired) {
			t.Fatalf("Transactions() error = %v, want ErrSessionExpired", err)
		}
	})

	t.Run("status error has no query and a short body", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("GET /core/users/transactions", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
		})
		client := newTestClient(t, mux)

		_, err := client.Transactions(t.Context(), "access-1")

		statusErr, ok := errors.AsType[*StatusError](err)
		if !ok {
			t.Fatalf("Transactions() error = %v, want a StatusError", err)
		}
		if statusErr.Path != "/users/transactions" || statusErr.Status != http.StatusInternalServerError {
			t.Errorf("StatusError = %+v", statusErr)
		}
		if len(statusErr.Body) > maxErrorBodySize {
			t.Errorf("StatusError.Body has %d bytes, want %d or fewer", len(statusErr.Body), maxErrorBodySize)
		}
	})

	t.Run("empty access token", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, http.NewServeMux())

		if _, err := client.Holdings(t.Context(), ""); err == nil {
			t.Fatal("Holdings() error = nil, want an error")
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()

		mux := http.NewServeMux()
		mux.HandleFunc("GET /core/user-assets", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"assets":[]}`))
		})
		client := newTestClient(t, mux)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		if _, err := client.Holdings(ctx, "access-1"); !errors.Is(err, context.Canceled) {
			t.Fatalf("Holdings() error = %v, want context.Canceled", err)
		}
	})
}

func TestErrorsDoNotContainTokens(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /auth/auth/refresh", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("GET /core/user-assets", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	client := newTestClient(t, mux)

	_, refreshErr := client.Refresh(t.Context(), "secret-refresh")
	_, getErr := client.Holdings(t.Context(), "secret-access")

	for _, err := range []error{refreshErr, getErr} {
		if err == nil {
			t.Fatal("error = nil, want an error")
		}
		if strings.Contains(err.Error(), "secret-") {
			t.Errorf("error contains a token: %v", err)
		}
	}
}
