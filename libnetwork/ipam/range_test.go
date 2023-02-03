package ipam

import (
	"math"
	"net/netip"
	"testing"

	"gotest.tools/v3/assert"
	is "gotest.tools/v3/assert/cmp"
)

func TestNewRange(t *testing.T) {
	for _, tt := range []struct {
		name       string
		base       netip.Prefix
		subnetBits int
		expectErr  bool
	}{
		{name: "InvalidBase", subnetBits: 16, expectErr: true},
		{name: "TooFewSubnetBits/IPv4", base: netip.MustParsePrefix("192.168.0.0/16"), subnetBits: 8, expectErr: true},
		{name: "TooFewSubnetBits/IPv6", base: netip.MustParsePrefix("2001::/16"), subnetBits: 8, expectErr: true},
		{name: "TooManySubnetBits/IPv4", base: netip.MustParsePrefix("192.168.0.0/16"), subnetBits: 33, expectErr: true},
		{name: "TooManySubnetBits/IPv6", base: netip.MustParsePrefix("2001::/16"), subnetBits: 129, expectErr: true},
		{name: "SingleHostNetwork/IPv4", base: netip.MustParsePrefix("192.168.1.0/24"), subnetBits: 32},
		{name: "SingleHostNetwork/IPv6", base: netip.MustParsePrefix("fe80::/64"), subnetBits: 128},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewRange(tt.base, tt.subnetBits)
			if tt.expectErr {
				assert.Check(t, is.ErrorContains(err, ""))
			} else {
				assert.Check(t, err)
			}
		})
	}
}

func TestRangeAllocate(t *testing.T) {
	for _, tt := range []struct {
		name       string
		base       netip.Prefix
		subnetBits int
	}{
		{name: "IPv4", base: netip.MustParsePrefix("10.1.0.0/16"), subnetBits: 20},
		{name: "IPv6", base: netip.MustParsePrefix("fe80::/10"), subnetBits: 14},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewRange(tt.base, tt.subnetBits)
			assert.NilError(t, err)

			for i := 0; i < 16; i++ {
				p, n, ok := r.Allocate()
				t.Log(p)
				assert.Check(t, ok, "could not allocate network %d", i)
				assert.Check(t, is.Equal(n, uint64(i)))
			}

			p, n, ok := r.Allocate()
			assert.Check(t, !ok, "got unexpected allocation %v (ordinal=%v)", p, n)
		})
	}
}

func BenchmarkRangeAllocate(b *testing.B) {
	r, err := NewRange(netip.MustParsePrefix("aaaa::/16"), 80)
	assert.NilError(b, err)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _, ok := r.Allocate()
		if !ok {
			b.Fatal(i, b.N)
		}
	}
}

func TestRangeRelease(t *testing.T) {
	r, err := NewRange(netip.MustParsePrefix("fe80::/10"), 74)
	assert.NilError(t, err)
	assert.Equal(t, r.Len(), uint64(math.MaxUint64))

	_, _, ok := r.Allocate()
	assert.Assert(t, ok)

	p, n, ok := r.Allocate()
	assert.Assert(t, ok)

	assert.Check(t, r.Release(p))
	p2, n2, ok := r.Allocate()
	assert.Check(t, ok)
	assert.Equal(t, p, p2)
	assert.Equal(t, n, n2)
}
