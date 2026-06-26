package libnetwork

import (
	"context"
	"errors"
	"fmt"
	"net"
	strings "strings"
	"sync"
	"time"

	"github.com/containerd/log"
	"github.com/moby/moby/v2/daemon/libnetwork/internal/maputil"
	"github.com/moby/moby/v2/daemon/libnetwork/internal/nftables"
)

var (
	nftablesIngressOnce  sync.Once
	nftablesIngressTable nftables.Table
)

const (
	ingressPublishedPortsSet = "published-ports"
)

// initIngressConfigurationNftables programs the kernel for ingress using
// nftables. Unlike [initIngressConfigurationIPTables], the caller is
// responsible for ensuring that this function is only called once.
func initIngressConfigurationNftables(ctx context.Context, gwIP net.IP) error {
	// Find the bridge interface name for the gateway IP.
	oifName, err := findOIFName(gwIP)
	if err != nil {
		return fmt.Errorf("failed to find gateway bridge interface name for %s: %v", gwIP, err)
	}

	nftablesIngressTable, err = nftables.NewTable(nftables.IPv4, "docker-ingress")
	if err != nil {
		return err
	}
	var tm nftables.Modifier

	tm.Create(nftables.Set{
		Name:        ingressPublishedPortsSet,
		ElementType: nftables.InetProto.Concat(nftables.InetService),
		Flags:       []string{"counter"},
	})

	dnatRule := []string{"fib daddr type local meta l4proto { tcp, udp, sctp }",
		"meta l4proto . th dport @" + ingressPublishedPortsSet, "dnat to", gwIP.String()}
	nftables.BaseChain{
		Name:      "prerouting",
		ChainType: nftables.BaseChainTypeNAT,
		Hook:      nftables.BaseChainHookPrerouting,
		Priority:  nftables.BaseChainPriorityDstNAT,
		Policy:    nftables.BaseChainPolicyAccept,
	}.Builder().
		Rule(dnatRule...).
		Create(&tm)

	nftables.BaseChain{
		Name:      "output",
		ChainType: nftables.BaseChainTypeNAT,
		Hook:      nftables.BaseChainHookOutput,
		Priority:  nftables.BaseChainPriorityDstNAT,
		Policy:    nftables.BaseChainPolicyAccept,
	}.Builder().
		Rule(dnatRule...).
		Create(&tm)

	nftables.BaseChain{
		Name:      "postrouting",
		ChainType: nftables.BaseChainTypeNAT,
		Hook:      nftables.BaseChainHookPostrouting,
		Priority:  nftables.BaseChainPrioritySrcNAT,
		Policy:    nftables.BaseChainPolicyAccept,
	}.Builder().
		Rule(dnatRule...).
		Rule("meta l4proto . th dport @"+ingressPublishedPortsSet, "oifname", oifName,
			"fib saddr type local counter masquerade").
		Rule("meta l4proto . th dport @"+ingressPublishedPortsSet, "iifname !=", oifName,
			"oifname", oifName, "ip daddr", gwIP.String(), "counter masquerade").
		Create(&tm)

	return nftablesIngressTable.Apply(ctx, tm)
}

