package pricefeed

import (
	"testing"
	"time"
)

func TestParsePriceString(t *testing.T) {
	tests := []struct {
		input   string
		want    uint64
		wantErr bool
	}{
		// Whole numbers.
		{"95000", 95000 * PriceScale, false},
		// Two decimal places.
		{"95000.50", 9500050000000, false},
		// Eight decimal places (full precision).
		{"95000.12345678", 9500012345678, false},
		// More than 8 decimal places — truncated, not rounded.
		{"95000.123456789", 9500012345678, false},
		// Fewer than 8 decimal places — zero-padded.
		{"95000.1", 9500010000000, false},
		// Leading/trailing whitespace.
		{"  95000.50  ", 9500050000000, false},
		// Zero.
		{"0", 0, false},
		{"0.00000000", 0, false},
		// Error cases.
		{"", 0, true},
		{"abc", 0, true},
		{"95000.abc", 0, true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			got, err := parsePriceString(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parsePriceString(%q) error = %v, wantErr %v",
					tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("parsePriceString(%q) = %d, want %d",
					tc.input, got, tc.want)
			}
		})
	}
}

// TestFeedInterfaceCompliance verifies that all concrete feed types satisfy
// the PriceFeed interface at compile time.
func TestFeedInterfaceCompliance(t *testing.T) {
	timeout := 10 * time.Second

	var _ PriceFeed = NewBinanceFeed(timeout)
	var _ PriceFeed = NewKrakenFeed(timeout)
	var _ PriceFeed = NewCoinbaseFeed(timeout)
	var _ PriceFeed = NewBitstampFeed(timeout)
}
