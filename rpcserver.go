package tassandra

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/liongrass/tassandra/oracle"
	"github.com/liongrass/tassandra/tassandrarpc/priceoraclerpc"
)

const (
	// priceScale is the number of decimal places used in internal price
	// representation (matches pricefeed.PriceScale = 1e8).
	priceScale uint32 = 8

	// btcPaymentRateCoefficient is the number of millisatoshis per BTC,
	// used as the paymentAssetRate when the payment asset is BTC.
	// 1 BTC = 100,000,000 sats = 100,000,000,000 msats.
	btcPaymentRateCoefficient = "100000000000"

	// rateExpirySeconds is how far in the future (in seconds) the
	// returned asset rates are valid. Set to two minutes to cover the
	// oracle's poll interval plus a buffer.
	rateExpirySeconds = 120
)

// RpcServer implements the priceoraclerpc.PriceOracleServer interface,
// making Tassandra a drop-in external price oracle for tapd nodes.
type RpcServer struct {
	priceoraclerpc.UnimplementedPriceOracleServer

	oracle *oracle.Oracle

	// assets maps lowercase hex asset IDs to their currency and markup
	// configuration. Built once at construction time from the daemon config.
	assets map[string]AssetConfig
}

// NewRpcServer creates a new RpcServer. assets is a slice of AssetConfig
// entries as parsed from tassandra.conf; duplicate asset IDs are silently
// deduplicated (last entry wins).
func NewRpcServer(o *oracle.Oracle, assets []AssetConfig) *RpcServer {
	assetMap := make(map[string]AssetConfig, len(assets))
	for _, a := range assets {
		assetMap[strings.ToLower(a.AssetID)] = a
	}

	return &RpcServer{
		oracle: o,
		assets: assetMap,
	}
}

// QueryAssetRates implements priceoraclerpc.PriceOracleServer. It looks up
// the subject asset in the configured asset map, fetches the latest oracle
// price for its currency, applies the per-asset markup, and returns the
// result as FixedPoint rates.
func (s *RpcServer) QueryAssetRates(ctx context.Context,
	req *priceoraclerpc.QueryAssetRatesRequest) (
	*priceoraclerpc.QueryAssetRatesResponse, error) {

	// Resolve the subject asset to an AssetConfig.
	subjectHex, err := assetSpecifierToHex(req.SubjectAsset)
	if err != nil {
		return errorResponse(
			priceoraclerpc.ErrorCode_UNSPECIFIED_ORACLE_ERROR_CODE,
			fmt.Sprintf("invalid subject asset specifier: %v", err),
		), nil
	}

	assetCfg, ok := s.assets[subjectHex]
	if !ok {
		log.Debugf("QueryAssetRates: unknown asset %s", subjectHex)

		return errorResponse(
			priceoraclerpc.ErrorCode_UNSUPPORTED_ASSET_ORACLE_ERROR_CODE,
			fmt.Sprintf("asset %s is not configured", subjectHex),
		), nil
	}

	// Fetch the latest oracle price for this currency.
	stored, ok := s.oracle.LatestPrice(assetCfg.Currency)
	if !ok {
		return errorResponse(
			priceoraclerpc.ErrorCode_UNSPECIFIED_ORACLE_ERROR_CODE,
			fmt.Sprintf("no price available yet for %s",
				assetCfg.Currency),
		), nil
	}

	// Apply markup and decimal display scaling, then build the subject
	// asset rate.
	subjectCoeff := applyMarkup(
		stored.Value, assetCfg.MarkupPPM, assetCfg.DecimalDisplay,
	)
	subjectRate := &priceoraclerpc.FixedPoint{
		Coefficient: subjectCoeff,
		Scale:       priceScale,
	}

	// Build the payment asset rate.
	paymentRate, err := s.paymentAssetRate(req.PaymentAsset)
	if err != nil {
		return errorResponse(
			priceoraclerpc.ErrorCode_UNSPECIFIED_ORACLE_ERROR_CODE,
			fmt.Sprintf("payment asset error: %v", err),
		), nil
	}

	expiry := uint64(time.Now().Add(rateExpirySeconds * time.Second).Unix())

	log.Debugf("QueryAssetRates: asset=%s currency=%s raw=%d "+
		"markup=%dppm decimal_display=%d coeff=%s expiry=%d",
		subjectHex, assetCfg.Currency, stored.Value,
		assetCfg.MarkupPPM, assetCfg.DecimalDisplay, subjectCoeff, expiry)

	return &priceoraclerpc.QueryAssetRatesResponse{
		Result: &priceoraclerpc.QueryAssetRatesResponse_Ok{
			Ok: &priceoraclerpc.QueryAssetRatesOkResponse{
				AssetRates: &priceoraclerpc.AssetRates{
					SubjectAssetRate: subjectRate,
					PaymentAssetRate: paymentRate,
					ExpiryTimestamp:  expiry,
				},
			},
		},
	}, nil
}

