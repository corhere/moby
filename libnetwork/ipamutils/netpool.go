package ipamutils

import (
	"fmt"
	"net/netip"

	"github.com/docker/docker/libnetwork/bitmap"
)

// A NetworkPool is a set of IP network prefixes that may be individually
// allocated and released.
//
// NetworkPool values are not safe for concurrent use.
type NetworkPool struct {
	chunks      []NetworkChunk
	nextChunk   int    // index into chunks to allocate the next network.
	nextOrdinal uint64 // ordinal in nextChunk to start allocating the next network from.
}

// NewPool returns a new pool containing the set of network prefixes described
// by nets. The base network prefixes must not overlap.
func NewPool(nets []NetworkToSplit) (*NetworkPool, error) {
	chunks := make([]NetworkChunk, len(nets))
	for i, n := range nets {
		base, err := netip.ParsePrefix(n.Base)
		if err != nil {
			return nil, fmt.Errorf("invalid base network prefix at index %v: %w", i, err)
		}
		for _, chk := range chunks[:i] {
			if base.Overlaps(chk.Base()) {
				return nil, fmt.Errorf("base network prefix %v overlaps %v", base, chk.Base())
			}
		}
		chunks[i], err = NewChunk(base, n.Size)
		if err != nil {
			return nil, fmt.Errorf("invalid size at index %v: %w", i, err)
		}
	}

	return &NetworkPool{chunks: chunks}, nil
}

// Allocate allocates an available network prefix from the pool.
//
// The returned prefix will not be available for allocation again until after it
// is released with [p.Release]. Allocate makes a best-effort attempt not to
// allocate a prefix which was recently released.
func (p *NetworkPool) Allocate() (prefix netip.Prefix, ok bool) {
	// Approximate allocating the least-recently-used prefix by looking for
	// an available prefix starting from the (chunk, ordinal) immediately
	// following the most recent allocation.

	if len(p.chunks) == 0 {
		return netip.Prefix{}, false
	}

	// First, scan the right half of the "current" chunk.
	currChunk := &p.chunks[p.nextChunk]
	pfx, n, ok := currChunk.Allocate(bitmap.WithRange(p.nextOrdinal, currChunk.Len()-1))
	if ok {
		p.setNext(p.nextChunk, n)
		return pfx, true
	}

	// Scan all the other chunks.
	for chk := p.nextChunk + 1; chk < len(p.chunks); chk++ {
		pfx, n, ok = p.chunks[chk].Allocate()
		if ok {
			p.setNext(chk, n)
			return pfx, true
		}
	}
	for chk := 0; chk < p.nextChunk; chk++ {
		pfx, n, ok = p.chunks[chk].Allocate()
		if ok {
			p.setNext(chk, n)
			return pfx, true
		}
	}

	// Finally, scan the left half of currChunk.
	pfx, n, ok = currChunk.Allocate(bitmap.WithRange(0, p.nextOrdinal))
	if ok {
		p.setNext(p.nextChunk, n)
		return pfx, true
	}

	return netip.Prefix{}, false
}

func (p *NetworkPool) setNext(currChunk int, currN uint64) {
	if currN >= p.chunks[currChunk].Len()-1 {
		// Last prefix in currChunk. The next allocation needs to start from the following chunk.
		p.nextChunk, p.nextOrdinal = currChunk+1, 0
		if p.nextChunk >= len(p.chunks) {
			p.nextChunk = 0
		}
	} else {
		p.nextChunk, p.nextOrdinal = currChunk, currN+1
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
func (p *NetworkPool) Release(prefix netip.Prefix) (ok bool) {
	for i := range p.chunks {
		ok = ok || p.chunks[i].Release(prefix)
	}
	return ok
}
