package pricestore

import (
	"context"
	"errors"
	"time"

	"github.com/liongrass/tassandra/pricefeed"
)

// ErrNotFound is returned when a price record does not exist in the store.
var ErrNotFound = errors.New("price record not found")

// StoredPrice is a price record retrieved from the store.
type StoredPrice struct {
	// Value is the BTC price scaled by pricefeed.PriceScale.
	Value uint64

	// Currency is the fiat denomination.
	Currency pricefeed.FiatCurrency

	// Exchange is the name of the exchange, or empty for aggregated prices.
	Exchange string

	// MinuteTS is the Unix timestamp of the minute this price represents.
	MinuteTS int64

	// Timestamp is a time.Time representation of MinuteTS.
	Timestamp time.Time
}

// PriceStore persists per-minute Bitcoin price samples and exposes queries
// over them.
type PriceStore interface {
	// InsertExchangePrice stores a raw price sample from a single
	// exchange. If a record already exists for the same exchange,
	// currency, and minute, its value is overwritten.
	InsertExchangePrice(ctx context.Context, price pricefeed.Price) error

	// InsertAggregatedPrice stores the aggregated (median) price for a
	// currency at the given minute timestamp. If a record already exists
	// for the same currency and minute, its value is overwritten.
	InsertAggregatedPrice(ctx context.Context,
		currency pricefeed.FiatCurrency, value uint64,
		minuteTS int64) error

	// LatestAggregatedPrice returns the most recently stored aggregated
	// price for the given currency. Returns ErrNotFound if none exists.
	LatestAggregatedPrice(ctx context.Context,
		currency pricefeed.FiatCurrency) (StoredPrice, error)

	// AggregatedPriceAt returns the aggregated price for a currency at
	// or before the given minute-aligned Unix timestamp. Returns
	// ErrNotFound if no record exists at or before that time.
	AggregatedPriceAt(ctx context.Context,
		currency pricefeed.FiatCurrency,
		minuteTS int64) (StoredPrice, error)

	// ExchangePriceAt returns the price from a specific exchange for a
	// currency at the given exact minute-aligned Unix timestamp. Returns
	// ErrNotFound if no record exists at that exact minute.
	ExchangePriceAt(ctx context.Context,
		currency pricefeed.FiatCurrency, exchange string,
		minuteTS int64) (StoredPrice, error)

	// ListAggregatedPrices returns all aggregated price records for a
	// currency within the given inclusive minute timestamp range, ordered
	// by timestamp ascending.
	ListAggregatedPrices(ctx context.Context,
		currency pricefeed.FiatCurrency,
		startTS, endTS int64) ([]StoredPrice, error)

	// Close releases all resources held by the store.
	Close() error
}

// toMinuteTS truncates a time.Time to the start of its minute as a Unix
// timestamp.
func toMinuteTS(t time.Time) int64 {
	return t.Truncate(time.Minute).Unix()
}
