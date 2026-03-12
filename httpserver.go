package tassandra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/liongrass/tassandra/oracle"
	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore"
)

// PriceResponse is the JSON payload returned by all HTTP price endpoints.
type PriceResponse struct {
	// Currency is the ISO 4217 fiat currency code (e.g. "USD").
	Currency string `json:"currency"`

	// Price is the BTC spot price as a decimal string with 8 decimal
	// places of precision (e.g. "95000.12345678").
	Price string `json:"price"`

	// Timestamp is the Unix timestamp of the minute this price
	// represents.
	Timestamp int64 `json:"timestamp"`

	// Exchange is the name of the exchange, present only for
	// exchange-specific historical queries.
	Exchange string `json:"exchange,omitempty"`
}

// HTTPServer serves raw (no markup) Bitcoin prices over HTTP. It is intended
// to sit behind an Aperture L402 proxy; it performs no authentication itself.
type HTTPServer struct {
	oracle *oracle.Oracle
	store  pricestore.PriceStore
	srv    *http.Server
}

// NewHTTPServer creates a new HTTPServer that listens on addr. Call Start to
// begin serving.
func NewHTTPServer(addr string, o *oracle.Oracle,
	store pricestore.PriceStore) *HTTPServer {

	s := &HTTPServer{
		oracle: o,
		store:  store,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /price/{currency}", s.handlePrice)

	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	return s
}

// ServeHTTP implements http.Handler, delegating to the internal mux.
// Primarily used in tests to exercise handlers without a real listener.
func (s *HTTPServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.srv.Handler.ServeHTTP(w, r)
}

// Start begins serving HTTP requests in a background goroutine.
func (s *HTTPServer) Start() error {
	log.Infof("HTTP server listening on %s", s.srv.Addr)

	go func() {
		if err := s.srv.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {

			log.Errorf("HTTP server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server, waiting until ctx is done or
// all active connections are closed.
func (s *HTTPServer) Stop(ctx context.Context) error {
	log.Infof("HTTP server stopping")
	return s.srv.Shutdown(ctx)
}

// handlePrice handles GET /price/{currency}.
//
// Query parameters (both optional):
//   - date     unix timestamp; if omitted the current oracle price is returned
//   - exchange exchange name; requires date; returns per-exchange price
func (s *HTTPServer) handlePrice(w http.ResponseWriter, r *http.Request) {
	currency := pricefeed.FiatCurrency(
		strings.ToUpper(r.PathValue("currency")),
	)

	dateStr := r.URL.Query().Get("date")
	exchangeName := r.URL.Query().Get("exchange")

	if dateStr != "" {
		s.handleHistoricalPrice(w, r, currency, exchangeName, dateStr)
		return
	}

	if exchangeName != "" {
		s.handleLatestExchangePrice(w, r, currency, exchangeName)
		return
	}

	s.handleCurrentPrice(w, currency)
}

// handleCurrentPrice serves the latest oracle price for currency.
func (s *HTTPServer) handleCurrentPrice(w http.ResponseWriter,
	currency pricefeed.FiatCurrency) {

	stored, ok := s.oracle.LatestPrice(currency)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("no price available yet for %s", currency))
		return
	}

	writeJSON(w, PriceResponse{
		Currency:  string(currency),
		Price:     FormatPrice(stored.Value),
		Timestamp: stored.MinuteTS,
	})
}

// handleLatestExchangePrice serves the most recently stored price for a
// specific exchange and currency.
func (s *HTTPServer) handleLatestExchangePrice(w http.ResponseWriter,
	r *http.Request, currency pricefeed.FiatCurrency, exchangeName string) {

	stored, err := s.store.LatestExchangePrice(
		r.Context(), currency, exchangeName,
	)
	if errors.Is(err, pricestore.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound,
			fmt.Sprintf("no %s/%s price available yet",
				exchangeName, currency))
		return
	}
	if err != nil {
		log.Errorf("LatestExchangePrice %s/%s: %v",
			exchangeName, currency, err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, PriceResponse{
		Currency:  string(currency),
		Price:     FormatPrice(stored.Value),
		Timestamp: stored.MinuteTS,
		Exchange:  stored.Exchange,
	})
}

// handleHistoricalPrice serves a stored price for the given minute timestamp.
// If exchangeName is non-empty the per-exchange record is returned; otherwise
// the aggregated record at-or-before dateTS is returned.
func (s *HTTPServer) handleHistoricalPrice(w http.ResponseWriter,
	r *http.Request, currency pricefeed.FiatCurrency, exchangeName,
	dateStr string) {

	dateTS, err := strconv.ParseInt(dateStr, 10, 64)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "date must be a unix timestamp")
		return
	}

	minuteTS := time.Unix(dateTS, 0).Truncate(time.Minute).Unix()
	ctx := r.Context()

	if exchangeName != "" {
		stored, err := s.store.ExchangePriceAt(
			ctx, currency, exchangeName, minuteTS,
		)
		if errors.Is(err, pricestore.ErrNotFound) {
			writeJSONError(w, http.StatusNotFound,
				fmt.Sprintf("no %s/%s price at %d",
					exchangeName, currency, minuteTS))
			return
		}
		if err != nil {
			log.Errorf("ExchangePriceAt %s/%s@%d: %v",
				exchangeName, currency, minuteTS, err)
			writeJSONError(w, http.StatusInternalServerError,
				"internal error")
			return
		}

		writeJSON(w, PriceResponse{
			Currency:  string(currency),
			Price:     FormatPrice(stored.Value),
			Timestamp: stored.MinuteTS,
			Exchange:  stored.Exchange,
		})
		return
	}

	stored, err := s.store.AggregatedPriceAt(ctx, currency, minuteTS)
	if errors.Is(err, pricestore.ErrNotFound) {
		writeJSONError(w, http.StatusNotFound,
			fmt.Sprintf("no aggregated %s price at or before %d",
				currency, minuteTS))
		return
	}
	if err != nil {
		log.Errorf("AggregatedPriceAt %s@%d: %v", currency, minuteTS, err)
		writeJSONError(w, http.StatusInternalServerError, "internal error")
		return
	}

	writeJSON(w, PriceResponse{
		Currency:  string(currency),
		Price:     FormatPrice(stored.Value),
		Timestamp: stored.MinuteTS,
	})
}

// FormatPrice converts a scaled uint64 price (PriceScale = 1e8) to a decimal
// string with 8 decimal places. For example, 9500012345678 → "95000.12345678".
func FormatPrice(value uint64) string {
	const scale = pricefeed.PriceScale
	return fmt.Sprintf("%d.%08d", value/scale, value%scale)
}

// writeJSON encodes v as JSON and writes it to w with a 200 status.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Errorf("writing JSON response: %v", err)
	}
}

// errResponse is the JSON payload for error responses.
type errResponse struct {
	Error string `json:"error"`
}

// writeJSONError writes a JSON error payload with the given HTTP status code.
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errResponse{Error: msg}) //nolint:errcheck
}
