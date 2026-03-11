package oracle

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore"
)

const (
	// DefaultPollInterval is the default interval between exchange polls.
	DefaultPollInterval = time.Minute

	// DefaultFetchTimeout is the default per-feed HTTP request timeout.
	DefaultFetchTimeout = 10 * time.Second

	// subBufferSize is the buffer capacity of each subscriber channel.
	// Slow consumers that fill their buffer will miss updates.
	subBufferSize = 10
)

// FeedConfig pairs a price feed with the specific fiat currencies it should
// be polled for. Only the listed currencies will be requested from this feed.
type FeedConfig struct {
	Feed       pricefeed.PriceFeed
	Currencies []pricefeed.FiatCurrency
}

// Config holds the configuration for the Oracle.
type Config struct {
	// Feeds is the list of price feeds and the currencies each should poll.
	Feeds []FeedConfig

	// Store is the price store used to persist price samples.
	Store pricestore.PriceStore

	// PollInterval is how often to poll exchanges. Defaults to
	// DefaultPollInterval if zero.
	PollInterval time.Duration

	// FetchTimeout is the per-feed HTTP request timeout. Defaults to
	// DefaultFetchTimeout if zero.
	FetchTimeout time.Duration
}

// PriceUpdate is emitted to subscribers each time a new aggregated price is
// computed for a currency.
type PriceUpdate struct {
	Currency pricefeed.FiatCurrency
	Price    pricestore.StoredPrice
}

// Oracle polls exchange price feeds, computes per-currency median prices,
// persists them to the store, and broadcasts updates to subscribers.
type Oracle struct {
	cfg Config

	// latest holds the most recently computed aggregated price per
	// currency, guarded by mu.
	mu     sync.RWMutex
	latest map[pricefeed.FiatCurrency]pricestore.StoredPrice

	// subs holds the active subscriber channels, guarded by subsMu.
	subsMu    sync.Mutex
	subs      map[uint64]chan PriceUpdate
	nextSubID uint64

	quit chan struct{}
	wg   sync.WaitGroup
}

// New creates a new Oracle. Call Start to begin polling.
func New(cfg Config) (*Oracle, error) {
	if len(cfg.Feeds) == 0 {
		return nil, errors.New("oracle requires at least one price feed")
	}
	for _, fc := range cfg.Feeds {
		if len(fc.Currencies) == 0 {
			return nil, errors.New("each feed config requires at " +
				"least one currency")
		}
	}
	if cfg.Store == nil {
		return nil, errors.New("oracle requires a price store")
	}

	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.FetchTimeout == 0 {
		cfg.FetchTimeout = DefaultFetchTimeout
	}

	return &Oracle{
		cfg:    cfg,
		latest: make(map[pricefeed.FiatCurrency]pricestore.StoredPrice),
		subs:   make(map[uint64]chan PriceUpdate),
		quit:   make(chan struct{}),
	}, nil
}

// Start performs an initial poll and then begins the background polling loop.
// It is safe to call Start only once.
func (o *Oracle) Start() error {
	log.Infof("Oracle starting: %d feed(s), interval=%s",
		len(o.cfg.Feeds), o.cfg.PollInterval)

	// Poll immediately so callers have prices before the first tick.
	o.pollAll()

	o.wg.Add(1)
	go o.pollLoop()

	return nil
}

// Stop signals the polling loop to exit and waits for it to finish.
func (o *Oracle) Stop() {
	log.Infof("Oracle stopping")
	close(o.quit)
	o.wg.Wait()
	log.Infof("Oracle stopped")
}

// LatestPrice returns the most recently computed aggregated price for the
// given currency. The second return value is false if no price has been
// computed yet.
func (o *Oracle) LatestPrice(
	currency pricefeed.FiatCurrency) (pricestore.StoredPrice, bool) {

	o.mu.RLock()
	defer o.mu.RUnlock()

	p, ok := o.latest[currency]
	return p, ok
}

