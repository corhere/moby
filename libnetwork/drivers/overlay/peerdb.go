// FIXME(thaJeztah): remove once we are a module; the go:build directive prevents go from downgrading language version to go1.16:
//go:build go1.19 && linux

package overlay

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"syscall"

	"github.com/containerd/log"
	"github.com/docker/docker/libnetwork/internal/setmatrix"
	"github.com/docker/docker/libnetwork/osl"
)

const ovPeerTable = "overlay_peer_table"

type peerKey struct {
	peerIP  netip.Addr
	peerMac net.HardwareAddr
}

type peerEntry struct {
	eid        string
	vtep       netip.Addr
	prefixBits int // number of 1-bits in network mask of peerIP
	isLocal    bool
}

type peerMap struct {
	// set of peerEntry, note the values have to be objects and not pointers to maintain the proper equality checks
	mp setmatrix.SetMatrix[peerEntry]
	sync.Mutex
}

type peerNetworkMap struct {
	// map with key peerKey
	mp map[string]*peerMap
	sync.Mutex
}

func (pKey peerKey) String() string {
	return fmt.Sprintf("%s %s", pKey.peerIP, pKey.peerMac)
}

func (pKey *peerKey) Scan(state fmt.ScanState, verb rune) error {
	ipB, err := state.Token(true, nil)
	if err != nil {
		return err
	}

	pKey.peerIP, err = netip.ParseAddr(string(ipB))
	if err != nil {
		return err
	}

	macB, err := state.Token(true, nil)
	if err != nil {
		return err
	}

	pKey.peerMac, err = net.ParseMAC(string(macB))
	return err
}

func (d *driver) peerDbWalk(f func(string, *peerKey, *peerEntry) bool) error {
	d.peerDb.Lock()
	nids := []string{}
	for nid := range d.peerDb.mp {
		nids = append(nids, nid)
	}
	d.peerDb.Unlock()

	for _, nid := range nids {
		d.peerDbNetworkWalk(nid, func(pKey *peerKey, pEntry *peerEntry) bool {
			return f(nid, pKey, pEntry)
		})
	}
	return nil
}

func (d *driver) peerDbNetworkWalk(nid string, f func(*peerKey, *peerEntry) bool) error {
	d.peerDb.Lock()
	pMap, ok := d.peerDb.mp[nid]
	d.peerDb.Unlock()

	if !ok {
		return nil
	}

	mp := map[string]peerEntry{}
	pMap.Lock()
	for _, pKeyStr := range pMap.mp.Keys() {
		entryDBList, ok := pMap.mp.Get(pKeyStr)
		if ok {
			peerEntry := entryDBList[0]
			mp[pKeyStr] = peerEntry
		}
	}
	pMap.Unlock()

	for pKeyStr, pEntry := range mp {
		var pKey peerKey
		pEntry := pEntry
		if _, err := fmt.Sscan(pKeyStr, &pKey); err != nil {
			log.G(context.TODO()).Warnf("Peer key scan on network %s failed: %v", nid, err)
		}
		if f(&pKey, &pEntry) {
			return nil
		}
	}

	return nil
}

func (d *driver) peerDbSearch(nid string, peerIP netip.Addr) (*peerKey, *peerEntry, error) {
	var pKeyMatched *peerKey
	var pEntryMatched *peerEntry
	err := d.peerDbNetworkWalk(nid, func(pKey *peerKey, pEntry *peerEntry) bool {
		if pKey.peerIP == peerIP {
			pKeyMatched = pKey
			pEntryMatched = pEntry
			return true
		}

		return false
	})
	if err != nil {
		return nil, nil, fmt.Errorf("peerdb search for peer ip %q failed: %v", peerIP, err)
	}

	if pKeyMatched == nil || pEntryMatched == nil {
		return nil, nil, fmt.Errorf("peer ip %q not found in peerdb", peerIP)
	}

	return pKeyMatched, pEntryMatched, nil
}

