//go:build linux

package confine_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

// datagramSends are the ways the datagram probe sends: on a connected
// socket, with sendto on an unconnected one, and with sendmsg naming the
// destination.
var datagramSends = []string{"connected", "sendto", "sendmsg"}

// datagramProbe sends one datagram to each target, one per line, every way
// datagramSends names, and prints the reply or the error.
func datagramProbe(mode string) int {
	targets := strings.Split(os.Getenv(linuxProbePathsEnv), "\n")
	if mode == "udpserve" {
		return datagramServe(targets[0])
	}
	for _, target := range targets {
		dst, err := netip.ParseAddrPort(target)
		if err != nil {
			fmt.Printf("udp %s: %v\n", target, err)
			continue
		}
		for _, how := range datagramSends {
			fmt.Printf("udp %s %s: %s\n", how, target, sendDatagram(how, dst))
		}
	}
	return 0
}

func sendDatagram(how string, dst netip.AddrPort) string {
	to := net.UDPAddrFromAddrPort(dst)
	var conn *net.UDPConn
	var err error
	if how == "connected" {
		conn, err = net.DialUDP("udp", nil, to)
	} else {
		conn, err = net.ListenUDP("udp", nil)
	}
	if err != nil {
		return errnoName(err)
	}
	defer conn.Close()
	switch how {
	case "connected":
		_, err = conn.Write([]byte("ping"))
	case "sendto":
		_, err = conn.WriteToUDP([]byte("ping"), to)
	default:
		_, _, err = conn.WriteMsgUDP([]byte("ping"), nil, to)
	}
	if err != nil {
		return errnoName(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	n, _, err := conn.ReadFromUDP(buf)
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "no reply"
	case err != nil:
		return errnoName(err)
	}
	return "ok " + string(buf[:n])
}

// datagramServe answers one datagram on addr with "served".
func datagramServe(addr string) int {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		fmt.Println("listen:", errnoName(err))
		return 2
	}
	defer conn.Close()
	fmt.Println("listening", conn.LocalAddr())
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	buf := make([]byte, 64)
	_, from, err := conn.ReadFrom(buf)
	if err != nil {
		fmt.Println("read:", err)
		return 3
	}
	_, _ = conn.WriteTo([]byte("served"), from)
	return 0
}

// datagramEcho answers every datagram on addr with "host" until the test ends.
func datagramEcho(t *testing.T, addr string) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenPacket("udp", addr)
	testutil.FailErr(t, "listen "+addr, err)
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 64)
		for {
			_, from, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = conn.WriteTo([]byte("host"), from)
		}
	}()
	return conn.LocalAddr().(*net.UDPAddr).AddrPort()
}

// serviceEcho serves datagramEcho on a loopback port below the ephemeral
// range, where the host's datagram services live.
func serviceEcho(t *testing.T) netip.AddrPort {
	t.Helper()
	for port := 20000 + os.Getpid()%5000; port < 32000; port += 7 {
		conn, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			continue
		}
		_ = conn.Close()
		return datagramEcho(t, conn.LocalAddr().String())
	}
	t.Fatal("no free datagram port below the ephemeral range")
	return netip.AddrPort{}
}

func probeDatagrams(t *testing.T, self string, plan linuxsandbox.Plan, targets ...netip.AddrPort) string {
	t.Helper()
	lines := make([]string, len(targets))
	for i, a := range targets {
		lines[i] = a.String()
	}
	env := append(probeEnv("udpsend"), linuxProbePathsEnv+"="+strings.Join(lines, "\n"))
	status, out := rawHelperRun(t, self, plan, env, self, probeTestFlag)
	if status.ExitStatus() != 0 {
		t.Fatalf("datagram probe: exit %d: %s", status.ExitStatus(), out)
	}
	return out
}

func requireDatagrams(t *testing.T, out string, dst netip.AddrPort, want string) {
	t.Helper()
	for _, how := range datagramSends {
		line := fmt.Sprintf("udp %s %s: %s", how, dst, want)
		if !strings.Contains(out, line+"\n") {
			t.Fatalf("want %q:\n%s", line, out)
		}
	}
}

func networkPlan(n linuxsandbox.Network) linuxsandbox.Plan {
	return linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Attest:  linuxsandbox.Attest{HostNamespaces: linuxsandbox.HostNamespaces()},
		Network: n,
	}
}

