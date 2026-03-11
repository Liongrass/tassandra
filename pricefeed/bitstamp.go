package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	bitstampName    = "bitstamp"
	bitstampBaseURL = "https://www.bitstamp.net/api/v2/ticker"
)

// bitstampTickerResponse is the JSON response from the Bitstamp ticker
// endpoint. Only the last trade price is used.
type bitstampTickerResponse struct {
	Last string `json:"last"`
}

// BitstampFeed fetches Bitcoin spot prices from the Bitstamp public API.
type BitstampFeed struct {
	client *http.Client
}

// NewBitstampFeed creates a new BitstampFeed using the given HTTP timeout.
func NewBitstampFeed(timeout time.Duration) *BitstampFeed {
	return &BitstampFeed{
		client: &http.Client{Timeout: timeout},
	}
}

// Name returns the exchange identifier.
func (b *BitstampFeed) Name() string { return bitstampName }

// pairFor returns the Bitstamp currency pair path segment for the given fiat
// currency. Bitstamp uses lowercase identifiers, e.g. USD → "btcusd",
// EUR → "btceur".
func (b *BitstampFeed) pairFor(currency FiatCurrency) string {
	return "btc" + strings.ToLower(string(currency))
}

// FetchPrice fetches the current BTC spot price from Bitstamp for the given
// fiat currency.
func (b *BitstampFeed) FetchPrice(ctx context.Context,
	currency FiatCurrency) (Price, error) {

	pair := b.pairFor(currency)

	url := fmt.Sprintf("%s/%s/", bitstampBaseURL, pair)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Price{}, fmt.Errorf("creating request: %w", err)
	}

	resp, err := b.client.Do(req)
	if err != nil {
		return Price{}, fmt.Errorf("fetching price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return Price{}, ErrCurrencyNotSupported
	}
	if resp.StatusCode != http.StatusOK {
		return Price{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var ticker bitstampTickerResponse
	if err := json.NewDecoder(resp.Body).Decode(&ticker); err != nil {
		return Price{}, fmt.Errorf("decoding response: %w", err)
	}

	priceStr := ticker.Last
	value, err := parsePriceString(priceStr)
	if err != nil {
		return Price{}, fmt.Errorf("parsing price %q: %w", priceStr, err)
	}

	log.Debugf("Bitstamp %s/%s: %s (raw)", currency, "BTC", priceStr)

	return Price{
		Value:     value,
		Currency:  currency,
		Exchange:  bitstampName,
		Timestamp: time.Now(),
	}, nil
}
