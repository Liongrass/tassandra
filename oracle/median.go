package oracle

import (
	"sort"

	"github.com/liongrass/tassandra/pricefeed"
)

// median returns the median value across the given prices. For an even-length
// slice the lower of the two middle values is returned to avoid fractional
// truncation. Returns 0 if prices is empty.
func median(prices []pricefeed.Price) uint64 {
	switch len(prices) {
	case 0:
		return 0
	case 1:
		return prices[0].Value
	}

	vals := make([]uint64, len(prices))
	for i, p := range prices {
		vals[i] = p.Value
	}

	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })

	mid := len(vals) / 2
	if len(vals)%2 == 0 {
		// Return the lower middle to avoid rounding up.
		return vals[mid-1]
	}

	return vals[mid]
}