func (d *driver) peerDbAdd(nid, eid string, peerIP netip.Prefix, peerMac net.HardwareAddr, vtep netip.Addr, isLocal bool) (bool, int) {
	d.peerDb.Lock()
	pMap, ok := d.peerDb.mp[nid]
	if !ok {
		pMap = &peerMap{}
		d.peerDb.mp[nid] = pMap
	}
	d.peerDb.Unlock()

	pKey := peerKey{
		peerIP:  peerIP.Addr(),
		peerMac: peerMac,
	}

	pEntry := peerEntry{
		eid:        eid,
		vtep:       vtep,
		prefixBits: peerIP.Bits(),
		isLocal:    isLocal,
	}

	pMap.Lock()
	defer pMap.Unlock()
	b, i := pMap.mp.Insert(pKey.String(), pEntry)
	if i != 1 {
		// Transient case, there is more than one endpoint that is using the same IP,MAC pair
		s, _ := pMap.mp.String(pKey.String())
		log.G(context.TODO()).Warnf("peerDbAdd transient condition - Key:%s cardinality:%d db state:%s", pKey.String(), i, s)
	}
	return b, i
}

func (d *driver) peerDbDelete(nid, eid string, peerIP netip.Prefix, peerMac net.HardwareAddr, vtep netip.Addr, isLocal bool) (bool, int) {
	d.peerDb.Lock()
	pMap, ok := d.peerDb.mp[nid]
	if !ok {
		d.peerDb.Unlock()
		return false, 0
	}
	d.peerDb.Unlock()

	pKey := peerKey{
		peerIP:  peerIP.Addr(),
		peerMac: peerMac,
	}

	pEntry := peerEntry{
		eid:        eid,
		vtep:       vtep,
		prefixBits: peerIP.Bits(),
		isLocal:    isLocal,
	}

	pMap.Lock()
	defer pMap.Unlock()
	b, i := pMap.mp.Remove(pKey.String(), pEntry)
	if i != 0 {
		// Transient case, there is more than one endpoint that is using the same IP,MAC pair
		s, _ := pMap.mp.String(pKey.String())
		log.G(context.TODO()).Warnf("peerDbDelete transient condition - Key:%s cardinality:%d db state:%s", pKey.String(), i, s)
	}
	return b, i
}

// The overlay uses a lazy initialization approach, this means that when a network is created
// and the driver registered the overlay does not allocate resources till the moment that a
// sandbox is actually created.
// At the moment of this call, that happens when a sandbox is initialized, is possible that
// networkDB has already delivered some events of peers already available on remote nodes,
// these peers are saved into the peerDB and this function is used to properly configure
// the network sandbox with all those peers that got previously notified.
// Note also that this method sends a single message on the channel and the go routine on the
// other side, will atomically loop on the whole table of peers and will program their state
// in one single atomic operation. This is fundamental to guarantee consistency, and avoid that
// new peerAdd or peerDelete gets reordered during the sandbox init.
func (d *driver) initSandboxPeerDB(nid string) {
	d.peerOpMu.Lock()
	defer d.peerOpMu.Unlock()
	if err := d.peerInitOp(nid); err != nil {
		log.G(context.TODO()).WithError(err).Warn("Peer init operation failed")
	}
}

func (d *driver) peerInitOp(nid string) error {
	return d.peerDbNetworkWalk(nid, func(pKey *peerKey, pEntry *peerEntry) bool {
		// Local entries do not need to be added
		if pEntry.isLocal {
			return false
		}

		d.peerAddOp(nid, pEntry.eid, netip.PrefixFrom(pKey.peerIP, pEntry.prefixBits), pKey.peerMac, pEntry.vtep, false, pEntry.isLocal)
		// return false to loop on all entries
		return false
	})
}

