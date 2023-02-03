package ipam

import (
	"fmt"
	"net/netip"

	"github.com/docker/docker/libnetwork/ipamutils"
)

// RangesFrom converts []*NetworkToSplit to the equivalent []Range.
func RangesFrom(nets []*ipamutils.NetworkToSplit) ([]Range, error) {
	ranges := make([]Range, len(nets))
	for i, n := range nets {
		base, err := netip.ParsePrefix(n.Base)
		if err != nil {
			return nil, fmt.Errorf("invalid base network prefix at index %v: %w", i, err)
		}
		ranges[i], err = NewRange(base, n.Size)
		if err != nil {
			return nil, fmt.Errorf("invalid size at index %v: %w", i, err)
		}
	}
	return ranges, nil
}
