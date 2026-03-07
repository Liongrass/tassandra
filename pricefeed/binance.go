package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	binanceName    = "binance"
	binanceBaseURL = "https://api.binance.com/api/v3/ticker/price"
)

// binanceSymbols maps fiat currencies to Binance trading pair symbols.
var binanceSymbols = map[FiatCurrency]string{
	USD: "BTCUSDT",
	EUR: "BTCEUR",
	GBP: "BTCGBP",
}

// binanceTickerResponse is the JSON response from the Binance ticker endpoint.
type binanceTickerResponse struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
}

// BinanceFeed fetches Bitcoin spot prices from the Binance public API.
type BinanceFeed struct {
	client *http.Client
}

// NewBinanceFeed creates a new BinanceFeed using the given HTTP timeout.
func NewBinanceFeed(timeout time.Duration) *BinanceFeed {
	return &BinanceFeed{
		client: &http.Client{Timeout: timeout},
	}
}

// Name returns the exchange identifier.
func (b *BinanceFeed) Name() string { return binanceName }

// SupportedCurrencies returns the fiat currencies Binance can price.
func (b *BinanceFeed) SupportedCurrencies() []FiatCurrency {
	currencies := make([]FiatCurrency, 0, len(binanceSymbols))
	for c := range binanceSymbols {
		currencies = append(currencies, c)
	}
	return currencies
}

// FetchPrice fetches the current BTC spot price from Binance for the given
// fiat currency.
func (b *BinanceFeed) FetchPrice(ctx context.Context,
	currency FiatCurrency) (Price, error) {

	symbol, ok := binanceSymbols[currency]
	if !ok {
		return Price{}, ErrCurrencyNotSupported
	}

	url := fmt.Sprintf("%s?symbol=%s", binanceBaseURL, symbol)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Price{}, fmt.Errorf("creating request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return Price{}, fmt.Errorf("fetching price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Price{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var ticker binanceTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&ticker); err != nil {
		return Price{}, fmt.Errorf("decoding response: %w", err)
	}

	value, err := parsePriceString(ticker.Price)
	if err != nil {
		return Price{}, fmt.Errorf("parsing price %q: %w", ticker.Price, err)
	}

	now := time.Now()

	log.Debugf("Binance %s/%s: %s (raw)", currency, "BTC", ticker.Price)

	return Price{
		Value:     value,
		Currency:  currency,
		Exchange:  binanceName,
		Timestamp: now,
	}, nil
}
