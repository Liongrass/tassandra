package pricefeed

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// FiatCurrency is a ISO 4217 fiat currency code.
type FiatCurrency string

const (
	USD FiatCurrency = "USD"
	EUR FiatCurrency = "EUR"
	GBP FiatCurrency = "GBP"
)

// PriceScale is the factor applied to all Price.Value fields.
// Values are stored as the actual price multiplied by PriceScale, giving
// 8 decimal places of precision. For example, $95,000.12345678/BTC is
// stored as 9_500_012_345_678.
const PriceScale uint64 = 100_000_000

// Price represents a Bitcoin spot price in a fiat currency at a point in time.
type Price struct {
	// Value is the BTC/fiat price multiplied by PriceScale.
	Value uint64

	// Currency is the fiat denomination.
	Currency FiatCurrency

	// Exchange is the name of the exchange that reported this price.
	Exchange string

	// Timestamp is when this price was observed.
	Timestamp time.Time
}

// ErrCurrencyNotSupported is returned when a feed is asked for a currency it
// does not support.
var ErrCurrencyNotSupported = errors.New(
	"currency not supported by this exchange",
)

// PriceFeed is the interface that every exchange adapter must implement.
type PriceFeed interface {
	// Name returns the human-readable identifier of the exchange.
	Name() string

	// FetchPrice fetches the current BTC spot price for the given fiat
	// currency. Returns ErrCurrencyNotSupported if the exchange does not
	// offer a BTC/fiat pair for that currency.
	FetchPrice(ctx context.Context, currency FiatCurrency) (Price, error)

	// SupportedCurrencies returns the set of fiat currencies this feed
	// can price.
	SupportedCurrencies() []FiatCurrency
}

// parsePriceString parses a decimal price string (e.g. "97432.15000000")
// into a uint64 scaled by PriceScale (8 decimal places). Precision beyond
// 8 decimal places is truncated.
func parsePriceString(s string) (uint64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty price string")
	}

	parts := strings.SplitN(s, ".", 2)

	intPart, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parsing integer part %q: %w", parts[0], err)
	}

	result := intPart * PriceScale

	if len(parts) == 2 {
		dec := parts[1]

		// Truncate or zero-pad to exactly 8 decimal places.
		const decimals = 8
		if len(dec) > decimals {
			dec = dec[:decimals]
		} else {
			dec += strings.Repeat("0", decimals-len(dec))
		}

		fracPart, err := strconv.ParseUint(dec, 10, 64)
		if err != nil {
			return 0, fmt.Errorf(
				"parsing fractional part %q: %w", dec, err,
			)
		}

		result += fracPart
	}

	return result, nil
}
