package linuxsandbox

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDatagramPacketsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, d := range []datagram{
		{src: ap("192.0.0.8:40000"), dst: ap("9.9.9.9:53"), payload: []byte("query")},
		{src: ap("[fd70:7700:636f:6465::8]:40000"), dst: ap("[2001:db8::53]:53"), payload: []byte("odd")},
		{src: ap("9.9.9.9:53"), dst: ap("192.0.0.8:40000"), payload: nil},
	} {
		packet := buildDatagram(d)
		got, ok := parseDatagram(packet)
		if !ok || got.src != d.src || got.dst != d.dst || !bytes.Equal(got.payload, d.payload) {
			t.Fatalf("%v → %v: parsed %+v, %v", d.src, d.dst, got, ok)
		}
		// A packet with valid checksums sums to zero over each checksummed span.
		if d.src.Addr().Is4() && checksum(packet[:ipv4HeaderLen], 0) != 0 {
			t.Fatalf("IPv4 header checksum does not verify")
		}
		udp := packet[len(packet)-udpHeaderLen-len(d.payload):]
		if checksum(udp, pseudoHeaderSum(d.src.Addr(), d.dst.Addr(), protocolUDP, len(udp))) != 0 {
			t.Fatalf("%v: UDP checksum does not verify", d.src)
		}
	}
}

// The report comes from the closed port's host, quotes the datagram's headers,
// and carries valid checksums.
func TestPortUnreachableQuotesTheDatagram(t *testing.T) {
	t.Parallel()
	for _, d := range []datagram{
		{src: ap("192.0.0.8:40000"), dst: ap("9.9.9.9:53"), payload: []byte("query")},
		{src: ap("[fd70:7700:636f:6465::8]:40000"), dst: ap("[2001:db8::53]:53"), payload: []byte("query")},
	} {
		packet := buildPortUnreachable(d)
		var icmp []byte
		if d.src.Addr().Is4() {
			if packet[9] != protocolICMP || checksum(packet[:ipv4HeaderLen], 0) != 0 {
				t.Fatalf("%v: bad IPv4 header", d.src)
			}
			if netip.AddrFrom4([4]byte(packet[12:16])) != d.dst.Addr() || netip.AddrFrom4([4]byte(packet[16:20])) != d.src.Addr() {
				t.Fatalf("%v: report not from the destination host to the sender", d.src)
			}
			icmp = packet[ipv4HeaderLen:]
			if icmp[0] != 3 || icmp[1] != 3 || checksum(icmp, 0) != 0 {
				t.Fatalf("%v: bad ICMP message % x", d.src, icmp[:4])
			}
		} else {
			if packet[6] != protocolICMP6 {
				t.Fatalf("%v: bad IPv6 header", d.src)
			}
			icmp = packet[ipv6HeaderLen:]
			if icmp[0] != 1 || icmp[1] != 4 || checksum(icmp, pseudoHeaderSum(d.dst.Addr(), d.src.Addr(), protocolICMP6, len(icmp))) != 0 {
				t.Fatalf("%v: bad ICMPv6 message % x", d.src, icmp[:4])
			}
		}
		quoted, ok := parseDatagram(icmp[icmpHeaderLen:])
		if !ok || quoted.src != d.src || quoted.dst != d.dst {
			t.Fatalf("%v: quoted %+v, %v", d.src, quoted, ok)
		}
	}
}

func TestParseDatagramRejectsWhatTheGatewayDoesNotCarry(t *testing.T) {
	t.Parallel()
	v4 := buildDatagram(datagram{src: ap("192.0.0.8:1"), dst: ap("9.9.9.9:53"), payload: []byte("x")})
	tcp := append([]byte(nil), v4...)
	tcp[9] = 6
	fragment := append([]byte(nil), v4...)
	binary.BigEndian.PutUint16(fragment[6:8], 0x2000)
	short := append([]byte(nil), v4...)
	binary.BigEndian.PutUint16(short[ipv4HeaderLen+4:], 4)
	v6 := buildDatagram(datagram{src: ap("[fd70::8]:1"), dst: ap("[2001:db8::1]:53"), payload: []byte("x")})
	extension := append([]byte(nil), v6...)
	extension[6] = 0
	for name, packet := range map[string][]byte{
		"tcp": tcp, "fragment": fragment, "short udp length": short, "ipv6 extension header": extension,
		"empty": nil, "truncated": v4[:10], "version 5": {0x50},
	} {
		if _, ok := parseDatagram(packet); ok {
			t.Errorf("%s: parsed", name)
		}
	}
}

