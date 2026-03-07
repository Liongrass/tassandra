package pricestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore/sqlc"

	_ "modernc.org/sqlite" // register the sqlite3 driver
)

// SQLiteStore is a PriceStore backed by a SQLite database via modernc.org/sqlite.
type SQLiteStore struct {
	db      *sql.DB
	queries *sqlc.Queries
}

// NewSQLiteStore opens (or creates) the SQLite database at the given path,
// runs the schema migrations, and returns a ready-to-use SQLiteStore.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db at %q: %w", dbPath, err)
	}

	// SQLite performs best with a single writer connection.
	db.SetMaxOpenConns(1)

	if err := applySchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("applying schema: %w", err)
	}

	log.Infof("SQLite price store opened at %s", dbPath)

	return &SQLiteStore{
		db:      db,
		queries: sqlc.New(db),
	}, nil
}

// applySchema creates the tables and indexes if they do not already exist.
func applySchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS exchange_prices (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    exchange    TEXT    NOT NULL,
    currency    TEXT    NOT NULL,
    value       INTEGER NOT NULL,
    minute_ts   INTEGER NOT NULL,
    UNIQUE(exchange, currency, minute_ts)
);

CREATE TABLE IF NOT EXISTS aggregated_prices (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    currency    TEXT    NOT NULL,
    value       INTEGER NOT NULL,
    minute_ts   INTEGER NOT NULL,
    UNIQUE(currency, minute_ts)
);

CREATE INDEX IF NOT EXISTS idx_exchange_prices_lookup
    ON exchange_prices(currency, exchange, minute_ts);

CREATE INDEX IF NOT EXISTS idx_aggregated_prices_lookup
    ON aggregated_prices(currency, minute_ts);
`
	_, err := db.Exec(schema)
	return err
}

// InsertExchangePrice stores a raw price sample from a single exchange.
func (s *SQLiteStore) InsertExchangePrice(ctx context.Context,
	price pricefeed.Price) error {

	minuteTS := toMinuteTS(price.Timestamp)

	err := s.queries.InsertExchangePrice(ctx, sqlc.InsertExchangePriceParams{
		Exchange: price.Exchange,
		Currency: string(price.Currency),
		Value:    int64(price.Value),
		MinuteTs: minuteTS,
	})
	if err != nil {
		return fmt.Errorf("inserting exchange price (%s/%s@%d): %w",
			price.Exchange, price.Currency, minuteTS, err)
	}

	log.Tracef("Stored %s %s price: %d (minute %d)",
		price.Exchange, price.Currency, price.Value, minuteTS)

	return nil
}

// InsertAggregatedPrice stores the aggregated price for a currency at the
// given minute timestamp.
func (s *SQLiteStore) InsertAggregatedPrice(ctx context.Context,
	currency pricefeed.FiatCurrency, value uint64, minuteTS int64) error {

	err := s.queries.InsertAggregatedPrice(
		ctx, sqlc.InsertAggregatedPriceParams{
			Currency: string(currency),
			Value:    int64(value),
			MinuteTs: minuteTS,
		},
	)
	if err != nil {
		return fmt.Errorf("inserting aggregated price (%s@%d): %w",
			currency, minuteTS, err)
	}

	log.Debugf("Stored aggregated %s price: %d (minute %d)",
		currency, value, minuteTS)

	return nil
}

// LatestAggregatedPrice returns the most recently stored aggregated price for
// the given currency.
func (s *SQLiteStore) LatestAggregatedPrice(ctx context.Context,
	currency pricefeed.FiatCurrency) (StoredPrice, error) {

	row, err := s.queries.LatestAggregatedPrice(ctx, string(currency))
	if errors.Is(err, sql.ErrNoRows) {
		return StoredPrice{}, ErrNotFound
	}
	if err != nil {
		return StoredPrice{}, fmt.Errorf(
			"querying latest aggregated price for %s: %w", currency, err,
		)
	}

	return aggregatedRowToStoredPrice(row, currency), nil
}

// AggregatedPriceAt returns the aggregated price for a currency at or before
// the given minute-aligned Unix timestamp.
func (s *SQLiteStore) AggregatedPriceAt(ctx context.Context,
	currency pricefeed.FiatCurrency, minuteTS int64) (StoredPrice, error) {

	row, err := s.queries.AggregatedPriceAt(
		ctx, sqlc.AggregatedPriceAtParams{
			Currency: string(currency),
			MinuteTs: minuteTS,
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredPrice{}, ErrNotFound
	}
	if err != nil {
		return StoredPrice{}, fmt.Errorf(
			"querying aggregated price for %s at %d: %w",
			currency, minuteTS, err,
		)
	}

	return aggregatedRowToStoredPrice(row, currency), nil
}

// ExchangePriceAt returns the price from a specific exchange for a currency
// at the given exact minute-aligned Unix timestamp.
func (s *SQLiteStore) ExchangePriceAt(ctx context.Context,
	currency pricefeed.FiatCurrency, exchange string,
	minuteTS int64) (StoredPrice, error) {

	row, err := s.queries.ExchangePriceAt(
		ctx, sqlc.ExchangePriceAtParams{
			Currency: string(currency),
			Exchange: exchange,
			MinuteTs: minuteTS,
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredPrice{}, ErrNotFound
	}
	if err != nil {
		return StoredPrice{}, fmt.Errorf(
			"querying exchange price for %s/%s at %d: %w",
			exchange, currency, minuteTS, err,
		)
	}

	return StoredPrice{
		Value:     uint64(row.Value),
		Currency:  pricefeed.FiatCurrency(row.Currency),
		Exchange:  row.Exchange,
		MinuteTS:  row.MinuteTs,
		Timestamp: time.Unix(row.MinuteTs, 0).UTC(),
	}, nil
}

// ListAggregatedPrices returns all aggregated price records for a currency
// within the given inclusive minute timestamp range.
func (s *SQLiteStore) ListAggregatedPrices(ctx context.Context,
	currency pricefeed.FiatCurrency,
	startTS, endTS int64) ([]StoredPrice, error) {

	rows, err := s.queries.ListAggregatedPrices(
		ctx, sqlc.ListAggregatedPricesParams{
			Currency:   string(currency),
			MinuteTs:   startTS,
			MinuteTs_2: endTS,
		},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"listing aggregated prices for %s [%d, %d]: %w",
			currency, startTS, endTS, err,
		)
	}

	prices := make([]StoredPrice, len(rows))
	for i, row := range rows {
		prices[i] = aggregatedRowToStoredPrice(row, currency)
	}

	return prices, nil
}

// Close releases the database connection.
func (s *SQLiteStore) Close() error {
	log.Infof("Closing SQLite price store")
	return s.db.Close()
}

// aggregatedRowToStoredPrice converts a sqlc aggregated row to a StoredPrice.
func aggregatedRowToStoredPrice(row sqlc.AggregatedPrice,
	currency pricefeed.FiatCurrency) StoredPrice {

	return StoredPrice{
		Value:     uint64(row.Value),
		Currency:  currency,
		MinuteTS:  row.MinuteTs,
		Timestamp: time.Unix(row.MinuteTs, 0).UTC(),
	}
}
