package oracle_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/liongrass/tassandra/oracle"
	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore"
)

// mockFeed is a controllable PriceFeed for testing.
type mockFeed struct {
	name   string
	values map[pricefeed.FiatCurrency]uint64
	err    error
}

func (m *mockFeed) Name() string { return m.name }

func (m *mockFeed) FetchPrice(_ context.Context,
	currency pricefeed.FiatCurrency) (pricefeed.Price, error) {

	if m.err != nil {
		return pricefeed.Price{}, m.err
	}
	v, ok := m.values[currency]
	if !ok {
		return pricefeed.Price{}, pricefeed.ErrCurrencyNotSupported
	}
	return pricefeed.Price{
		Value:     v,
		Currency:  currency,
		Exchange:  m.name,
		Timestamp: time.Now(),
	}, nil
}

// newTestStore returns a temp-file SQLiteStore that is closed after the test.
func newTestStore(t *testing.T) pricestore.PriceStore {
	t.Helper()

	store, err := pricestore.NewSQLiteStore(
		filepath.Join(t.TempDir(), "test.db"),
	)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}

	t.Cleanup(func() { store.Close() })

	return store
}

// newTestOracle creates an oracle with a fast poll interval for testing.
// Each feed is configured to poll USD only.
func newTestOracle(t *testing.T, feeds []pricefeed.PriceFeed,
	store pricestore.PriceStore) *oracle.Oracle {

	t.Helper()

	feedCfgs := make([]oracle.FeedConfig, len(feeds))
	for i, f := range feeds {
		feedCfgs[i] = oracle.FeedConfig{
			Feed:       f,
			Currencies: []pricefeed.FiatCurrency{pricefeed.USD},
		}
	}

	o, err := oracle.New(oracle.Config{
		Feeds:        feedCfgs,
		Store:        store,
		PollInterval: 10 * time.Millisecond,
		FetchTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("oracle.New: %v", err)
	}

	return o
}

func TestMedianOdd(t *testing.T) {
	store := newTestStore(t)

	feeds := []pricefeed.PriceFeed{
		&mockFeed{name: "a", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_000_000_000_000,
		}},
		&mockFeed{name: "b", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_500_000_000_000,
		}},
		&mockFeed{name: "c", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_800_000_000_000,
		}},
	}

	o := newTestOracle(t, feeds, store)
	if err := o.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer o.Stop()

	got, ok := o.LatestPrice(pricefeed.USD)
	if !ok {
		t.Fatal("LatestPrice: no price available after Start")
	}

	// Median of [90000, 95000, 98000] is 95000.
	const want = 9_500_000_000_000
	if got.Value != want {
		t.Errorf("Value: got %d, want %d", got.Value, want)
	}
}

func TestMedianEven(t *testing.T) {
	store := newTestStore(t)

	// Even number of feeds: lower middle value should be returned.
	feeds := []pricefeed.PriceFeed{
		&mockFeed{name: "a", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_000_000_000_000,
		}},
		&mockFeed{name: "b", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_500_000_000_000,
		}},
		&mockFeed{name: "c", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_800_000_000_000,
		}},
		&mockFeed{name: "d", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 10_000_000_000_000,
		}},
	}

	o := newTestOracle(t, feeds, store)
	if err := o.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer o.Stop()

	got, ok := o.LatestPrice(pricefeed.USD)
	if !ok {
		t.Fatal("LatestPrice: no price available after Start")
	}

	// Sorted: [90000, 95000, 98000, 100000], lower middle = 95000.
	const want = 9_500_000_000_000
	if got.Value != want {
		t.Errorf("Value: got %d, want %d", got.Value, want)
	}
}

func TestOneFeedFails(t *testing.T) {
	store := newTestStore(t)

	feeds := []pricefeed.PriceFeed{
		&mockFeed{name: "good", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_500_000_000_000,
		}},
		&mockFeed{name: "bad", err: pricefeed.ErrCurrencyNotSupported},
	}

	o := newTestOracle(t, feeds, store)
	if err := o.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer o.Stop()

	got, ok := o.LatestPrice(pricefeed.USD)
	if !ok {
		t.Fatal("LatestPrice: no price despite one working feed")
	}
	if got.Value != 9_500_000_000_000 {
		t.Errorf("Value: got %d, want 9_500_000_000_000", got.Value)
	}
}

func TestAllFeedsFail(t *testing.T) {
	store := newTestStore(t)

	feeds := []pricefeed.PriceFeed{
		&mockFeed{name: "bad1", err: pricefeed.ErrCurrencyNotSupported},
		&mockFeed{name: "bad2", err: pricefeed.ErrCurrencyNotSupported},
	}

	o := newTestOracle(t, feeds, store)
	if err := o.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer o.Stop()

	_, ok := o.LatestPrice(pricefeed.USD)
	if ok {
		t.Error("expected no price when all feeds fail")
	}
}

func TestSubscription(t *testing.T) {
	store := newTestStore(t)

	feeds := []pricefeed.PriceFeed{
		&mockFeed{name: "a", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_500_000_000_000,
		}},
	}

	o, err := oracle.New(oracle.Config{
		Feeds: []oracle.FeedConfig{
			{
				Feed:       feeds[0],
				Currencies: []pricefeed.FiatCurrency{pricefeed.USD},
			},
		},
		Store:        store,
		PollInterval: 20 * time.Millisecond,
		FetchTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("oracle.New: %v", err)
	}

	ch, cancel := o.SubscribePrice()
	defer cancel()

	if err := o.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer o.Stop()

	// Expect at least one update within a reasonable timeout.
	select {
	case update := <-ch:
		if update.Currency != pricefeed.USD {
			t.Errorf("Currency: got %s, want USD", update.Currency)
		}
		if update.Price.Value != 9_500_000_000_000 {
			t.Errorf("Value: got %d, want 9_500_000_000_000",
				update.Price.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for subscription update")
	}
}

func TestPricesPersistedToStore(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	feeds := []pricefeed.PriceFeed{
		&mockFeed{name: "exchange1", values: map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: 9_500_000_000_000,
		}},
	}

	o := newTestOracle(t, feeds, store)
	if err := o.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer o.Stop()

	// Wait for at least one poll cycle.
	time.Sleep(50 * time.Millisecond)

	stored, err := store.LatestAggregatedPrice(ctx, pricefeed.USD)
	if err != nil {
		t.Fatalf("LatestAggregatedPrice: %v", err)
	}
	if stored.Value != 9_500_000_000_000 {
		t.Errorf("stored value: got %d, want 9_500_000_000_000",
			stored.Value)
	}
}
