package tassandra_test

import (
	"context"
	"math/big"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	tassandra "github.com/liongrass/tassandra"
	"github.com/liongrass/tassandra/oracle"
	"github.com/liongrass/tassandra/pricefeed"
	"github.com/liongrass/tassandra/pricestore"
	"github.com/liongrass/tassandra/tassandrarpc/priceoraclerpc"
)

const (
	usdAssetHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	eurAssetHex = "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2"
)

// mockFeed is a minimal PriceFeed returning fixed values for testing.
type mockFeed struct {
	name   string
	values map[pricefeed.FiatCurrency]uint64
}

func (m *mockFeed) Name() string { return m.name }
func (m *mockFeed) FetchPrice(_ context.Context,
	c pricefeed.FiatCurrency) (pricefeed.Price, error) {

	v, ok := m.values[c]
	if !ok {
		return pricefeed.Price{}, pricefeed.ErrCurrencyNotSupported
	}
	return pricefeed.Price{
		Value: v, Currency: c, Exchange: m.name, Timestamp: time.Now(),
	}, nil
}

// setupRpcServer creates and starts an oracle with the given feed values
// and returns a ready RpcServer.
func setupRpcServer(t *testing.T,
	feedValues map[pricefeed.FiatCurrency]uint64,
	assets []tassandra.AssetConfig) *tassandra.RpcServer {

	t.Helper()

	store, err := pricestore.NewSQLiteStore(
		filepath.Join(t.TempDir(), "test.db"),
	)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	o, err := oracle.New(oracle.Config{
		Feeds: []oracle.FeedConfig{
			{
				Feed:       &mockFeed{name: "test", values: feedValues},
				Currencies: currenciesFrom(feedValues),
			},
		},
		Store:        store,
		PollInterval: time.Minute,
		FetchTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("oracle.New: %v", err)
	}
	if err := o.Start(); err != nil {
		t.Fatalf("oracle.Start: %v", err)
	}
	t.Cleanup(o.Stop)

	return tassandra.NewRpcServer(o, assets)
}

func currenciesFrom(
	m map[pricefeed.FiatCurrency]uint64) []pricefeed.FiatCurrency {

	currencies := make([]pricefeed.FiatCurrency, 0, len(m))
	for c := range m {
		currencies = append(currencies, c)
	}
	return currencies
}

func TestQueryAssetRates_BtcPayment(t *testing.T) {
	const rawPrice = 9_500_000_000_000 // $95,000.00000000
	const markupPPM = 5_000            // 0.5%

	srv := setupRpcServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: rawPrice},
		[]tassandra.AssetConfig{
			{AssetID: usdAssetHex, Currency: pricefeed.USD, MarkupPPM: markupPPM},
		},
	)

	resp, err := srv.QueryAssetRates(context.Background(),
		&priceoraclerpc.QueryAssetRatesRequest{
			SubjectAsset: &priceoraclerpc.AssetSpecifier{
				Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
					AssetIdStr: usdAssetHex,
				},
			},
			// nil PaymentAsset → BTC
		},
	)
	if err != nil {
		t.Fatalf("QueryAssetRates: %v", err)
	}

	ok := resp.GetOk()
	if ok == nil {
		t.Fatalf("expected ok response, got error: %v", resp.GetError())
	}

	rates := ok.AssetRates

	// Subject asset rate: rawPrice with 0.5% markup.
	// Expected = 9500000000000 * 1005000 / 1000000 = 9547500000000
	wantSubject := applyMarkupBig(rawPrice, markupPPM)
	gotSubject := rates.SubjectAssetRate.Coefficient
	if gotSubject != wantSubject {
		t.Errorf("SubjectAssetRate: got %s, want %s", gotSubject, wantSubject)
	}
	if rates.SubjectAssetRate.Scale != 8 {
		t.Errorf("SubjectAssetRate.Scale: got %d, want 8",
			rates.SubjectAssetRate.Scale)
	}

	// Payment asset is BTC: 100 billion msats, scale 0.
	if rates.PaymentAssetRate.Coefficient != "100000000000" {
		t.Errorf("PaymentAssetRate: got %s, want 100000000000",
			rates.PaymentAssetRate.Coefficient)
	}
	if rates.PaymentAssetRate.Scale != 0 {
		t.Errorf("PaymentAssetRate.Scale: got %d, want 0",
			rates.PaymentAssetRate.Scale)
	}

	// Expiry should be in the future.
	if rates.ExpiryTimestamp <= uint64(time.Now().Unix()) {
		t.Errorf("ExpiryTimestamp %d is not in the future",
			rates.ExpiryTimestamp)
	}
}

