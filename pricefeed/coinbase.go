package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	coinbaseName    = "coinbase"
	coinbaseBaseURL = "https://api.coinbase.com/v2/prices"
)

// coinbasePairs maps fiat currencies to Coinbase product pair strings.
var coinbasePairs = map[FiatCurrency]string{
	USD: "BTC-USD",
	EUR: "BTC-EUR",
	GBP: "BTC-GBP",
}

// coinbaseSpotResponse is the JSON response from the Coinbase spot price
// endpoint.
type coinbaseSpotResponse struct {
	Data struct {
		Amount   string `json:"amount"`
		Base     string `json:"base"`
		Currency string `json:"currency"`
	} `json:"data"`
}

// CoinbaseFeed fetches Bitcoin spot prices from the Coinbase public API.
type CoinbaseFeed struct {
	client *http.Client
}

// NewCoinbaseFeed creates a new CoinbaseFeed using the given HTTP timeout.
func NewCoinbaseFeed(timeout time.Duration) *CoinbaseFeed {
	return &CoinbaseFeed{
		client: &http.Client{Timeout: timeout},
	}
}

// Name returns the exchange identifier.
func (c *CoinbaseFeed) Name() string { return coinbaseName }

// SupportedCurrencies returns the fiat currencies Coinbase can price.
func (c *CoinbaseFeed) SupportedCurrencies() []FiatCurrency {
	currencies := make([]FiatCurrency, 0, len(coinbasePairs))
	for cur := range coinbasePairs {
		currencies = append(currencies, cur)
	}
	return currencies
}

// FetchPrice fetches the current BTC spot price from Coinbase for the given
// fiat currency.
func (c *CoinbaseFeed) FetchPrice(ctx context.Context,
	currency FiatCurrency) (Price, error) {

	pair, ok := coinbasePairs[currency]
	if !ok {
		return Price{}, ErrCurrencyNotSupported
	}

	url := fmt.Sprintf("%s/%s/spot", coinbaseBaseURL, pair)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Price{}, fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return Price{}, fmt.Errorf("fetching price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Price{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var spotResp coinbaseSpotResponse
	if err := json.NewDecoder(resp.Body).Decode(&spotResp); err != nil {
		return Price{}, fmt.Errorf("decoding response: %w", err)
	}

	priceStr := spotResp.Data.Amount
	value, err := parsePriceString(priceStr)
	if err != nil {
		return Price{}, fmt.Errorf("parsing price %q: %w", priceStr, err)
	}

	now := time.Now()

	log.Debugf("Coinbase %s/%s: %s (raw)", currency, "BTC", priceStr)

	return Price{
		Value:     value,
		Currency:  currency,
		Exchange:  coinbaseName,
		Timestamp: now,
	}, nil
}
