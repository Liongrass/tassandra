package pricefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

const (
	krakenName    = "kraken"
	krakenBaseURL = "https://api.kraken.com/0/public/Ticker"
)

// krakenResponse is the top-level JSON response from the Kraken Ticker
// endpoint. The result map key is the exchange-internal pair name (e.g.
// "XXBTZUSD"), which may differ from the query pair name.
type krakenResponse struct {
	Error  []string                     `json:"error"`
	Result map[string]krakenTickerEntry `json:"result"`
}

// krakenTickerEntry holds the relevant fields from a Kraken ticker entry.
// Field "c" is [lastTradePrice, volume], per the Kraken API documentation.
type krakenTickerEntry struct {
	// C is [last trade closed price, volume].
	C []string `json:"c"`
}

// KrakenFeed fetches Bitcoin spot prices from the Kraken public API.
type KrakenFeed struct {
	client *http.Client
}

// NewKrakenFeed creates a new KrakenFeed using the given HTTP timeout.
func NewKrakenFeed(timeout time.Duration) *KrakenFeed {
	return &KrakenFeed{
		client: &http.Client{Timeout: timeout},
	}
}

// Name returns the exchange identifier.
func (k *KrakenFeed) Name() string { return krakenName }

// pairFor returns the Kraken pair name for the given fiat currency.
// Kraken uses XBT (not BTC) as the Bitcoin identifier, so the pair is
// formed as "XBT" + currency (e.g. USD → XBTUSD, EUR → XBTEUR).
func (k *KrakenFeed) pairFor(currency FiatCurrency) string {
	return "XBT" + string(currency)
}

// FetchPrice fetches the current BTC spot price from Kraken for the given
// fiat currency.
func (k *KrakenFeed) FetchPrice(ctx context.Context,
	currency FiatCurrency) (Price, error) {

	pair := k.pairFor(currency)

	url := fmt.Sprintf("%s?pair=%s", krakenBaseURL, pair)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Price{}, fmt.Errorf("creating request: %w", err)
	}

	resp, err := k.client.Do(req)
	if err != nil {
		return Price{}, fmt.Errorf("fetching price: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Price{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var krakenResp krakenResponse
	if err := json.NewDecoder(resp.Body).Decode(&krakenResp); err != nil {
		return Price{}, fmt.Errorf("decoding response: %w", err)
	}

	if len(krakenResp.Error) > 0 {
		return Price{}, fmt.Errorf("kraken API error: %v", krakenResp.Error)
	}

	// Kraken may return an internal pair name different from the query
	// pair, so we take the first (and only) entry in the result map.
	if len(krakenResp.Result) == 0 {
		return Price{}, ErrCurrencyNotSupported
	}

	var entry krakenTickerEntry
	for _, e := range krakenResp.Result {
		entry = e
		break
	}

	if len(entry.C) == 0 {
		return Price{}, fmt.Errorf(
			"missing last-trade field in Kraken response for pair %s", pair,
		)
	}

	priceStr := entry.C[0]
	value, err := parsePriceString(priceStr)
	if err != nil {
		return Price{}, fmt.Errorf("parsing price %q: %w", priceStr, err)
	}

	log.Debugf("Kraken %s/%s: %s (raw)", currency, "BTC", priceStr)

	return Price{
		Value:     value,
		Currency:  currency,
		Exchange:  krakenName,
		Timestamp: time.Now(),
	}, nil
}