func (n *Network) syncLBBackendsNftables(ctx context.Context, lb *loadBalancer) bool {
	ep, sb, err := n.findLBEndpointSandbox()
	if err != nil {
		log.G(ctx).Errorf("syncLBBackendsNftables %s/%s: %v", n.ID(), n.Name(), err)
		return false
	}
	if sb.osSbox == nil {
		return false
	}

	natTable, created, err := sb.nftable(nftables.IPv4, "docker-lb-nat")
	if err != nil {
		log.G(ctx).Errorf("Failed to create nftables nat table: %v", err)
		return false
	}
	var natInit nftables.Modifier
	const (
		natServiceVipMap  = "nat-service-vip"
		natPublishPortMap = "nat-publish-port"
		modulus           = 65536
		numgenExpr        = "numgen random mod 65536"
	)
	if created {
		natInit.Create(nftables.Map{
			Name:        natServiceVipMap,
			ElementType: nftables.Typeof("ip daddr").Concat(numgenExpr).MapTo("ip daddr; counter"),
			Flags:       []string{"interval"},
		})

		natInit.Create(nftables.Map{
			Name:        natPublishPortMap,
			ElementType: nftables.Typeof("meta l4proto . th dport").Concat(numgenExpr).MapTo("ip daddr; counter"),
			Flags:       []string{"interval"},
		})

		nftables.BaseChain{
			Name:      "prerouting",
			ChainType: nftables.BaseChainTypeNAT,
			Hook:      nftables.BaseChainHookPrerouting,
			Priority:  nftables.BaseChainPriorityDstNAT,
			Policy:    nftables.BaseChainPolicyAccept,
		}.Builder().
			Rule("dnat to ip daddr .", numgenExpr, "map @"+natServiceVipMap).
			Rule("dnat to meta l4proto . th dport .", numgenExpr, "map @"+natPublishPortMap).
			Create(&natInit)

		nftables.BaseChain{
			Name:      "postrouting",
			ChainType: nftables.BaseChainTypeNAT,
			Hook:      nftables.BaseChainHookPostrouting,
			Priority:  nftables.BaseChainPrioritySrcNAT,
			Policy:    nftables.BaseChainPolicyAccept,
		}.Builder().
			Rule("ct status dnat counter masquerade").
			Create(&natInit)
	}

	// Add IP alias for the VIP to the endpoint
	ifName := findIfaceDstName(sb, ep)
	if ifName == "" {
		log.G(ctx).Errorf("Failed find interface name for endpoint %s(%s) to create LB alias", ep.ID(), ep.Name())
		return false
	}

	backends := maputil.FilterValues(lb.backEnds, func(backend *lbBackend) bool { return !backend.disabled })
	newService := lb.nftBackendsProgrammed == 0 && len(backends) > 0
	delService := lb.nftBackendsProgrammed > 0 && len(backends) == 0

	vip := lb.vip.String()
	if newService {
		log.G(ctx).Debugf("Creating service for vip %s ingressPorts %#v in sbox %.7s (%.7s)", vip, lb.service.ingressPorts, sb.ID(), sb.ContainerID())

		err := sb.osSbox.AddAliasIP(ifName, &net.IPNet{IP: lb.vip, Mask: net.CIDRMask(32, 32)})
		if err != nil {
			log.G(ctx).Errorf("Failed add IP alias %s to network %s LB endpoint interface %s: %v", vip, n.ID(), ifName, err)
			return false
		}
	} else if delService {
		err := sb.osSbox.RemoveAliasIP(ifName, &net.IPNet{IP: lb.vip, Mask: net.CIDRMask(32, 32)})
		if err != nil {
			log.G(ctx).Errorf("Failed remove IP alias %s from network %s LB endpoint interface %s: %v", vip, n.ID(), ifName, err)
			return false
		}
	}

	backendIntervals := nftables.EqualWeightIntervals(backends, modulus)
	var natAdd nftables.Modifier
	for i, b := range backendIntervals {
		bip := b.ip.String()
		natAdd.Create(nftables.MapElement{
			MapName: natServiceVipMap,
			Key:     fmt.Sprintf("%s . %s", vip, i),
			Value:   bip,
		})
		for _, p := range lb.service.ingressPorts {
			natAdd.Create(nftables.MapElement{
				MapName: natPublishPortMap,
				Key:     fmt.Sprintf("%s . %v . %s", strings.ToLower(p.Protocol.String()), p.PublishedPort, i),
				Value:   bip,
			})
		}
	}

	var applyErr error
	if err := sb.ExecFunc(func() { applyErr = natTable.Apply(ctx, natInit, lb.nftClearNAT, natAdd) }); err != nil || applyErr != nil {
		log.G(ctx).Errorf("Failed to apply changes to nftables nat table: %v", errors.Join(err, applyErr))
	} else {
		lb.nftClearNAT = natAdd.Reverse()
		lb.nftBackendsProgrammed = len(backends)
	}

	if n.loadBalancerMode == loadBalancerModeDSR {
		dsrTable, created, err := sb.nftable(nftables.IPv4, "docker-lb-dsr")
		if err != nil {
			log.G(ctx).Errorf("Failed to create nftables dsr table: %v", err)
			return false
		}
		var dsrInit nftables.Modifier
		const (
			dsrVipSet        = "vip"
			dsrRealServerMap = "real-server"
		)
		if created {
			l4protos := []string{"tcp", "udp", "sctp"}

			dsrInit.Create(nftables.Set{
				Name:        dsrVipSet,
				ElementType: nftables.IPv4Addr + "; counter",
			})
			dsrInit.Create(nftables.Map{
				Name:        dsrRealServerMap,
				ElementType: nftables.Typeof("ip daddr").Concat(numgenExpr).MapTo("ip daddr; counter"),
				Flags:       []string{"interval"},
			})

			// Sticky overlay DSR sessions.
			// Split by L4 protocol as nftables 1.0.6 crashes when attempting to
			// compile a ruleset that contains a map update on a 5-tuple key.
			for _, l4proto := range l4protos {
				dsrInit.Create(nftables.Map{
					Name:        "dsr-conntrack-" + l4proto,
					ElementType: nftables.Typeof("ip saddr . th sport . ip daddr . th dport : ether daddr").MapTo("ether daddr"),
					Flags:       []string{"dynamic"},
					Size:        65536,
					Timeout:     60 * time.Second,
				})
			}

			b := nftables.BaseChain{
				Name:      "ingress",
				ChainType: nftables.BaseChainTypeFilter,
				Hook:      nftables.BaseChainHookIngress,
				Device:    ifName,
				Priority:  nftables.BaseChainPriorityFilter,
				Policy:    nftables.BaseChainPolicyAccept,
			}.Builder().
				Rule("ip daddr != @"+dsrVipSet, "counter accept").
				Rule("notrack ether saddr set ether daddr counter")

			for _, l4proto := range l4protos {
				// Established session: reuse MAC from conntrack map.
				b = b.Rule("meta l4proto", l4proto, "ether daddr set ip saddr . th sport . ip daddr . th dport",
					"map @dsr-conntrack-"+l4proto, "counter fwd to", ifName)
			}
			b.
				// New session: random bucket lookup. The session is
				// persisted to the conntrack map in the egress chain.
				Rule("meta l4proto {", strings.Join(l4protos, ", "), "}",
					"fwd ip to ip daddr .", numgenExpr, "map @"+dsrRealServerMap, "device", ifName).
				// The service is defined but the packet does not correspond to
				// an established session and no backends are available for a new
				// session.
				Rule("counter drop").
				Create(&dsrInit)

			b = nftables.BaseChain{
				Name:      "egress",
				ChainType: nftables.BaseChainTypeFilter,
				Hook:      nftables.BaseChainHookEgress,
				Device:    ifName,
				Priority:  nftables.BaseChainPriorityFilter,
				Policy:    nftables.BaseChainPolicyAccept,
			}.Builder().
				Rule("ip daddr != @"+dsrVipSet, "counter accept").
				// We can confidently stop tracking a TCP session that has been reset.
				Rule("tcp flags rst update @dsr-conntrack-tcp",
					"{ ip saddr . th sport . ip daddr . th dport : ether daddr timeout 0s }",
					"counter accept")
			for _, l4proto := range l4protos {
				b = b.Rule("meta l4proto", l4proto, "update @dsr-conntrack-"+l4proto,
					"{ ip saddr . th sport . ip daddr . th dport : ether daddr }",
					"counter accept")
			}
			b.Create(&dsrInit)
		}

		var dsrAdd nftables.Modifier
		dsrAdd.Create(nftables.SetElement{
			SetName: dsrVipSet,
			Element: vip,
			Comment: fmt.Sprintf("%s (%s)", lb.service.name, lb.service.id),
		})

		for i, b := range backendIntervals {
			bip := b.ip.String()
			dsrAdd.Create(nftables.MapElement{
				MapName: dsrRealServerMap,
				Key:     fmt.Sprintf("%s . %s", vip, i),
				Value:   bip,
			})
		}

		var applyErr error
		if err := sb.ExecFunc(func() { applyErr = dsrTable.Apply(ctx, dsrInit, lb.nftClearDSR, dsrAdd) }); err != nil || applyErr != nil {
			log.G(ctx).Errorf("Failed to apply changes to nftables dsr table: %v", errors.Join(err, applyErr))
		} else {
			lb.nftClearDSR = dsrAdd.Reverse()
		}
	}

	return newService
}

