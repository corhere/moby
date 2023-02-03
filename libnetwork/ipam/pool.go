package ipam

import (
	"fmt"
	"net/netip"

	"github.com/docker/docker/libnetwork/bitmap"
)

// A Pool is a set of IP network prefixes that may be individually allocated and
// released.
//
// Pool values are not safe for concurrent use.
type Pool struct {
	ranges      []RangeAllocator
	nextRange   int    // index into ranges to allocate the next network.
	nextOrdinal uint64 // ordinal in nextRange to start allocating the next network from.
}

// NewPool returns a new pool which will allocate subnets from the given set of
// network ranges. The base network prefixes of the ranges must not overlap.
func NewPool(ranges []Range) (*Pool, error) {
	allocs := make([]RangeAllocator, len(ranges))
	for i, n := range ranges {
		for _, r := range ranges[:i] {
			if n.Base().Overlaps(r.Base()) {
				return nil, fmt.Errorf("base network prefix %v overlaps %v", n.Base(), r.Base())
			}
		}
		allocs[i] = n.Allocator()
	}

	return &Pool{ranges: allocs}, nil
}

// Allocate allocates an available network prefix from the pool.
//
// The returned prefix will not be available for allocation again until after it
// is released with [Pool.Release]. Allocate makes a best-effort attempt not to
// allocate a prefix which was recently released.
func (p *Pool) Allocate() (prefix netip.Prefix, ok bool) {
	// Approximate allocating the least-recently-used prefix by looking for
	// an available prefix starting from the (range, ordinal) immediately
	// following the most recent allocation.

	if len(p.ranges) == 0 {
		return netip.Prefix{}, false
	}

	// First, scan the right half of the "current" range.
	currRange := &p.ranges[p.nextRange]
	pfx, n, ok := currRange.Allocate(bitmap.WithRange(p.nextOrdinal, currRange.Len()-1))
	if ok {
		p.setNext(p.nextRange, n)
		return pfx, true
	}

	// Scan all the other ranges.
	for r := p.nextRange + 1; r < len(p.ranges); r++ {
		pfx, n, ok = p.ranges[r].Allocate()
		if ok {
			p.setNext(r, n)
			return pfx, true
		}
	}
	for r := 0; r < p.nextRange; r++ {
		pfx, n, ok = p.ranges[r].Allocate()
		if ok {
			p.setNext(r, n)
			return pfx, true
		}
	}

	// Finally, scan the left half of currRange.
	pfx, n, ok = currRange.Allocate(bitmap.WithRange(0, p.nextOrdinal))
	if ok {
		p.setNext(p.nextRange, n)
		return pfx, true
	}

	return netip.Prefix{}, false
}

func (p *Pool) setNext(currRange int, currN uint64) {
	if currN >= p.ranges[currRange].Len()-1 {
		// Last prefix in currRange. The next allocation needs to start from the following range.
		p.nextRange, p.nextOrdinal = currRange+1, 0
		if p.nextRange >= len(p.ranges) {
			p.nextRange = 0
		}
	} else {
		p.nextRange, p.nextOrdinal = currRange, currN+1
	}
}

// Release returns prefix to the pool, making it available for future
// allocations. It returns whether prefix is a member of the pool, irrespective
// of its allocation status.
//
// Release is idempotent: releasing an already-released prefix is not an error.
//
// Only prefixes which were allocated from the pool may be released back to the
// same pool. Attempting to release other prefixes has no effect. Release cannot
// be used to append new prefixes to the pool.
func (p *Pool) Release(prefix netip.Prefix) (ok bool) {
	for i := range p.ranges {
		ok = ok || p.ranges[i].Release(prefix)
	}
	return ok
}