// paymentAssetRate returns the FixedPoint rate for the payment asset.
// A nil specifier or an all-zero asset ID is treated as BTC (msats).
// Any other asset ID is looked up in the asset config map.
func (s *RpcServer) paymentAssetRate(
	spec *priceoraclerpc.AssetSpecifier) (*priceoraclerpc.FixedPoint, error) {

	if isZeroAsset(spec) {
		return &priceoraclerpc.FixedPoint{
			Coefficient: btcPaymentRateCoefficient,
			Scale:       0,
		}, nil
	}

	paymentHex, err := assetSpecifierToHex(spec)
	if err != nil {
		return nil, fmt.Errorf("invalid payment asset specifier: %w", err)
	}

	assetCfg, ok := s.assets[paymentHex]
	if !ok {
		return nil, fmt.Errorf(
			"payment asset %s is not configured", paymentHex,
		)
	}

	stored, ok := s.oracle.LatestPrice(assetCfg.Currency)
	if !ok {
		return nil, fmt.Errorf(
			"no price available for payment asset currency %s",
			assetCfg.Currency,
		)
	}

	coeff := applyMarkup(stored.Value, assetCfg.MarkupPPM, assetCfg.DecimalDisplay)

	return &priceoraclerpc.FixedPoint{
		Coefficient: coeff,
		Scale:       priceScale,
	}, nil
}

// applyMarkup applies a PPM markup and a decimal display scaling to a price
// value and returns the result as a decimal string. Uses big.Int to avoid
// uint64 overflow at high prices.
//
// The markup step:
//
//	marked = value * (1_000_000 + markupPPM) / 1_000_000
//
// The decimal display step multiplies by 10^decimalDisplay so that the
// returned coefficient correctly represents base-unit quantities. For example,
// an asset with decimalDisplay=3 has 1000 base units per display unit, so the
// rate coefficient must be scaled up by 10^3 to map a fiat price expressed in
// display units per BTC to base units per BTC.
func applyMarkup(value, markupPPM uint64, decimalDisplay uint8) string {
	v := new(big.Int).SetUint64(value)

	if markupPPM != 0 {
		factor := new(big.Int).SetUint64(1_000_000 + markupPPM)
		divisor := new(big.Int).SetUint64(1_000_000)

		v.Mul(v, factor)
		v.Div(v, divisor)
	}

	if decimalDisplay > 0 {
		scale := new(big.Int).Exp(
			big.NewInt(10), big.NewInt(int64(decimalDisplay)), nil,
		)
		v.Mul(v, scale)
	}

	return v.String()
}

// assetSpecifierToHex normalises any variant of an AssetSpecifier to a
// lowercase hex string for map lookup.
func assetSpecifierToHex(
	spec *priceoraclerpc.AssetSpecifier) (string, error) {

	if spec == nil {
		return "", errors.New("nil asset specifier")
	}

	switch id := spec.Id.(type) {
	case *priceoraclerpc.AssetSpecifier_AssetId:
		return hex.EncodeToString(id.AssetId), nil

	case *priceoraclerpc.AssetSpecifier_AssetIdStr:
		return strings.ToLower(id.AssetIdStr), nil

	case *priceoraclerpc.AssetSpecifier_GroupKey:
		return hex.EncodeToString(id.GroupKey), nil

	case *priceoraclerpc.AssetSpecifier_GroupKeyStr:
		return strings.ToLower(id.GroupKeyStr), nil

	default:
		return "", errors.New("unrecognized asset specifier variant")
	}
}

// isZeroAsset returns true if the specifier is nil, empty, or an all-zero
// asset ID — all of which indicate that the payment asset is BTC.
func isZeroAsset(spec *priceoraclerpc.AssetSpecifier) bool {
	if spec == nil {
		return true
	}

	switch id := spec.Id.(type) {
	case *priceoraclerpc.AssetSpecifier_AssetId:
		for _, b := range id.AssetId {
			if b != 0 {
				return false
			}
		}
		return true

	case *priceoraclerpc.AssetSpecifier_AssetIdStr:
		trimmed := strings.TrimLeft(
			strings.ToLower(id.AssetIdStr), "0",
		)
		return trimmed == ""

	default:
		return false
	}
}

// errorResponse constructs a QueryAssetRatesResponse carrying an error.
func errorResponse(code priceoraclerpc.ErrorCode,
	msg string) *priceoraclerpc.QueryAssetRatesResponse {

	return &priceoraclerpc.QueryAssetRatesResponse{
		Result: &priceoraclerpc.QueryAssetRatesResponse_Error{
			Error: &priceoraclerpc.QueryAssetRatesErrResponse{
				Code:    code,
				Message: msg,
			},
		},
	}
}