func deleteIngressPortsNftables(ctx context.Context, filteredPorts []*PortConfig) error {
	if !nftablesIngressTable.IsValid() {
		// There are no ports to delete if the table hasn't been initialized yet.
		return nil
	}

	var tm nftables.Modifier
	for _, p := range filteredPorts {
		tm.Delete(nftables.SetElement{
			SetName: ingressPublishedPortsSet,
			Element: fmt.Sprintf("%s . %d", strings.ToLower(p.Protocol.String()), p.PublishedPort),
			Comment: p.Name,
		})
	}

	return nftablesIngressTable.Apply(ctx, tm)
}

func addIngressPortsNftables(ctx context.Context, gwIP net.IP, filteredPorts []*PortConfig) error {
	var err error
	nftablesIngressOnce.Do(func() {
		err = initIngressConfigurationNftables(context.WithoutCancel(ctx), gwIP)
	})
	if err != nil {
		return err
	}

	var tm nftables.Modifier
	for _, p := range filteredPorts {
		tm.Create(nftables.SetElement{
			SetName: ingressPublishedPortsSet,
			Element: fmt.Sprintf("%s . %d", strings.ToLower(p.Protocol.String()), p.PublishedPort),
			Comment: p.Name,
		})
	}

	return nftablesIngressTable.Apply(ctx, tm)
}