func TestBoundDatagramSocketsReadTheKernelTables(t *testing.T) {
	t.Parallel()
	udp := `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  1: 3500007F:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   991        0 1 2 0000000000000000 0
  2: 00000000:14E9 00000000:0000 07 00000000:00000000 00:00000000 00000000   991        0 1 2 0000000000000000 0
  3: 0100007F:9C40 0100007F:1F90 01 00000000:00000000 00:00000000 00000000   991        0 1 2 0000000000000000 0
`
	udp6 := `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
  4: 00000000000000000000000001000000:1FBD 00000000000000000000000000000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 1 2 0000000000000000 0
`
	got := append(boundDatagramSockets(udp), boundDatagramSockets(udp6)...)
	want := []netip.AddrPort{ap("127.0.0.53:53"), ap("0.0.0.0:5353"), ap("[::1]:8125")}
	if len(got) != len(want) {
		t.Fatalf("bound = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("bound = %v, want %v", got, want)
		}
	}
	services := loopbackServiceAddrs(append(got, ap("0.0.0.0:50000"), ap("192.0.2.1:9")), 32768)
	wantServices := []netip.AddrPort{ap("127.0.0.53:53"), ap("127.0.0.1:5353"), ap("[::1]:8125")}
	if len(services) != len(wantServices) {
		t.Fatalf("services = %v, want %v", services, wantServices)
	}
	for i := range wantServices {
		if services[i] != wantServices[i] {
			t.Fatalf("services = %v, want %v", services, wantServices)
		}
	}
	for dst, served := range map[string]bool{
		"127.0.0.53:53": true, "127.0.0.1:53": false, "127.0.0.2:5353": true, "[::1]:5353": false, "[::1]:8125": true, "127.0.0.1:8125": false,
	} {
		if hostServes(got, ap(dst)) != served {
			t.Errorf("host serves %s: want %v", dst, served)
		}
	}
	if ephemeralFloor("32768\t60999\n") != 32768 || ephemeralFloor("1024 65535") != 1024 || ephemeralFloor("") != 32768 {
		t.Fatal("ephemeral floor misread")
	}
}

// Each sender gets a flow of its own, and a flow's replies go back to the
// sender that opened it.
func TestRelayDatagramsKeepsEachSendersFlow(t *testing.T) {
	t.Parallel()
	echo, err := net.ListenPacket("udp", "127.0.0.1:0")
	testutil.FailErr(t, "net.ListenPacket failed", err)
	defer echo.Close()
	go func() {
		buf := make([]byte, 64)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = echo.WriteTo(append([]byte(from.String()+" "), buf[:n]...), from)
		}
	}()
	relay, err := net.ListenPacket("udp", "127.0.0.1:0")
	testutil.FailErr(t, "net.ListenPacket failed", err)
	defer relay.Close()
	go relayDatagrams(relay, func() (net.Conn, error) { return net.Dial("udp", echo.LocalAddr().String()) })
	upstream := map[string]bool{}
	for i := 0; i < 2; i++ {
		pingThroughRelay(t, i, relay.LocalAddr().String(), upstream)
	}
	if len(upstream) != 2 {
		t.Fatalf("two senders reached the service from %d flows: %v", len(upstream), upstream)
	}
}

// pingThroughRelay sends twice from one client and records the address the
// service saw it from.
func pingThroughRelay(t *testing.T, i int, relay string, upstream map[string]bool) {
	t.Helper()
	c, err := net.Dial("udp", relay)
	testutil.FailErr(t, "net.Dial failed", err)
	defer c.Close()
	for j := 0; j < 2; j++ {
		if _, err := c.Write([]byte("ping")); err != nil {
			testutil.FailErr(t, "c.Write failed", err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 64)
		n, err := c.Read(buf)
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		from, body, _ := bytes.Cut(buf[:n], []byte(" "))
		if string(body) != "ping" {
			t.Fatalf("client %d got %q", i, buf[:n])
		}
		upstream[string(from)] = true
	}
}

func TestDatagramFlowsStopAtTheirBound(t *testing.T) {
	t.Parallel()
	flows := newDatagramFlows()
	dials := 0
	dial := func() (net.Conn, error) {
		dials++
		a, b := net.Pipe()
		go func() { _, _ = io.Copy(io.Discard, b) }()
		return a, nil
	}
	for i := 0; i < maxDatagramFlows+5; i++ {
		flows.send(i, []byte("x"), dial, flowPeer{deliver: func([]byte) {}})
	}
	flows.send(0, []byte("x"), dial, flowPeer{deliver: func([]byte) {}})
	if dials != maxDatagramFlows {
		t.Fatalf("opened %d flows, want %d", dials, maxDatagramFlows)
	}
	_ = flows.Close()
	flows.send(maxDatagramFlows+10, []byte("x"), dial, flowPeer{deliver: func([]byte) {}})
	if dials != maxDatagramFlows {
		t.Fatal("a closed table opened a flow")
	}
}