func (d *driver) peerAdd(nid, eid string, peerIP netip.Prefix, peerMac net.HardwareAddr, vtep netip.Addr, localPeer bool) {
	d.peerOpMu.Lock()
	defer d.peerOpMu.Unlock()
	err := d.peerAddOp(nid, eid, peerIP, peerMac, vtep, true, localPeer)
	if err != nil {
		log.G(context.TODO()).WithError(err).Warn("Peer add operation failed")
	}
}

func (d *driver) peerAddOp(nid, eid string, peerIP netip.Prefix, peerMac net.HardwareAddr, vtep netip.Addr, updateDB, localPeer bool) error {
	if err := validateID(nid, eid); err != nil {
		return err
	}

	var inserted bool
	if updateDB {
		inserted, _ = d.peerDbAdd(nid, eid, peerIP, peerMac, vtep, localPeer)
		if !inserted {
			log.G(context.TODO()).Warnf("Entry already present in db: nid:%s eid:%s peerIP:%v peerMac:%v isLocal:%t vtep:%v",
				nid, eid, peerIP, peerMac, localPeer, vtep)
		}
	}

	// Local peers do not need any further configuration
	if localPeer {
		return nil
	}

	n := d.network(nid)
	if n == nil {
		return nil
	}

	sbox := n.sandbox()
	if sbox == nil {
		// We are hitting this case for all the events that are arriving before that the sandbox
		// is being created. The peer got already added into the database and the sanbox init will
		// call the peerDbUpdateSandbox that will configure all these peers from the database
		return nil
	}

	s := n.getSubnetforIP(peerIP)
	if s == nil {
		return fmt.Errorf("couldn't find the subnet %q in network %q", peerIP.String(), n.id)
	}

	if err := n.joinSandbox(s, false); err != nil {
		return fmt.Errorf("subnet sandbox join failed for %q: %v", s.subnetIP.String(), err)
	}

	if err := d.checkEncryption(nid, vtep, false, true); err != nil {
		log.G(context.TODO()).Warn(err)
	}

	// Add neighbor entry for the peer IP
	if err := sbox.AddNeighbor(peerIP.Addr(), peerMac, osl.WithLinkName(s.vxlanName)); err != nil {
		return fmt.Errorf("could not add neighbor entry for nid:%s eid:%s into the sandbox:%v", nid, eid, err)
	}

	// Add fdb entry to the bridge for the peer mac
	if err := sbox.AddNeighbor(vtep, peerMac, osl.WithLinkName(s.vxlanName), osl.WithFamily(syscall.AF_BRIDGE)); err != nil {
		return fmt.Errorf("could not add fdb entry for nid:%s eid:%s into the sandbox:%v", nid, eid, err)
	}

	// The 'proxy' feature of the Linux VXLAN network driver
	// is somewhat braindead.
	// It drops all ARP/ND packets transmitted to the vxlan interface
	// instead of encapsulating them.
	// If the dropped packet is a request
	// and a neighbor table entry for the request's target IP
	// is present on the vxlan interface,
	// the driver will generate a proxy ARP/ND reply locally.
	// Consequently, any ARP/ND announcements broadcast
	// from a container endpoint's link
	// will only reach the container's local peers.
	// We have to proxy the announcements from userspace
	// in order to update stale ARP/ND cache entries in remote peers.
	// The VXLAN interface will receive the announcement
	// and generate a unicast reply.
	// This reply is not a complete waste of cycles:
	// it teaches the bridge that segments addressed to the remote peer's MAC
	// should be forwarded to the VXLAN interface's port.
	if err := s.garp.Announce(peerIP.Addr(), peerMac); err != nil {
		// Best-effort. The peers will figure out that their neighbor
		// table entries are stale and recover within a few seconds.
		log.G(context.TODO()).Warnf("could not announce remote neighbor %s to local peers on nid:%s: %v", peerIP, nid, err)
	}

	return nil
}

