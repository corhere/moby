package osl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/containerd/log"
	"github.com/vishvananda/netlink"
)

type neigh struct {
	linkName string
	family   int
}

// DeleteNeighbor deletes neighbor entry from the sandbox.
//
// The options must exactly match the options passed into AddNeighbor.
func (n *Namespace) DeleteNeighbor(dstIP net.IP, dstMac net.HardwareAddr, options ...NeighOption) error {
	n.mu.Lock()
	nlh := n.nlHandle
	n.mu.Unlock()

	nlnh, _, err := n.nlneigh(nlh, options...)
	if err != nil {
		return err
	}
	nlnh.IP = dstIP
	nlnh.State = netlink.NUD_PERMANENT
	if nlnh.Family > 0 {
		nlnh.HardwareAddr = dstMac
		nlnh.Flags = netlink.NTF_SELF
	}

	// If the kernel deletion fails for the neighbor entry still remove it
	// from the namespace cache, otherwise kernel update can fail if the
	// neighbor moves back to the same host again.
	if err := nlh.NeighDel(nlnh); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.G(context.TODO()).Warnf("Deleting neighbor IP %s, mac %s failed, %v", dstIP, dstMac, err)
	}

	// Delete the dynamic entry in the bridge
	if nlnh.Family > 0 {
		if err := nlh.NeighDel(&netlink.Neigh{
			LinkIndex:    nlnh.LinkIndex,
			IP:           dstIP,
			Family:       nlnh.Family,
			HardwareAddr: dstMac,
			Flags:        netlink.NTF_MASTER,
		}); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.G(context.TODO()).WithError(err).Warn("error while deleting neighbor entry")
		}
	}

	log.G(context.TODO()).Debugf("Neighbor entry deleted for IP %v, mac %v", dstIP, dstMac)

	return nil
}

// AddNeighbor adds a neighbor entry into the sandbox.
func (n *Namespace) AddNeighbor(dstIP net.IP, dstMac net.HardwareAddr, options ...NeighOption) error {
	n.mu.Lock()
	nlh := n.nlHandle
	n.mu.Unlock()

	nlnh, linkName, err := n.nlneigh(nlh, options...)
	if err != nil {
		return err
	}
	nlnh.IP = dstIP
	nlnh.HardwareAddr = dstMac
	nlnh.State = netlink.NUD_PERMANENT
	if nlnh.Family > 0 {
		nlnh.Flags = netlink.NTF_SELF
	}

	if err := nlh.NeighSet(nlnh); err != nil {
		return fmt.Errorf("could not add neighbor entry:%+v error:%v", nlnh, err)
	}
	log.G(context.TODO()).Debugf("Neighbor entry added for IP:%v, mac:%v on ifc:%s", dstIP, dstMac, linkName)

	return nil
}

func (n *Namespace) nlneigh(nlh *netlink.Handle, options ...NeighOption) (nlnh *netlink.Neigh, linkName string, err error) {
	nh := &neigh{}
	nh.processNeighOptions(options...)

	nlnh = &netlink.Neigh{Family: nh.family}

	if nh.linkName != "" {
		linkDst := n.findDst(nh.linkName, false)
		if linkDst == "" {
			return nil, "", fmt.Errorf("could not find the interface with name %s", nh.linkName)
		}

		iface, err := nlh.LinkByName(linkDst)
		if err != nil {
			return nil, "", fmt.Errorf("could not find interface with destination name %s: %v", linkDst, err)
		}
		nlnh.LinkIndex = iface.Attrs().Index
	}

	return nlnh, nh.linkName, nil
}
