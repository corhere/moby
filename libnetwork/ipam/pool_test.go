package ipam

import (
	"fmt"
	"math"
	"net/netip"
	"testing"

	"github.com/docker/docker/libnetwork/ipamutils"
	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestNetworkPool(t *testing.T) {
	t.Run("Len=0", func(t *testing.T) {
		pool, err := NewPool(nil)
		assert.NilError(t, err)

		got, ok := pool.Allocate()
		assert.Check(t, !ok, "unexpected allocation %v", got)

		assert.Check(t, !pool.Release(netip.Prefix{}))
		assert.Check(t, !pool.Release(netip.MustParsePrefix("10.0.0.0/8")))
	})

	t.Run("Len=1", func(t *testing.T) {
		want := netip.MustParsePrefix("172.16.0.0/16")

		pool, err := NewPool([]ipamutils.NetworkToSplit{{Base: want.String(), Size: want.Bits()}})
		assert.NilError(t, err)

		got, ok := pool.Allocate()
		assert.Check(t, ok)
		assert.Check(t, is.Equal(got, want))

		got, ok = pool.Allocate()
		assert.Check(t, !ok, "unexpected allocation %v", got)

		assert.Check(t, pool.Release(want))
		got, ok = pool.Allocate()
		assert.Check(t, ok)
		assert.Check(t, is.Equal(got, want))
	})

	t.Run("Len=MaxUint64", func(t *testing.T) {
		// NB: the last prefix in a chunk of 2**64 networks cannot be
		// allocated because the length of the chunk is limited to
		// MaxUint64, 2**64-1.
		pool, err := NewPool([]ipamutils.NetworkToSplit{
			{Base: "aaaa::/16", Size: 80},
			{Base: "bbbb::/16", Size: 80},
		})
		assert.NilError(t, err)
		// Moving the "current" position by allocating 2**64 times
		// would take too long to be practical in a unit test.
		pool.nextOrdinal = math.MaxUint64 - 1
		for _, want := range []netip.Prefix{
			netip.MustParsePrefix("aaaa:ffff:ffff:ffff:fffe::/80"),
			netip.MustParsePrefix("bbbb::/80"),
			netip.MustParsePrefix("bbbb::1:0:0:0/80"),
		} {
			got, ok := pool.Allocate()
			assert.Check(t, ok)
			assert.Check(t, is.Equal(got, want))
		}

		pool.nextOrdinal = math.MaxUint64 - 1
		for _, want := range []netip.Prefix{
			netip.MustParsePrefix("bbbb:ffff:ffff:ffff:fffe::/80"),
			netip.MustParsePrefix("aaaa::/80"),
			netip.MustParsePrefix("aaaa::1:0:0:0/80"),
		} {
			got, ok := pool.Allocate()
			assert.Check(t, ok)
			assert.Check(t, is.Equal(got, want))
		}
	})

	for _, tt := range [][]ipamutils.NetworkToSplit{
		{
			{Base: "10.0.0.0/14", Size: 16},
		},
		{
			{Base: "10.0.0.0/15", Size: 16},
			{Base: "10.2.0.0/15", Size: 16},
		},
		{
			{Base: "10.0.0.0/16", Size: 16},
			{Base: "10.1.0.0/16", Size: 16},
			{Base: "10.2.0.0/16", Size: 16},
			{Base: "10.3.0.0/16", Size: 16},
		},
	} {
		t.Run(fmt.Sprintf("Chunks=%d", len(tt)), func(t *testing.T) {
			t.Run("AllocateAll", func(t *testing.T) {
				pool, err := NewPool(tt)
				assert.NilError(t, err)

				for i, want := range []netip.Prefix{
					netip.MustParsePrefix("10.0.0.0/16"),
					netip.MustParsePrefix("10.1.0.0/16"),
					netip.MustParsePrefix("10.2.0.0/16"),
					netip.MustParsePrefix("10.3.0.0/16"),
					{},
				} {
					got, ok := pool.Allocate()
					assert.Check(t, is.Equal(ok, want.IsValid()), "%d", i)
					assert.Check(t, is.Equal(got, want), "%d", i)
				}
			})

			t.Run("ReallocateOne", func(t *testing.T) {
				pool, err := NewPool(tt)
				assert.NilError(t, err)

				for {
					if _, ok := pool.Allocate(); !ok {
						break
					}
				}

				want := netip.MustParsePrefix("10.2.0.0/16")
				assert.Check(t, pool.Release(want))

				got, ok := pool.Allocate()
				assert.Check(t, ok)
				assert.Check(t, is.Equal(got, want))

				_, ok = pool.Allocate()
				assert.Check(t, !ok)
			})

			t.Run("AllocateSerially", func(t *testing.T) {
				pool, err := NewPool(tt)
				assert.NilError(t, err)

				for i := 0; i < 2; i++ {
					p, ok := pool.Allocate()
					assert.Check(t, ok)
					assert.Check(t, pool.Release(p))
				}

				got, ok := pool.Allocate()
				assert.Check(t, ok)
				assert.Check(t, is.Equal(got, netip.MustParsePrefix("10.2.0.0/16")))
			})

			t.Run("ReallocateSerially", func(t *testing.T) {
				pool, err := NewPool(tt)
				assert.NilError(t, err)

				for {
					if _, ok := pool.Allocate(); !ok {
						break
					}
				}
				// With all prefixes allocated, we can manipulate the "current"
				// position to wherever we want by releasing the prefix at the
				// preceding position and immediately allocating.
				assert.Check(t, pool.Release(netip.MustParsePrefix("10.1.0.0/16")))
				got, ok := pool.Allocate()
				assert.Check(t, ok)
				assert.Check(t, is.Equal(got, netip.MustParsePrefix("10.1.0.0/16")))

				// Release all the prefixes aside from current. Because of the
				// aforementioned manipulation, current+1 should be allocated
				// next despite it being the most-recently released prefix.
				assert.Check(t, pool.Release(netip.MustParsePrefix("10.0.0.0/16")))
				assert.Check(t, pool.Release(netip.MustParsePrefix("10.1.0.0/16")))
				assert.Check(t, pool.Release(netip.MustParsePrefix("10.3.0.0/16")))

				got, ok = pool.Allocate()
				assert.Check(t, ok)
				assert.Check(t, is.Equal(got, netip.MustParsePrefix("10.3.0.0/16")))
			})
		})
	}
}
