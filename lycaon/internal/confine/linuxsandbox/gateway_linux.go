//go:build linux

package linuxsandbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
)

// The gateway is a TUN interface inside the sandbox's network namespace that
// carries every route off the sandbox. The outer helper holds its other end:
// it forwards each UDP datagram a permit covers from a socket of its own on
// the host, writes the replies back as packets, and drops everything else.

const gatewayInterface = "pw0"

// gatewayMTU lets any datagram cross whole, so none arrives fragmented.
const gatewayMTU = 65535

// The sandbox's own addresses on the gateway. 192.0.0.8 is the IPv4 dummy
// address (RFC 7600), the source a host uses when it has no IPv4 of its own;
// the IPv6 address is unique-local.
var (
	gatewayAddr4 = netip.MustParseAddr("192.0.0.8")
	gatewayAddr6 = netip.MustParseAddr("fd70:7700:636f:6465::8")
)

const handoffGateway = "gateway"

// handOffGateway creates the gateway interface, routes everything off the
// sandbox through it, and passes its descriptor to the outer helper.
func handOffGateway() error {
	tun, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return gatewayUnavailable("open /dev/net/tun", err)
	}
	defer func() { _ = unix.Close(tun) }()
	ifr, err := unix.NewIfreq(gatewayInterface)
	if err != nil {
		return err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(tun, unix.TUNSETIFF, ifr); err != nil {
		return gatewayUnavailable("create "+gatewayInterface, err)
	}
	index, err := configureGatewayLink()
	if err != nil {
		return gatewayUnavailable("configure "+gatewayInterface, err)
	}
	if err := addGatewayRoute(index, gatewayAddr4, 32); err != nil {
		return gatewayUnavailable("route IPv4 through "+gatewayInterface, err)
	}
	// A kernel without IPv6 leaves the gateway to IPv4.
	if err := addGatewayRoute(index, gatewayAddr6, 128); err != nil && !errors.Is(err, unix.EAFNOSUPPORT) && !errors.Is(err, unix.EOPNOTSUPP) {
		return gatewayUnavailable("route IPv6 through "+gatewayInterface, err)
	}
	if err := sendHandoff(handoff{Kind: handoffGateway}, tun); err != nil {
		return fmt.Errorf("hand off gateway: %w", err)
	}
	return nil
}

func gatewayUnavailable(step string, err error) *Unavailability {
	return &Unavailability{Code: UnavailableDatagramGateway, Detail: step + ": " + err.Error()}
}

// configureGatewayLink sets the gateway's MTU, brings it up, and returns its
// interface index.
func configureGatewayLink() (int, error) {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return 0, err
	}
	defer func() { _ = unix.Close(fd) }()
	ifr, err := unix.NewIfreq(gatewayInterface)
	if err != nil {
		return 0, err
	}
	ifr.SetUint32(gatewayMTU)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFMTU, ifr); err != nil {
		return 0, err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, ifr); err != nil {
		return 0, err
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, ifr); err != nil {
		return 0, err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFINDEX, ifr); err != nil {
		return 0, err
	}
	return int(ifr.Uint32()), nil
}

// addGatewayRoute gives the gateway the sandbox's address of addr's family
// and makes it that family's default route.
func addGatewayRoute(index int, addr netip.Addr, prefix uint8) error {
	family := uint8(unix.AF_INET)
	if addr.Is6() {
		family = unix.AF_INET6
	}
	ifa := make([]byte, unix.SizeofIfAddrmsg)
	ifa[0], ifa[1] = family, prefix
	// No duplicate detection: nothing else is on the link.
	ifa[2] = unix.IFA_F_NODAD
	binary.NativeEndian.PutUint32(ifa[4:], uint32(index))
	raw := addr.AsSlice()
	ifa = append(ifa, netlinkAttr(unix.IFA_LOCAL, raw)...)
	ifa = append(ifa, netlinkAttr(unix.IFA_ADDRESS, raw)...)
	if err := netlinkRequest(unix.RTM_NEWADDR, ifa); err != nil {
		return err
	}
	rt := make([]byte, unix.SizeofRtMsg)
	rt[0] = family
	rt[4] = unix.RT_TABLE_MAIN
	rt[5] = unix.RTPROT_BOOT
	rt[6] = unix.RT_SCOPE_LINK
	rt[7] = unix.RTN_UNICAST
	oif := make([]byte, 4)
	binary.NativeEndian.PutUint32(oif, uint32(index))
	rt = append(rt, netlinkAttr(unix.RTA_OIF, oif)...)
	return netlinkRequest(unix.RTM_NEWROUTE, rt)
}

func netlinkAttr(typ uint16, data []byte) []byte {
	length := unix.SizeofRtAttr + len(data)
	b := make([]byte, (length+unix.NLMSG_ALIGNTO-1) & ^(unix.NLMSG_ALIGNTO-1))
	binary.NativeEndian.PutUint16(b[0:], uint16(length))
	binary.NativeEndian.PutUint16(b[2:], typ)
	copy(b[unix.SizeofRtAttr:], data)
	return b
}

// netlinkRequest sends one routing request that creates something and waits
// for the kernel's acknowledgement.
func netlinkRequest(typ uint16, body []byte) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	msg := make([]byte, unix.SizeofNlMsghdr, unix.SizeofNlMsghdr+len(body))
	binary.NativeEndian.PutUint32(msg[0:], uint32(unix.SizeofNlMsghdr+len(body)))
	binary.NativeEndian.PutUint16(msg[4:], typ)
	binary.NativeEndian.PutUint16(msg[6:], unix.NLM_F_REQUEST|unix.NLM_F_ACK|unix.NLM_F_CREATE|unix.NLM_F_EXCL)
	binary.NativeEndian.PutUint32(msg[8:], 1)
	msg = append(msg, body...)
	if err := unix.Sendto(fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	reply := make([]byte, 4096)
	n, _, err := unix.Recvfrom(fd, reply, 0)
	if err != nil {
		return err
	}
	if n < unix.SizeofNlMsghdr+4 || binary.NativeEndian.Uint16(reply[4:]) != unix.NLMSG_ERROR {
		return errors.New("netlink: unexpected reply")
	}
	if code := int32(binary.NativeEndian.Uint32(reply[unix.SizeofNlMsghdr:])); code != 0 {
		return unix.Errno(-code)
	}
	return nil
}

// serveGateway forwards the datagrams the network's permits cover until the
// gateway closes. A datagram no permit covers is dropped, so a connected
// socket the policy refused at connect and an unconnected send alike reach
// nothing.
func serveGateway(tun *os.File, network Network) {
	flows := newDatagramFlows()
	defer func() { _ = flows.Close() }()
	buf := make([]byte, maxDatagram+ipv6HeaderLen)
	for {
		n, err := tun.Read(buf)
		if err != nil {
			return
		}
		d, ok := parseDatagram(buf[:n])
		if !ok || !gatewayCarries(network, d.dst) {
			continue
		}
		src, dst := d.src, d.dst
		dial := func() (net.Conn, error) { return net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(dst)) }
		deliver := func(reply []byte) {
			_, _ = tun.Write(buildDatagram(datagram{src: dst, dst: src, payload: reply}))
		}
		flows.send([2]netip.AddrPort{src, dst}, d.payload, dial, deliver)
	}
}

// gatewayCarries reports whether a datagram to dst may leave the sandbox.
func gatewayCarries(n Network, dst netip.AddrPort) bool {
	a := dst.Addr()
	if !a.IsValid() || a.IsLoopback() || a.IsUnspecified() || a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return false
	}
	return n.directAllowed(false, dst.Port())
}