func (sb *Sandbox) nftable(family nftables.Family, name string) (nftables.Table, bool, error) {
	if sb.nftables == nil {
		sb.nftables = make(map[nftables.Family]map[string]nftables.Table)
	}
	if sb.nftables[family] == nil {
		sb.nftables[family] = make(map[string]nftables.Table)
	}
	t, ok := sb.nftables[family][name]
	if !ok {
		var err error
		t, err = nftables.NewTable(family, name)
		if err != nil {
			return nftables.Table{}, false, err
		}
		sb.nftables[family][name] = t
	}
	return t, !ok, nil
}

func (sb *Sandbox) addRedirectRulesNftables(ctx context.Context, eIP *net.IPNet, ingressPorts []*PortConfig) error {
	const (
		publishedPortsMap = "published-ports"
		ingressIPsSet     = "ingress-ips"
	)
	t, created, err := sb.nftable(nftables.IPv4, "docker-container-ingress")
	if err != nil {
		return err
	}
	var tm nftables.Modifier
	if created {
		// Map of ingress-IP . publishPort -> targetPort.
		// Packets with a destination address of ingress-IP and a
		// destination port in this map are redirected to the target
		// port.
		tm.Create(nftables.Map{
			Name:        publishedPortsMap,
			ElementType: nftables.IPv4Addr.Concat(nftables.InetProto).Concat(nftables.InetService).MapTo(nftables.InetService),
		})
		tm.Create(nftables.Set{
			Name:        ingressIPsSet,
			ElementType: nftables.IPv4Addr,
		})

		nftables.BaseChain{
			Name:      "prerouting",
			ChainType: nftables.BaseChainTypeNAT,
			Hook:      nftables.BaseChainHookPrerouting,
			Priority:  nftables.BaseChainPriorityDstNAT,
			Policy:    nftables.BaseChainPolicyAccept,
		}.Builder().
			Rule("meta l4proto { tcp, udp, sctp } redirect to ip daddr . meta l4proto . th dport map @" + publishedPortsMap).
			Create(&tm)

		nftables.BaseChain{
			Name:      "input",
			ChainType: nftables.BaseChainTypeFilter,
			Hook:      nftables.BaseChainHookInput,
			Priority:  nftables.BaseChainPriorityFilter,
			Policy:    nftables.BaseChainPolicyAccept,
		}.Builder().
			Rule("ip daddr != @"+ingressIPsSet, "counter accept").
			Rule("icmp type { destination-unreachable, time-exceeded } counter accept").
			// Only allow incoming connections to exposed ports
			Rule("ct state { new, established } counter accept").
			Rule("counter reject").
			Create(&tm)

		nftables.BaseChain{
			Name:      "output",
			ChainType: nftables.BaseChainTypeFilter,
			Hook:      nftables.BaseChainHookOutput,
			Priority:  nftables.BaseChainPriorityFilter,
			Policy:    nftables.BaseChainPolicyAccept,
		}.Builder().
			Rule("ip saddr != @"+ingressIPsSet, "counter accept").
			Rule("icmp type { destination-unreachable, time-exceeded } counter accept").
			// Only allow outgoing connections from exposed ports
			Rule("ct state established counter accept").
			Rule("counter reject").
			Create(&tm)
	}

	ingressIP := eIP.IP.String()
	tm.Create(nftables.SetElement{
		SetName: ingressIPsSet,
		Element: ingressIP,
	})
	for _, p := range ingressPorts {
		tm.Create(nftables.MapElement{
			MapName: publishedPortsMap,
			Key:     fmt.Sprintf("%s . %s . %d", ingressIP, strings.ToLower(p.Protocol.String()), p.PublishedPort),
			Value:   fmt.Sprintf("%d", p.TargetPort),
			Comment: p.Name,
		})
	}

	var applyErr error
	err = sb.ExecFunc(func() { applyErr = t.Apply(ctx, tm) })
	return errors.Join(err, applyErr)
}
