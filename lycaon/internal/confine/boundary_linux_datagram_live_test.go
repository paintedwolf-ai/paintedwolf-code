//go:build linux

package confine_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

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
		sends := datagramSends
		if mode == "udpsendto" {
			sends = []string{"sendto"}
		}
		for _, how := range sends {
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
	return datagramEcho(t, net.JoinHostPort("127.0.0.1", strconv.Itoa(int(servicePort(t)))))
}

// nextServicePort is where servicePort looks next, so no two callers in one
// test process get the same port.
var nextServicePort atomic.Int32

// servicePort is a loopback port below the ephemeral range, free for both
// streams and datagrams. A client's source port comes from the ephemeral
// range, so a datagram client can never be connected to itself there.
func servicePort(t *testing.T) uint16 {
	t.Helper()
	nextServicePort.CompareAndSwap(0, int32(20000+os.Getpid()%5000))
	for {
		port := int(nextServicePort.Add(7))
		if port >= 32000 {
			t.Fatal("no free port below the ephemeral range")
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		pc, err := net.ListenPacket("udp", addr)
		if err != nil {
			continue
		}
		ln, err := net.Listen("tcp", addr)
		_ = pc.Close()
		if err != nil {
			continue
		}
		_ = ln.Close()
		return uint16(port)
	}
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

// farHostEnv marks the test binary re-executed inside its own network
// namespace, where farHost is an address off loopback.
const farHostEnv = "PW_CONFINE_FAR_HOST"

// farHost is a documentation address (TEST-NET-1) the fixture namespace
// assigns itself, so a datagram reaches it only by leaving the sandbox's
// namespace through the gateway, with no network outside the test.
var farHost = netip.MustParseAddr("192.0.2.1")

// inFarHostNamespace runs the calling test again in a fresh user and network
// namespace that holds farHost, and reports false there, where the test body
// runs. The sandbox nests its own namespaces inside it as it would on a host.
func inFarHostNamespace(t *testing.T, self string) bool {
	t.Helper()
	if os.Getenv(farHostEnv) != "" {
		testutil.FailErr(t, "assign the far host address", assignFarHost())
		return false
	}
	cmd := exec.Command(self, "-test.run=^"+t.Name()+"$", "-test.v", "-test.count=1", "-test.timeout=5m")
	// The coverage helper is built once, outside, and shared.
	cmd.Env = append(os.Environ(), farHostEnv+"=1", helperBinaryEnv+"="+helperBinary(t, self))
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
		UidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getuid(), HostID: os.Getuid(), Size: 1}},
		GidMappings:                []syscall.SysProcIDMap{{ContainerID: os.Getgid(), HostID: os.Getgid(), Size: 1}},
		GidMappingsEnableSetgroups: false,
		// Executing as a non-root uid would otherwise drop what assigning the
		// address needs.
		AmbientCaps: []uintptr{unix.CAP_NET_ADMIN},
	}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS: "+t.Name()) {
		t.Fatalf("in the far host namespace: %v\n%s", err, out)
	}
	return true
}

// assignFarHost brings this namespace's loopback up and adds farHost to it.
func assignFarHost() error {
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	lo, err := unix.NewIfreq("lo")
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(fd, unix.SIOCGIFFLAGS, lo); err != nil {
		return err
	}
	lo.SetUint16(lo.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(fd, unix.SIOCSIFFLAGS, lo); err != nil {
		return err
	}
	// A labelled alias adds an address beside 127.0.0.1 rather than replacing it.
	alias, err := unix.NewIfreq("lo:far")
	if err != nil {
		return err
	}
	if err := alias.SetInet4Addr(farHost.AsSlice()); err != nil {
		return err
	}
	return unix.IoctlIfreq(fd, unix.SIOCSIFADDR, alias)
}

// A udp permit carries datagrams to its port through the gateway however the
// process sends, and a closed port there is reported as the far host reports
// it. A datagram to any other port reaches nothing: a connect to it is
// refused, and an unconnected send is dropped.
func TestLinuxDirectIPDatagramsCrossTheGateway(t *testing.T) {
	self := requireLinuxSandbox(t)
	if inFarHostNamespace(t, self) {
		return
	}
	host := farHost
	permitted := datagramEcho(t, netip.AddrPortFrom(host, 0).String())
	other := datagramEcho(t, netip.AddrPortFrom(host, 0).String())
	closed := closedDatagramPort(t, host)
	plan := networkPlan(linuxsandbox.Network{
		Mode: linuxsandbox.NetworkDirectIP,
		DirectIPPermits: []linuxsandbox.DirectIPPermit{
			{Protocol: "tcp", Port: 443}, {Protocol: "udp", Port: permitted.Port()}, {Protocol: "udp", Port: closed.Port()},
		},
	})
	out := probeDatagrams(t, self, plan, permitted, other, closed)
	requireDatagrams(t, out, permitted, "ok host")
	if !strings.Contains(probeLine(out, fmt.Sprintf("udp connected %s: ", closed)), "connection refused") {
		t.Fatalf("a closed permitted port was not reported refused:\n%s", out)
	}
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

	// The gateway reports the unconnected send it dropped to the action.
	c, lease := boundConfinement(t, confine.Confinement{
		Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDirectIP, EgressOff: confine.EgressOffOverride,
		DirectIPPermits: []confine.DirectIPPermit{{Protocol: "udp", Port: permitted.Port()}},
	})
	env := append(probeEnv("udpsendto"), linuxProbePathsEnv+"="+other.String())
	status, out := linuxConfinedRun(t, self, *c, env, self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, fmt.Sprintf("udp sendto %s: no reply", other)) {
		t.Fatalf("unconnected send to an unpermitted port: exit %d:\n%s", status.ExitStatus(), out)
	}
	refusals := lease.SettledRefusals(context.Background())
	if _, reported := refusalTargets(refusals)["network-outbound remote:"+other.String()]; !reported {
		t.Fatalf("refusals = %+v, want the dropped datagram to %s", refusals, other)
	}
}

// probeLine is the probe's output line that starts with prefix.
func probeLine(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

// closedDatagramPort is a port on addr nothing serves datagrams on.
func closedDatagramPort(t *testing.T, addr netip.Addr) netip.AddrPort {
	t.Helper()
	conn, err := net.ListenPacket("udp", netip.AddrPortFrom(addr, 0).String())
	testutil.FailErr(t, "reserve a port", err)
	port := conn.LocalAddr().(*net.UDPAddr).AddrPort()
	_ = conn.Close()
	return port
}

// A granted datagram listener is reachable from the host on loopback; an
// ungranted one stays inside the sandbox.
func TestLinuxLocalListenDatagramGrantIsReachableFromTheHost(t *testing.T) {
	self := requireLinuxSandbox(t)
	for _, granted := range []bool{true, false} {
		// A free ephemeral port would let the host's client be connected to
		// itself and read back its own datagram.
		port := servicePort(t)
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

// askDatagram sends one datagram to addr and returns the reply. A client the
// kernel connected to itself would read back its own datagram, so that
// attempt gets no reply.
func askDatagram(addr string) string {
	conn, err := net.Dial("udp", addr)
	if err != nil {
		time.Sleep(100 * time.Millisecond)
		return ""
	}
	defer conn.Close()
	if conn.LocalAddr().String() == conn.RemoteAddr().String() {
		return ""
	}
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