// SubscribePrice returns a channel that receives PriceUpdate notifications
// whenever a new aggregated price is computed, and a cancel function that
// unsubscribes and closes the channel. The channel is buffered; a slow
// consumer may miss updates if the buffer fills.
func (o *Oracle) SubscribePrice() (<-chan PriceUpdate, func()) {
	o.subsMu.Lock()
	defer o.subsMu.Unlock()

	id := o.nextSubID
	o.nextSubID++

	ch := make(chan PriceUpdate, subBufferSize)
	o.subs[id] = ch

	cancel := func() {
		o.subsMu.Lock()
		defer o.subsMu.Unlock()

		delete(o.subs, id)
		close(ch)
	}

	return ch, cancel
}

// pollLoop runs the ticker-driven polling loop until quit is closed.
func (o *Oracle) pollLoop() {
	defer o.wg.Done()

	ticker := time.NewTicker(o.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			o.pollAll()

		case <-o.quit:
			return
		}
	}
}

// pollAll polls every currency that has at least one feed configured for it.
func (o *Oracle) pollAll() {
	// Build a deduplicated map from currency → feeds that serve it.
	currencyFeeds := make(
		map[pricefeed.FiatCurrency][]pricefeed.PriceFeed,
	)
	for _, fc := range o.cfg.Feeds {
		for _, c := range fc.Currencies {
			currencyFeeds[c] = append(currencyFeeds[c], fc.Feed)
		}
	}

	for currency, feeds := range currencyFeeds {
		o.pollCurrency(currency, feeds)
	}
}

// pollCurrency fetches prices from the given feeds for the given currency
// concurrently, computes the median, and persists both raw and aggregated
// samples.
func (o *Oracle) pollCurrency(currency pricefeed.FiatCurrency,
	feeds []pricefeed.PriceFeed) {

	ctx, cancel := context.WithTimeout(
		context.Background(), o.cfg.FetchTimeout,
	)
	defer cancel()

	type result struct {
		price pricefeed.Price
		err   error
	}

	resultCh := make(chan result, len(feeds))

	for _, feed := range feeds {
		feed := feed
		go func() {
			p, err := feed.FetchPrice(ctx, currency)
			resultCh <- result{price: p, err: err}
		}()
	}

	prices := make([]pricefeed.Price, 0, len(feeds))
	for range feeds {
		r := <-resultCh
		if r.err != nil {
			// ErrCurrencyNotSupported is expected for feeds that
			// don't carry this pair; log at debug only.
			if errors.Is(r.err, pricefeed.ErrCurrencyNotSupported) {
				log.Debugf("Feed does not support %s: %v",
					currency, r.err)
			} else {
				log.Warnf("Failed to fetch %s price: %v",
					currency, r.err)
			}
			continue
		}
		prices = append(prices, r.price)
	}

	if len(prices) == 0 {
		log.Errorf("No prices obtained for %s, skipping this cycle",
			currency)
		return
	}

	// Persist individual exchange prices.
	storeCtx := context.Background()
	for _, p := range prices {
		if err := o.cfg.Store.InsertExchangePrice(storeCtx, p); err != nil {
			log.Errorf("Storing exchange price %s/%s: %v",
				p.Exchange, currency, err)
		}
	}

	// Compute and persist the aggregated (median) price.
	medianValue := median(prices)
	minuteTS := time.Now().Truncate(time.Minute).Unix()

	if err := o.cfg.Store.InsertAggregatedPrice(
		storeCtx, currency, medianValue, minuteTS,
	); err != nil {
		log.Errorf("Storing aggregated price for %s: %v", currency, err)
		return
	}

	stored := pricestore.StoredPrice{
		Value:     medianValue,
		Currency:  currency,
		MinuteTS:  minuteTS,
		Timestamp: time.Unix(minuteTS, 0).UTC(),
	}

	// Update the in-memory latest price.
	o.mu.Lock()
	o.latest[currency] = stored
	o.mu.Unlock()

	log.Infof("Aggregated %s price: %d (from %d feed(s), minute %d)",
		currency, medianValue, len(prices), minuteTS)

	// Broadcast to subscribers.
	o.broadcast(PriceUpdate{Currency: currency, Price: stored})
}

// broadcast sends a PriceUpdate to all active subscribers in a non-blocking
// manner.
func (o *Oracle) broadcast(update PriceUpdate) {
	o.subsMu.Lock()
	defer o.subsMu.Unlock()

	for _, ch := range o.subs {
		select {
		case ch <- update:
		default:
			log.Warnf("Subscriber channel full, dropping %s update",
				update.Currency)
		}
	}
}