func (d *driver) peerDelete(nid, eid string, peerIP netip.Prefix, peerMac net.HardwareAddr, vtep netip.Addr, localPeer bool) {
	d.peerOpMu.Lock()
	defer d.peerOpMu.Unlock()
	err := d.peerDeleteOp(nid, eid, peerIP, peerMac, vtep, localPeer)
	if err != nil {
		log.G(context.TODO()).WithError(err).Warn("Peer delete operation failed")
	}
}

func (d *driver) peerDeleteOp(nid, eid string, peerIP netip.Prefix, peerMac net.HardwareAddr, vtep netip.Addr, localPeer bool) error {
	if err := validateID(nid, eid); err != nil {
		return err
	}

	deleted, dbEntries := d.peerDbDelete(nid, eid, peerIP, peerMac, vtep, localPeer)
	if !deleted {
		log.G(context.TODO()).Warnf("Entry was not in db: nid:%s eid:%s peerIP:%v peerMac:%v isLocal:%t vtep:%v",
			nid, eid, peerIP, peerMac, localPeer, vtep)
	}

	n := d.network(nid)
	if n == nil {
		return nil
	}

	sbox := n.sandbox()
	if sbox == nil {
		return nil
	}

	if err := d.checkEncryption(nid, vtep, localPeer, false); err != nil {
		log.G(context.TODO()).Warn(err)
	}

	// Local peers do not have any local configuration to delete
	if !localPeer {
		s := n.getSubnetforIP(peerIP)
		if s == nil {
			return fmt.Errorf("couldn't find the subnet %q in network %q", peerIP.String(), n.id)
		}
		// Remove fdb entry to the bridge for the peer mac
		if err := sbox.DeleteNeighbor(vtep, peerMac, osl.WithLinkName(s.vxlanName), osl.WithFamily(syscall.AF_BRIDGE)); err != nil {
			return fmt.Errorf("could not delete fdb entry for nid:%s eid:%s into the sandbox:%v", nid, eid, err)
		}

		// Delete neighbor entry for the peer IP
		if err := sbox.DeleteNeighbor(peerIP.Addr(), peerMac, osl.WithLinkName(s.vxlanName)); err != nil {
			return fmt.Errorf("could not delete neighbor entry for nid:%s eid:%s into the sandbox:%v", nid, eid, err)
		}
	}

	if dbEntries == 0 {
		return nil
	}

	// If there is still an entry into the database and the deletion went through without errors means that there is now no
	// configuration active in the kernel.
	// Restore one configuration for the <ip,mac> directly from the database, note that is guaranteed that there is one
	peerKey, peerEntry, err := d.peerDbSearch(nid, peerIP.Addr())
	if err != nil {
		log.G(context.TODO()).Errorf("peerDeleteOp unable to restore a configuration for nid:%s ip:%v mac:%v err:%s", nid, peerIP, peerMac, err)
		return err
	}
	return d.peerAddOp(nid, peerEntry.eid, netip.PrefixFrom(peerKey.peerIP, peerEntry.prefixBits), peerKey.peerMac, peerEntry.vtep, false, peerEntry.isLocal)
}

func (d *driver) peerFlush(nid string) {
	d.peerOpMu.Lock()
	defer d.peerOpMu.Unlock()
	if err := d.peerFlushOp(nid); err != nil {
		log.G(context.TODO()).WithError(err).Warn("Peer flush operation failed")
	}
}

func (d *driver) peerFlushOp(nid string) error {
	d.peerDb.Lock()
	defer d.peerDb.Unlock()
	_, ok := d.peerDb.mp[nid]
	if !ok {
		return fmt.Errorf("Unable to find the peerDB for nid:%s", nid)
	}
	delete(d.peerDb.mp, nid)
	return nil
}

func (d *driver) peerDBUpdateSelf() {
	d.peerDbWalk(func(nid string, pkey *peerKey, pEntry *peerEntry) bool {
		if pEntry.isLocal {
			pEntry.vtep = d.advertiseAddress
		}
		return false
	})
}
