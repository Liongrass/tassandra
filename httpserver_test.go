package tassandra_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	tassandra "github.com/liongrass/tassandra"
	"github.com/liongrass/tassandra/oracle"
	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore"
)

// setupHTTPServer creates a test HTTPServer backed by a live oracle (with
// mock feeds) and a temp SQLiteStore.
func setupHTTPServer(t *testing.T,
	feedValues map[pricefeed.FiatCurrency]uint64) (
	*tassandra.HTTPServer, pricestore.PriceStore) {

	t.Helper()

	store, err := pricestore.NewSQLiteStore(
		filepath.Join(t.TempDir(), "test.db"),
	)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	o, err := oracle.New(oracle.Config{
		Feeds: []oracle.FeedConfig{
			{
				Feed:       &mockFeed{name: "testex", values: feedValues},
				Currencies: currenciesFrom(feedValues),
			},
		},
		Store:        store,
		PollInterval: time.Minute,
		FetchTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("oracle.New: %v", err)
	}
	if err := o.Start(); err != nil {
		t.Fatalf("oracle.Start: %v", err)
	}
	t.Cleanup(o.Stop)

	srv := tassandra.NewHTTPServer("127.0.0.1:0", o, store)

	return srv, store
}

// get issues a GET request against the HTTPServer's handler and returns the
// response recorder.
func get(t *testing.T, srv *tassandra.HTTPServer, path string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	return w
}

func TestHTTPCurrentPrice(t *testing.T) {
	const rawPrice = 9_500_012_345_678

	srv, _ := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: rawPrice},
	)

	w := get(t, srv, "/price/usd")

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	var resp tassandra.PriceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Currency != "USD" {
		t.Errorf("Currency: got %q, want USD", resp.Currency)
	}
	if resp.Price != "95000.12345678" {
		t.Errorf("Price: got %q, want 95000.12345678", resp.Price)
	}
	if resp.Exchange != "" {
		t.Errorf("Exchange: got %q, want empty", resp.Exchange)
	}
}

func TestHTTPCurrentPrice_CurrencyUppercase(t *testing.T) {
	srv, _ := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.EUR: 8_800_000_000_000},
	)

	// Both "eur" and "EUR" should work.
	for _, path := range []string{"/price/eur", "/price/EUR"} {
		w := get(t, srv, path)
		if w.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, w.Code)
		}
	}
}

func TestHTTPHistoricalAggregatedPrice(t *testing.T) {
	srv, store := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
	)

	// Insert a known historical record.
	minuteTS := time.Now().Add(-2 * time.Hour).Truncate(time.Minute).Unix()
	const historicalValue = 9_000_000_000_000
	if err := store.InsertAggregatedPrice(
		t.Context(), pricefeed.USD, historicalValue, minuteTS,
	); err != nil {
		t.Fatalf("InsertAggregatedPrice: %v", err)
	}

	w := get(t, srv, fmt.Sprintf("/price/USD?date=%d", minuteTS))

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", w.Code, w.Body)
	}

	var resp tassandra.PriceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Price != "90000.00000000" {
		t.Errorf("Price: got %q, want 90000.00000000", resp.Price)
	}
	if resp.Timestamp != minuteTS {
		t.Errorf("Timestamp: got %d, want %d", resp.Timestamp, minuteTS)
	}
}

func TestHTTPHistoricalExchangePrice(t *testing.T) {
	srv, store := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
	)

	minuteTS := time.Now().Add(-1 * time.Hour).Truncate(time.Minute)
	const exchangeValue = 9_450_000_000_000
	if err := store.InsertExchangePrice(t.Context(), pricefeed.Price{
		Value:     exchangeValue,
		Currency:  pricefeed.USD,
		Exchange:  "kraken",
		Timestamp: minuteTS,
	}); err != nil {
		t.Fatalf("InsertExchangePrice: %v", err)
	}

	path := fmt.Sprintf("/price/USD?exchange=kraken&date=%d",
		minuteTS.Unix())
	w := get(t, srv, path)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", w.Code, w.Body)
	}

	var resp tassandra.PriceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Exchange != "kraken" {
		t.Errorf("Exchange: got %q, want kraken", resp.Exchange)
	}
	if resp.Price != "94500.00000000" {
		t.Errorf("Price: got %q, want 94500.00000000", resp.Price)
	}
}

func TestHTTPNotFound(t *testing.T) {
	srv, _ := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
	)

	// Query a timestamp with no data.
	w := get(t, srv, "/price/USD?date=1")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHTTPLatestExchangePrice(t *testing.T) {
	srv, store := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
	)

	const exchangeValue = 9_450_000_000_000
	if err := store.InsertExchangePrice(t.Context(), pricefeed.Price{
		Value:     exchangeValue,
		Currency:  pricefeed.USD,
		Exchange:  "kraken",
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("InsertExchangePrice: %v", err)
	}

	w := get(t, srv, "/price/USD?exchange=kraken")
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200 (body: %s)", w.Code, w.Body)
	}

	var resp tassandra.PriceResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if resp.Exchange != "kraken" {
		t.Errorf("Exchange: got %q, want kraken", resp.Exchange)
	}
	if resp.Price != "94500.00000000" {
		t.Errorf("Price: got %q, want 94500.00000000", resp.Price)
	}
}

func TestHTTPExchangeWithoutDateNotFound(t *testing.T) {
	srv, _ := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
	)

	// No data seeded for this exchange — expect 404.
	w := get(t, srv, "/price/USD?exchange=kraken")
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestHTTPInvalidDate(t *testing.T) {
	srv, _ := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
	)

	w := get(t, srv, "/price/USD?date=notanumber")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestHTTPNoOraclePrice(t *testing.T) {
	// Use a currency the oracle has no feed for.
	srv, _ := setupHTTPServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
	)

	w := get(t, srv, "/price/GBP")
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", w.Code)
	}
}

func TestFormatPrice(t *testing.T) {
	cases := []struct {
		value uint64
		want  string
	}{
		{9_500_000_000_000, "95000.00000000"},
		{9_500_012_345_678, "95000.12345678"},
		{100_000_000, "1.00000000"},
		{1, "0.00000001"},
		{0, "0.00000000"},
	}

	for _, tc := range cases {
		got := tassandra.FormatPrice(tc.value)
		if got != tc.want {
			t.Errorf("FormatPrice(%d) = %q, want %q",
				tc.value, got, tc.want)
		}
	}
}