func TestQueryAssetRates_ZeroMarkup(t *testing.T) {
	const rawPrice = 9_500_000_000_000

	srv := setupRpcServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: rawPrice},
		[]tassandra.AssetConfig{
			{AssetID: usdAssetHex, Currency: pricefeed.USD, MarkupPPM: 0},
		},
	)

	resp, err := srv.QueryAssetRates(context.Background(),
		&priceoraclerpc.QueryAssetRatesRequest{
			SubjectAsset: &priceoraclerpc.AssetSpecifier{
				Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
					AssetIdStr: usdAssetHex,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("QueryAssetRates: %v", err)
	}

	ok := resp.GetOk()
	if ok == nil {
		t.Fatalf("expected ok response, got error: %v", resp.GetError())
	}

	wantCoeff := strconv.FormatUint(rawPrice, 10)
	if ok.AssetRates.SubjectAssetRate.Coefficient != wantCoeff {
		t.Errorf("Coefficient: got %s, want %s",
			ok.AssetRates.SubjectAssetRate.Coefficient, wantCoeff)
	}
}

func TestQueryAssetRates_TwoTapAssets(t *testing.T) {
	const usdPrice = 9_500_000_000_000 // $95,000
	const eurPrice = 8_800_000_000_000 // €88,000

	srv := setupRpcServer(t,
		map[pricefeed.FiatCurrency]uint64{
			pricefeed.USD: usdPrice,
			pricefeed.EUR: eurPrice,
		},
		[]tassandra.AssetConfig{
			{AssetID: usdAssetHex, Currency: pricefeed.USD, MarkupPPM: 1_000},
			{AssetID: eurAssetHex, Currency: pricefeed.EUR, MarkupPPM: 1_000},
		},
	)

	resp, err := srv.QueryAssetRates(context.Background(),
		&priceoraclerpc.QueryAssetRatesRequest{
			SubjectAsset: &priceoraclerpc.AssetSpecifier{
				Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
					AssetIdStr: usdAssetHex,
				},
			},
			PaymentAsset: &priceoraclerpc.AssetSpecifier{
				Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
					AssetIdStr: eurAssetHex,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("QueryAssetRates: %v", err)
	}

	ok := resp.GetOk()
	if ok == nil {
		t.Fatalf("expected ok response, got error: %v", resp.GetError())
	}

	wantSubject := applyMarkupBig(usdPrice, 1_000)
	wantPayment := applyMarkupBig(eurPrice, 1_000)

	if ok.AssetRates.SubjectAssetRate.Coefficient != wantSubject {
		t.Errorf("SubjectAssetRate: got %s, want %s",
			ok.AssetRates.SubjectAssetRate.Coefficient, wantSubject)
	}
	if ok.AssetRates.PaymentAssetRate.Coefficient != wantPayment {
		t.Errorf("PaymentAssetRate: got %s, want %s",
			ok.AssetRates.PaymentAssetRate.Coefficient, wantPayment)
	}
}

func TestQueryAssetRates_UnknownAsset(t *testing.T) {
	srv := setupRpcServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: 9_500_000_000_000},
		[]tassandra.AssetConfig{},
	)

	resp, err := srv.QueryAssetRates(context.Background(),
		&priceoraclerpc.QueryAssetRatesRequest{
			SubjectAsset: &priceoraclerpc.AssetSpecifier{
				Id: &priceoraclerpc.AssetSpecifier_AssetIdStr{
					AssetIdStr: usdAssetHex,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("QueryAssetRates: %v", err)
	}

	errResp := resp.GetError()
	if errResp == nil {
		t.Fatal("expected error response for unknown asset, got ok")
	}
	if errResp.Code != priceoraclerpc.ErrorCode_UNSUPPORTED_ASSET_ORACLE_ERROR_CODE {
		t.Errorf("ErrorCode: got %v, want UNSUPPORTED_ASSET",
			errResp.Code)
	}
}

func TestQueryAssetRates_RawBytesAssetID(t *testing.T) {
	const rawPrice = 9_500_000_000_000

	assetBytes := make([]byte, 32)
	assetBytes[0] = 0xde
	assetBytes[1] = 0xad

	srv := setupRpcServer(t,
		map[pricefeed.FiatCurrency]uint64{pricefeed.USD: rawPrice},
		[]tassandra.AssetConfig{
			// Register using the hex string equivalent.
			{AssetID: "dead" + "000000000000000000000000000000000000000000000000000000000000",
				Currency: pricefeed.USD, MarkupPPM: 0},
		},
	)

	resp, err := srv.QueryAssetRates(context.Background(),
		&priceoraclerpc.QueryAssetRatesRequest{
			SubjectAsset: &priceoraclerpc.AssetSpecifier{
				// Query using raw bytes; server normalises to hex.
				Id: &priceoraclerpc.AssetSpecifier_AssetId{
					AssetId: assetBytes,
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("QueryAssetRates: %v", err)
	}
	if resp.GetOk() == nil {
		t.Fatalf("expected ok response, got: %v", resp.GetError())
	}
}

// applyMarkupBig replicates the server's markup logic for test assertions.
func applyMarkupBig(value, markupPPM uint64) string {
	v := new(big.Int).SetUint64(value)
	if markupPPM == 0 {
		return v.String()
	}
	factor := new(big.Int).SetUint64(1_000_000 + markupPPM)
	result := new(big.Int).Mul(v, factor)
	result.Div(result, new(big.Int).SetUint64(1_000_000))
	return result.String()
}
