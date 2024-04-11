// Package garp provides utilities to send Gratuitous ARP (IPv4) and Unsolicited
// Neighbor Advertisement (IPv6) notifications on behalf of other hosts.
package garp

import (
	"errors"
	"net"
	"net/netip"

	"github.com/mdlayher/arp"
	"github.com/mdlayher/ndp"
	"golang.org/x/net/ipv6"
)

type Socket struct {
	arp *arp.Client
	ndp *ndp.Conn
}

func Dial(ifi *net.Interface) (_ *Socket, err error) {
	var s Socket
	s.arp, err = arp.Dial(ifi)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			s.arp.Close()
		}
	}()

	s.ndp, _, err = ndp.Listen(ifi, ndp.Unspecified)
	if err != nil {
		// TODO: ignore error if IPv6 disabled on iface?
		return nil, err
	}
	defer func() {
		if err != nil {
			s.ndp.Close()
		}
	}()
	// We will never read from the NDP socket, so we can tell the kernel not
	// to waste cycles or memory queueing received packets on the socket.
	var blockAllICMP6 ipv6.ICMPFilter
	blockAllICMP6.SetAll(true)
	if err := s.ndp.SetICMPFilter(&blockAllICMP6); err != nil {
		return nil, err
	}

	return &s, nil
}

func (s *Socket) Close() error {
	return errors.Join(s.arp.Close(), s.ndp.Close())
}

func (s *Socket) Announce(addr netip.Addr, mac net.HardwareAddr) error {
	if addr.Is4() {
		return s.announce4(addr, mac)
	}
	return s.announce6(addr, mac)
}

func (s *Socket) announce4(addr netip.Addr, mac net.HardwareAddr) error {
	p, err := arp.NewPacket(arp.OperationRequest, mac, addr, net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, addr)
	if err != nil {
		return err
	}
	return s.arp.WriteTo(p, p.TargetHardwareAddr)
}

func (s *Socket) announce6(addr netip.Addr, mac net.HardwareAddr) error {
	p := ndp.NeighborAdvertisement{
		Override:      true,
		TargetAddress: addr,
		Options: []ndp.Option{
			&ndp.LinkLayerAddress{
				Direction: ndp.Target,
				Addr:      mac,
			},
		},
	}
	return s.ndp.WriteTo(&p, nil, netip.IPv6LinkLocalAllNodes())
}
