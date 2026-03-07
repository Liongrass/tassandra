package tassandra

import "github.com/liongrass/tassandra/pricefeed"

// AssetConfig maps a single Taproot Asset to a fiat currency and a per-asset
// markup. One AssetConfig is created per [Asset] stanza in tassandra.conf.
type AssetConfig struct {
	// AssetID is the hex-encoded Taproot Asset ID or group key, matching
	// the value used in the [Asset "hex"] section header.
	AssetID string

	// Currency is the fiat currency this asset is denominated in
	// (e.g. USD, EUR, GBP).
	Currency pricefeed.FiatCurrency

	// MarkupPPM is the markup applied to the raw aggregated price when
	// serving this asset over gRPC, expressed in parts per million.
	// For example, 5000 represents a 0.5% markup.
	MarkupPPM uint64
}
