package ipam

import (
	"errors"
	"fmt"
	"math"
	"net/netip"

	"github.com/docker/docker/libnetwork/bitmap"
	"github.com/docker/docker/libnetwork/ipbits"
)

// A Range is a set of contiguous, equally-sized IP subnets with a common
// network prefix that may be individually allocated and released.
//
// Range values are not safe for concurrent use.
type Range struct {
	// Invariant: is in canonical form (base == base.Masked())
	base netip.Prefix

	// Network mask of each sub-network
	// Invariants:
	//   base.Addr().BitLen() >= subbits >= base.Bits()
	subbits uint8

	allocated *bitmap.Bitmap
}

var (
	errInvalidPrefix = errors.New("invalid prefix")
)

// NewRange returns a Range of all subnets with a prefix of base and a prefix
// length of bits.
//
// For example, base=10.1.0.0/16 and bits=20 will yield a Range containing the
// following set of sixteen subnets:
//
//	10.1.0.0/20
//	10.1.16.0/20
//	10.1.32.0/20
//	...
//	10.1.224.0/20
//	10.1.240.0/20
//
// Only the first 2**64-1 subnets in the range will be available for allocation.
// This limitation may be lifted in the future.
func NewRange(base netip.Prefix, bits int) (Range, error) {
	if !base.IsValid() {
		return Range{}, errInvalidPrefix
	}
	if bits > base.Addr().BitLen() || base.Bits() > bits {
		return Range{}, fmt.Errorf("bits %v out of range for base prefix %v", bits, base)
	}

	// How many subnets can base be subdivided into? Saturating arithmetic.
	lgn := bits - base.Bits() // log2(n)
	var n uint64 = math.MaxUint64
	if lgn < 64 {
		n = 1 << lgn
	}

	return Range{
		base:      base.Masked(),
		subbits:   uint8(bits),
		allocated: bitmap.New(n),
	}, nil
}

// Base returns the network prefix being subdivided.
func (r *Range) Base() netip.Prefix {
	return r.base
}

// Len returns the total number of allocatable subnets.
func (r *Range) Len() uint64 {
	return r.allocated.Bits()
}

// Allocate allocates and returns an available subnet, along with its ordinal
// subnet ID.
//
// Allocate panics if opts specify an out-of-bounds range.
func (r *Range) Allocate(opts ...bitmap.RangeOpt) (prefix netip.Prefix, ordinal uint64, ok bool) {
	n, err := r.allocated.SetAny(opts...)
	if err != nil {
		if errors.Is(err, bitmap.ErrNoBitAvailable) {
			return netip.Prefix{}, 0, false
		}
		panic(err)
	}

	return r.subnet(n), n, true
}

// Release marks p as available for future allocations. It returns whether p is
// a member of the range, irrespective of its allocation status.
//
// Release is idempotent: releasing an already-released subnet is not an error.
//
// Only prefixes which were allocated from the range may be released back to the
// same range. Attempting to release other prefixes has no effect. Release cannot
// be used to append new subnets to the range.
func (r *Range) Release(p netip.Prefix) bool {
	n, ok := r.subnetID(p)
	if !ok {
		return false
	}
	if err := r.allocated.Unset(n); err != nil {
		panic(err)
	}
	return true
}

// subnet returns the subnet of c.base with a subnet ID of ordinal.
func (r *Range) subnet(ordinal uint64) netip.Prefix {
	shift := uint(r.base.Addr().BitLen()) - uint(r.subbits)
	return netip.PrefixFrom(ipbits.Add(r.base.Masked().Addr(), ordinal, shift), int(r.subbits))
}

// subnetID returns the ordinal for which s.subnet(ordinal) == p.
func (r *Range) subnetID(p netip.Prefix) (ordinal uint64, ok bool) {
	if !p.IsValid() || p.Bits() != int(r.subbits) || !r.base.Overlaps(p) {
		return 0, false
	}

	// Extract the subnet part of p as an integer.
	// E.g. given base=10.42.0.0/16 and subbits=20,
	// when p.Masked()=10.42.224.0/20
	//
	//    10    .   42    .   224   .    0
	// 0000 1010 0010 1010 1110 0000 0000 0000
	// PPPP PPPP PPPP PPPP SSSS HHHH HHHH HHHH
	//
	// (P = prefix, S = subnet id, H = host)
	//
	// we want to extract the S bits.

	return ipbits.Field(p.Masked().Addr(), uint(r.base.Bits()), uint(p.Bits())), true
}