// A granted loopback port reaches the host's datagram service however the
// process sends; a service that starts after launch is reached by connecting.
// Any other port stays inside the sandbox.
func TestLinuxLoopbackDatagramGrantReachesTheHost(t *testing.T) {
	self := requireLinuxSandbox(t)
	granted := serviceEcho(t)
	other := serviceEcho(t)
	plan := networkPlan(linuxsandbox.Network{Mode: linuxsandbox.NetworkDeny, LoopbackConnect: true, LoopbackConnectPorts: []uint16{granted.Port()}})
	out := probeDatagrams(t, self, plan, granted, other)
	requireDatagrams(t, out, granted, "ok host")
	for _, how := range datagramSends {
		if strings.Contains(out, fmt.Sprintf("udp %s %s: ok", how, other)) {
			t.Fatalf("ungranted port reached the host service:\n%s", out)
		}
	}

	plan = networkPlan(linuxsandbox.Network{Mode: linuxsandbox.NetworkDeny, LoopbackConnect: true})
	late := datagramEcho(t, "127.0.0.1:0")
	out = probeDatagrams(t, self, plan, late)
	if !strings.Contains(out, fmt.Sprintf("udp connected %s: ok host\n", late)) {
		t.Fatalf("connecting did not reach a service the launch relays did not cover:\n%s", out)
	}
}

// hostAddress is an address of this host's own off loopback, which a datagram
// can reach only by leaving the sandbox's namespace.
func hostAddress(t *testing.T) netip.Addr {
	t.Helper()
	addrs, err := net.InterfaceAddrs()
	testutil.FailErr(t, "interface addresses", err)
	for _, a := range addrs {
		prefix, err := netip.ParsePrefix(a.String())
		if err == nil && prefix.Addr().Is4() && !prefix.Addr().IsLoopback() && !prefix.Addr().IsLinkLocalUnicast() {
			return prefix.Addr()
		}
	}
	t.Fatal("the host has no IPv4 address off loopback")
	return netip.Addr{}
}

// A udp permit carries datagrams to its port through the gateway however the
// process sends, and a datagram to any other port reaches nothing: a connect
// to it is refused, and an unconnected send is dropped.
func TestLinuxDirectIPDatagramsCrossTheGateway(t *testing.T) {
	self := requireLinuxSandbox(t)
	host := hostAddress(t)
	permitted := datagramEcho(t, netip.AddrPortFrom(host, 0).String())
	other := datagramEcho(t, netip.AddrPortFrom(host, 0).String())
	plan := networkPlan(linuxsandbox.Network{
		Mode:            linuxsandbox.NetworkDirectIP,
		DirectIPPermits: []linuxsandbox.DirectIPPermit{{Protocol: "tcp", Port: 443}, {Protocol: "udp", Port: permitted.Port()}},
	})
	out := probeDatagrams(t, self, plan, permitted, other)
	requireDatagrams(t, out, permitted, "ok host")
	if !strings.Contains(out, fmt.Sprintf("udp connected %s: EACCES\n", other)) {
		t.Fatalf("a connect to a port no permit covers was not refused:\n%s", out)
	}
	for _, how := range []string{"sendto", "sendmsg"} {
		if !strings.Contains(out, fmt.Sprintf("udp %s %s: no reply\n", how, other)) {
			t.Fatalf("an unconnected send to a port no permit covers was answered:\n%s", out)
		}
	}
	// Without a udp permit there is no gateway, and the namespace has no route.
	plan.Network.DirectIPPermits = plan.Network.DirectIPPermits[:1]
	out = probeDatagrams(t, self, plan, permitted)
	if strings.Contains(out, ": ok") {
		t.Fatalf("a datagram left the sandbox without a udp permit:\n%s", out)
	}
}

// A granted datagram listener is reachable from the host on loopback; an
// ungranted one stays inside the sandbox.
func TestLinuxLocalListenDatagramGrantIsReachableFromTheHost(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, granted := range []bool{true, false} {
		port := freePort(t)
		c := confine.Confinement{Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride}
		if granted {
			c.LocalListen, c.LocalListenPorts = true, []uint16{port}
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		cmd, cleanup, err := confine.Command(ctx, helperBinary(t, self), self, []string{probeTestFlag}, c)
		testutil.FailErr(t, "confine.Command", err)
		cmd.Env = append(probeEnv("udpserve"), linuxProbePathsEnv+"="+addr)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		testutil.FailErr(t, "start server", cmd.Start())
		cleanup()
		reached := ""
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && reached == "" {
			reached = askDatagram(addr)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		if granted && reached != "served" {
			t.Fatalf("granted datagram listener not reachable from the host (got %q):\n%s", reached, out.String())
		}
		if !granted && reached != "" {
			t.Fatalf("ungranted datagram listener reachable from the host (got %q):\n%s", reached, out.String())
		}
	}
}

func askDatagram(addr string) string {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		time.Sleep(100 * time.Millisecond)
		return ""
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("ping"))
	_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		time.Sleep(100 * time.Millisecond)
		return ""
	}
	return string(buf[:n])
}
