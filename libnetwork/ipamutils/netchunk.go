package ipamutils

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"net/netip"

	"github.com/docker/docker/libnetwork/bitmap"
)

// A NetworkChunk is a set of equally-sized IP subnets subdivided from a common
// network prefix that may be individually allocated and released.
//
// NetworkChunk values are not safe for concurrent use.
type NetworkChunk struct {
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

// NewChunk returns a new NetworkChunk which subdivides base into equally-sized
// subnets with a prefix length of subnetBits.
// For example, base=10.1.0.0/16 and subnetBits=20 will yield the set of
// sixteen networks:
//
//	10.1.0.0/20
//	10.1.16.0/20
//	10.1.32.0/20
//	...
//	10.1.224.0/20
//	10.1.240.0/20
func NewChunk(base netip.Prefix, subnetBits int) (NetworkChunk, error) {
	if !base.IsValid() {
		return NetworkChunk{}, errInvalidPrefix
	}
	if subnetBits > base.Addr().BitLen() || base.Bits() > subnetBits {
		return NetworkChunk{}, fmt.Errorf("subnet bits %v out of range for base prefix %v", subnetBits, base)
	}

	// How many subnets can base be subdivided into? Saturating arithmetic.
	lgn := subnetBits - base.Bits() // log2(n)
	var n uint64 = math.MaxUint64
	if lgn < 64 {
		n = 1 << lgn
	}

	return NetworkChunk{
		base:      base.Masked(),
		subbits:   uint8(subnetBits),
		allocated: bitmap.New(n),
	}, nil
}

// Base returns the network prefix being subdivided.
func (c *NetworkChunk) Base() netip.Prefix {
	return c.base
}

// Len returns the total number of network prefixes in c.
func (c *NetworkChunk) Len() uint64 {
	return c.allocated.Bits()
}

// Allocate allocates an available network prefix and returns the allocated prefix along with its ordinal.
//
// This function panics if opts specify an out-of-bounds range, like the slice operator.
func (c *NetworkChunk) Allocate(opts ...bitmap.RangeOpt) (prefix netip.Prefix, ordinal uint64, ok bool) {
	n, err := c.allocated.SetAny(opts...)
	if err != nil {
		if errors.Is(err, bitmap.ErrNoBitAvailable) {
			return netip.Prefix{}, 0, false
		}
		panic(err)
	}

	return c.prefixOf(n), n, true
}

// Release marks prefix as available for future allocations. It returns whether
// prefix is a member of the chunk, irrespective of its allocation status.
//
// Release is idempotent: releasing an already-released prefix is not an error.
//
// Only prefixes which were allocated from the chunk may be released back to the
// same chunk. Attempting to release other prefixes has no effect. Release cannot
// be used to append new prefixes to the chunk.
func (c *NetworkChunk) Release(p netip.Prefix) bool {
	n, ok := c.ordinalOf(p)
	if !ok {
		return false
	}
	if err := c.allocated.Unset(n); err != nil {
		panic(err)
	}
	return true
}

// prefixOf returns c.base + (ordinal << c.subbits).
func (c *NetworkChunk) prefixOf(ordinal uint64) netip.Prefix {
	addend := new(big.Int).SetUint64(ordinal)
	addend.Lsh(addend, uint(c.base.Addr().BitLen())-uint(c.subbits))

	var netaddr netip.Addr
	if c.base.Addr().Is4() {
		a := c.base.Addr().As4()
		addr := new(big.Int).SetBytes(a[:])
		addr.Add(addr, addend).FillBytes(a[:])
		netaddr = netip.AddrFrom4(a)
	} else {
		a := c.base.Addr().As16()
		addr := new(big.Int).SetBytes(a[:])
		addr.Add(addr, addend).FillBytes(a[:])
		netaddr = netip.AddrFrom16(a)
	}
	return netip.PrefixFrom(netaddr, int(c.subbits))
}

// ordinalOf returns the ordinal for which c.prefixOf(ordinal) == p.
func (c *NetworkChunk) ordinalOf(p netip.Prefix) (ordinal uint64, ok bool) {
	if !p.IsValid() || p.Bits() != int(c.subbits) || !c.base.Overlaps(p) {
		return 0, false
	}
	p = p.Masked()

	var addr big.Int
	if p.Addr().Is4() {
		a := p.Addr().As4()
		addr.SetBytes(a[:])
	} else {
		a := p.Addr().As16()
		addr.SetBytes(a[:])
	}

	// Extract the subnet part of p as an integer.
	// E.g. given c.base = 10.42.0.0/16 and c.subbits = 20,
	// when p.Masked() = 10.42.224.0/20
	//
	//    10    .   42    .   224   .    0
	// 0000 1010 0010 1010 1110 0000 0000 0000
	// PPPP PPPP PPPP PPPP SSSS HHHH HHHH HHHH
	//
	// (P = prefix, S = subnet id, H = host)
	//
	// we want to extract the S bits as an integer.
	// Clear P, then right-shift until S is in low order bits.

	submask := big.NewInt(1)
	submask.Lsh(submask, uint(c.base.Addr().BitLen()-c.base.Bits()))
	submask.Sub(submask, big.NewInt(1))
	addr.And(&addr, submask)
	addr.Rsh(&addr, uint(p.Addr().BitLen()-p.Bits()))

	if !addr.IsUint64() {
		panic(fmt.Sprintf("bug: got out of range value %v for subnet ordinal", addr))
	}

	return addr.Uint64(), true
}
