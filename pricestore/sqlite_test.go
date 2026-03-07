package pricestore_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore"
)

// newTestStore opens a temporary SQLite store for testing.
func newTestStore(t *testing.T) pricestore.PriceStore {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.db")
	store, err := pricestore.NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	t.Cleanup(func() { store.Close() })

	return store
}

func TestInsertAndQueryAggregatedPrice(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	now := time.Now().Truncate(time.Minute)
	minuteTS := now.Unix()

	const wantValue = 9_500_000_000_000 // $95,000.00000000

	if err := store.InsertAggregatedPrice(
		ctx, pricefeed.USD, wantValue, minuteTS,
	); err != nil {
		t.Fatalf("InsertAggregatedPrice: %v", err)
	}

	// LatestAggregatedPrice should return the value we just inserted.
	got, err := store.LatestAggregatedPrice(ctx, pricefeed.USD)
	if err != nil {
		t.Fatalf("LatestAggregatedPrice: %v", err)
	}

	if got.Value != wantValue {
		t.Errorf("Value: got %d, want %d", got.Value, wantValue)
	}
	if got.MinuteTS != minuteTS {
		t.Errorf("MinuteTS: got %d, want %d", got.MinuteTS, minuteTS)
	}
}

func TestAggregatedPriceAt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	base := time.Now().Truncate(time.Minute).Unix()
	values := []uint64{9_000_000_000_000, 9_500_000_000_000, 9_800_000_000_000}
	offsets := []int64{0, 60, 120}

	for i, v := range values {
		if err := store.InsertAggregatedPrice(
			ctx, pricefeed.USD, v, base+offsets[i],
		); err != nil {
			t.Fatalf("InsertAggregatedPrice[%d]: %v", i, err)
		}
	}

	// Querying at exactly the second minute should return the second value.
	got, err := store.AggregatedPriceAt(ctx, pricefeed.USD, base+60)
	if err != nil {
		t.Fatalf("AggregatedPriceAt: %v", err)
	}
	if got.Value != values[1] {
		t.Errorf("Value: got %d, want %d", got.Value, values[1])
	}

	// Querying between the second and third minute returns the second value.
	got, err = store.AggregatedPriceAt(ctx, pricefeed.USD, base+90)
	if err != nil {
		t.Fatalf("AggregatedPriceAt (between): %v", err)
	}
	if got.Value != values[1] {
		t.Errorf("Value: got %d, want %d", got.Value, values[1])
	}
}

func TestInsertAndQueryExchangePrice(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	now := time.Now().Truncate(time.Minute)
	price := pricefeed.Price{
		Value:     9_500_012_345_678,
		Currency:  pricefeed.USD,
		Exchange:  "kraken",
		Timestamp: now,
	}

	if err := store.InsertExchangePrice(ctx, price); err != nil {
		t.Fatalf("InsertExchangePrice: %v", err)
	}

	got, err := store.ExchangePriceAt(
		ctx, pricefeed.USD, "kraken", now.Unix(),
	)
	if err != nil {
		t.Fatalf("ExchangePriceAt: %v", err)
	}

	if got.Value != price.Value {
		t.Errorf("Value: got %d, want %d", got.Value, price.Value)
	}
	if got.Exchange != "kraken" {
		t.Errorf("Exchange: got %q, want %q", got.Exchange, "kraken")
	}
}

func TestExchangePriceAtNotFound(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	_, err := store.ExchangePriceAt(ctx, pricefeed.USD, "binance", 0)
	if err != pricestore.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestListAggregatedPrices(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	base := time.Now().Truncate(time.Minute).Unix()

	for i := range 5 {
		if err := store.InsertAggregatedPrice(
			ctx, pricefeed.USD, uint64(9_000_000_000_000+i),
			base+int64(i*60),
		); err != nil {
			t.Fatalf("InsertAggregatedPrice[%d]: %v", i, err)
		}
	}

	// Query the middle three records.
	prices, err := store.ListAggregatedPrices(
		ctx, pricefeed.USD, base+60, base+180,
	)
	if err != nil {
		t.Fatalf("ListAggregatedPrices: %v", err)
	}
	if len(prices) != 3 {
		t.Fatalf("expected 3 records, got %d", len(prices))
	}

	// Verify ascending order.
	for i := 1; i < len(prices); i++ {
		if prices[i].MinuteTS <= prices[i-1].MinuteTS {
			t.Errorf("prices not in ascending order at index %d", i)
		}
	}
}

// TestInterfaceCompliance verifies SQLiteStore satisfies PriceStore at
// compile time.
func TestInterfaceCompliance(t *testing.T) {
	var _ pricestore.PriceStore = (*pricestore.SQLiteStore)(nil)
}
