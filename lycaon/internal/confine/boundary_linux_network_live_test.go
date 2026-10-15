//go:build linux

package confine_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
)

// probeTarget is a destination only the broker can answer for: it does not
// resolve, so a response proves the request reached the broker.
const probeTarget = "http://pw-confine-probe.invalid/"

// proxyGetProbe fetches probeTarget the way any tool does, through the proxy
// its environment names, and prints the status or the transport error.
func proxyGetProbe() int {
	client := &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	resp, err := client.Get(probeTarget)
	if err != nil {
		fmt.Println("proxyget error:", err)
		return 0
	}
	_ = resp.Body.Close()
	fmt.Println("proxyget status:", resp.StatusCode)
	return 0
}

// proxyConfinement binds a proxy-only boundary to a fresh action.
func proxyConfinement(t *testing.T, root string) (*confine.Confinement, *confine.ActionLease) {
	t.Helper()
	c := &confine.Confinement{Roots: sandboxRoots(root), Network: confine.NetworkProxyOnly, ProjectID: "linux-proxy"}
	lease, err := confine.BindAction(c, confine.EgressCommand{
		ProjectID: "linux-proxy", SessionID: "s1", ToolCallID: "call-" + t.Name(), ToolName: "command", CommandLine: "proxyget",
	})
	testutil.FailErr(t, "bind action", err)
	t.Cleanup(func() { lease.Close(context.Background()) })
	return c, lease
}

func runProxyProbe(t *testing.T, self string, c *confine.Confinement) string {
	t.Helper()
	env := confine.ProcessEnvironment(probeEnv("proxyget"), *c)
	status, out := linuxConfinedRun(t, self, *c, env, self, probeTestFlag)
	if status.ExitStatus() != 0 {
		t.Fatalf("proxy probe: exit %d: %s", status.ExitStatus(), out)
	}
	return out
}

// decideEgress makes every new destination ask, and answers allow.
func decideEgress(t *testing.T, allow bool) {
	t.Helper()
	confine.SetEgressPosture(confine.PostureAsk)
	t.Cleanup(func() { confine.SetEgressPosture(confine.PostureObserve) })
	release := confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return allow
	})
	t.Cleanup(func() { _ = release(context.Background()) })
}

// The confined process reaches the broker only through the outer helper's
// forwarder, and the broker places the request under this action's lease.
func TestLinuxProxyOnlyReachesTheBrokerByLineage(t *testing.T) {
	self := requireLinuxSandbox(t)
	liveBroker(t)
	for _, allow := range []bool{false, true} {
		decideEgress(t, allow)
		c, lease := proxyConfinement(t, t.TempDir())
		out := runProxyProbe(t, self, c)
		if !strings.Contains(out, "proxyget status:") {
			t.Fatalf("allow=%v: the request did not reach the broker: %s", allow, out)
		}
		hosts := lease.ObservedHosts()
		if len(hosts) != 1 || hosts[0].Host != "pw-confine-probe.invalid" || hosts[0].Allowed != allow {
			t.Fatalf("allow=%v: broker observations = %+v\n%s", allow, hosts, out)
		}
	}
}

// With the broker gone, the forwarder has nowhere to send the request, and
// the namespace offers no other route.
func TestLinuxProxyOnlyWithoutTheBrokerFailsClosed(t *testing.T) {
	self := requireLinuxSandbox(t)
	liveBroker(t)
	decideEgress(t, true)
	c, lease := proxyConfinement(t, t.TempDir())
	testutil.FailErr(t, "stop broker", confine.StopEgressBroker())
	out := runProxyProbe(t, self, c)
	if !strings.Contains(out, "proxyget error:") {
		t.Fatalf("request succeeded without the broker: %s", out)
	}
	if hosts := lease.ObservedHosts(); len(hosts) != 0 {
		t.Fatalf("observations without a broker: %+v", hosts)
	}
	if _, err := confine.BindAction(&confine.Confinement{Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkProxyOnly}, confine.EgressCommand{ToolCallID: "after-stop"}); err == nil {
		t.Fatal("a proxy-only action bound with no broker running")
	}
}

// networkProbe runs the socket probes the grant tests drive. Each target
// comes from the probe paths variable, one per line.
func networkProbe(mode string) int {
	targets := strings.Split(os.Getenv(linuxProbePathsEnv), "\n")
	switch mode {
	case "tcpconnect":
		for _, addr := range targets {
			conn, err := net.DialTimeout("tcp", addr, 10*time.Second)
			if err != nil {
				fmt.Printf("connect %s: %s\n", addr, errnoName(err))
				continue
			}
			greeting, _ := io.ReadAll(io.LimitReader(conn, 64))
			_ = conn.Close()
			fmt.Printf("connect %s: ok %s\n", addr, greeting)
		}
	case "unixconnect":
		for _, path := range targets {
			conn, err := net.DialTimeout("unix", path, 10*time.Second)
			if err != nil {
				fmt.Printf("connect %s: %s\n", path, errnoName(err))
				continue
			}
			greeting, _ := io.ReadAll(io.LimitReader(conn, 64))
			_ = conn.Close()
			fmt.Printf("connect %s: ok %s\n", path, greeting)
		}
	case "resolve":
		resolver := &net.Resolver{PreferGo: true}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		addrs, err := resolver.LookupHost(ctx, targets[0])
		fmt.Println("resolve:", addrs, err)
	case "serve":
		ln, err := net.Listen("tcp", targets[0])
		if err != nil {
			fmt.Println("listen:", err)
			return 2
		}
		fmt.Println("listening", ln.Addr())
		if tl, ok := ln.(*net.TCPListener); ok {
			_ = tl.SetDeadline(time.Now().Add(20 * time.Second))
		}
		conn, err := ln.Accept()
		if err != nil {
			fmt.Println("accept:", err)
			return 3
		}
		_, _ = io.WriteString(conn, "served")
		_ = conn.Close()
	}
	return 0
}

// greeter serves one fixed greeting per connection on the host until the test ends.
func greeter(t *testing.T, network, addr string) net.Listener {
	t.Helper()
	ln, err := net.Listen(network, addr)
	testutil.FailErr(t, "listen "+addr, err)
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(conn, "host")
			_ = conn.Close()
		}
	}()
	return ln
}

func portOfListener(ln net.Listener) uint16 {
	return uint16(ln.Addr().(*net.TCPAddr).Port)
}

// boundConfinement binds c to a fresh action so its refusals reach a feed.
func boundConfinement(t *testing.T, c confine.Confinement) (*confine.Confinement, *confine.ActionLease) {
	t.Helper()
	confine.StartRefusalWatch(context.Background(), t.TempDir())
	t.Cleanup(confine.StopRefusalWatch)
	lease, err := confine.BindAction(&c, confine.EgressCommand{ToolCallID: "call-" + t.Name(), ToolName: "command", CommandLine: "probe"})
	testutil.FailErr(t, "bind action", err)
	t.Cleanup(func() { lease.Close(context.Background()) })
	if w := lease.Refusals().Witness; w != confine.WitnessIncomplete {
		t.Fatalf("bound action has refusal witness %q, want a live supervised feed", w)
	}
	return &c, lease
}

func probeLines(t *testing.T, self string, c confine.Confinement, mode string, targets ...string) string {
	t.Helper()
	env := append(probeEnv(mode), linuxProbePathsEnv+"="+strings.Join(targets, "\n"))
	status, out := linuxConfinedRun(t, self, c, env, self, probeTestFlag)
	if status.ExitStatus() != 0 {
		t.Fatalf("%s probe: exit %d: %s", mode, status.ExitStatus(), out)
	}
	return out
}

func refusalTargets(r confine.SandboxRefusals) map[string]confine.RefusalRecovery {
	out := map[string]confine.RefusalRecovery{}
	for _, x := range r.Refusals {
		out[x.Operation+" "+x.Target] = x.Recovery
	}
	return out
}

// A granted loopback port reaches the host's listener through a socket the
// supervisor connects; any other port stays inside the sandbox, and the
// refusal reaches the action with the capability that would open it.
func TestLinuxLoopbackConnectGrantReachesTheHost(t *testing.T) {
	self := requireLinuxSandbox(t)
	granted := greeter(t, "tcp", "127.0.0.1:0")
	other := greeter(t, "tcp", "127.0.0.1:0")
	c, lease := boundConfinement(t, confine.Confinement{
		Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
		LoopbackConnect: true, LoopbackConnectPorts: []uint16{portOfListener(granted)},
	})
	out := probeLines(t, self, *c, "tcpconnect", granted.Addr().String(), other.Addr().String())
	if !strings.Contains(out, "connect "+granted.Addr().String()+": ok host") {
		t.Fatalf("granted port did not reach the host listener:\n%s", out)
	}
	if strings.Contains(out, "connect "+other.Addr().String()+": ok") {
		t.Fatalf("ungranted port reached the host listener:\n%s", out)
	}
	refusals := lease.SettledRefusals(context.Background())
	key := "network-outbound remote:" + other.Addr().String()
	if refusals.Witness != confine.WitnessIncomplete || refusalTargets(refusals)[key] != confine.RecoverOutboundPort {
		t.Fatalf("refusals = %+v, want %s routed to %s with an incomplete witness", refusals, key, confine.RecoverOutboundPort)
	}
	if _, reported := refusalTargets(refusals)["network-outbound remote:"+granted.Addr().String()]; reported {
		t.Fatalf("an admitted connection was reported refused: %+v", refusals)
	}
}

// An exact socket grant connects; every other path socket stays refused and
// is reported with the grant that would open it.
func TestLinuxUnixSocketGrantConnectsOnlyItsPath(t *testing.T) {
	self := requireLinuxSandbox(t)
	dir, err := os.MkdirTemp("/tmp", "pw-grant-") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "socket directory", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	granted := greeter(t, "unix", filepath.Join(dir, "granted.sock"))
	other := greeter(t, "unix", filepath.Join(dir, "other.sock"))
	grantedPath := granted.Addr().String()
	c, lease := boundConfinement(t, confine.Confinement{
		Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
		SocketGrants: []confine.SocketGrant{{ApprovedPath: grantedPath, ResolvedPath: grantedPath}},
	})
	out := probeLines(t, self, *c, "unixconnect", grantedPath, other.Addr().String())
	if !strings.Contains(out, "connect "+grantedPath+": ok host") {
		t.Fatalf("granted socket did not connect:\n%s", out)
	}
	if !strings.Contains(out, "connect "+other.Addr().String()+": EACCES") {
		t.Fatalf("ungranted socket was not refused with EACCES:\n%s", out)
	}
	settled := lease.SettledRefusals(context.Background())
	if refusalTargets(settled)["network-outbound "+other.Addr().String()] != confine.RecoverSocketPath {
		t.Fatalf("refusals = %+v, want the ungranted path routed to %s\n%s", settled, confine.RecoverSocketPath, out)
	}
}

// A granted listener is reachable from the host on loopback; an ungranted one
// stays inside the sandbox.
func TestLinuxLocalListenGrantIsReachableFromTheHost(t *testing.T) {
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
		cmd.Env = append(probeEnv("serve"), linuxProbePathsEnv+"="+addr)
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		testutil.FailErr(t, "start server", cmd.Start())
		cleanup()
		reached := ""
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) && reached == "" {
			conn, err := net.DialTimeout("tcp", addr, time.Second)
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			body, _ := io.ReadAll(conn)
			_ = conn.Close()
			reached = string(body)
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		cancel()
		if granted && reached != "served" {
			t.Fatalf("granted listener not reachable from the host (got %q):\n%s", reached, out.String())
		}
		if !granted && reached != "" {
			t.Fatalf("ungranted listener reachable from the host (got %q):\n%s", reached, out.String())
		}
	}
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	testutil.FailErr(t, "free port", err)
	defer ln.Close()
	return portOfListener(ln)
}

// fakeResolver answers every A question with 192.0.2.7 until the test ends.
func fakeResolver(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	testutil.FailErr(t, "resolver listen", err)
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var p dnsmessage.Parser
			header, err := p.Start(buf[:n])
			if err != nil {
				continue
			}
			q, err := p.Question()
			if err != nil {
				continue
			}
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: header.ID, Response: true, Authoritative: true})
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			if q.Type == dnsmessage.TypeA {
				_ = b.AResource(dnsmessage.ResourceHeader{Name: q.Name, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{192, 0, 2, 7}})
			}
			reply, err := b.Finish()
			if err == nil {
				_, _ = pc.WriteTo(reply, from)
			}
		}
	}()
	return pc.LocalAddr().String()
}

// Direct IP reaches names through the stub the supervisor relays to the
// host's resolver, and reaches a loopback listener on the host.
func TestLinuxDirectIPResolvesThroughTheHostResolver(t *testing.T) {
	self := requireLinuxSandbox(t)
	plan := linuxsandbox.Plan{
		Version: linuxsandbox.PlanVersion,
		Attest:  linuxsandbox.Attest{HostNamespaces: linuxsandbox.HostNamespaces()},
		Network: linuxsandbox.Network{
			Mode: linuxsandbox.NetworkDirectIP, Resolvers: []string{fakeResolver(t)},
			DirectIPPermits: []linuxsandbox.DirectIPPermit{{Protocol: "tcp", Port: 443}},
		},
	}
	env := append(probeEnv("resolve"), linuxProbePathsEnv+"=probe.example")
	status, out := rawHelperRun(t, self, plan, env, self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, "resolve: [192.0.2.7] <nil>") {
		t.Fatalf("direct IP name resolution: exit %d: %s", status.ExitStatus(), out)
	}
	host := greeter(t, "tcp", "127.0.0.1:0")
	env = append(probeEnv("tcpconnect"), linuxProbePathsEnv+"="+host.Addr().String())
	status, out = rawHelperRun(t, self, plan, env, self, probeTestFlag)
	if status.ExitStatus() != 0 || !strings.Contains(out, "connect "+host.Addr().String()+": ok host") {
		t.Fatalf("direct IP loopback connect: exit %d: %s", status.ExitStatus(), out)
	}
}

// rawHelperRun runs the helper on a hand-built plan with env and returns the
// wait status and combined output.
func rawHelperRun(t *testing.T, self string, plan linuxsandbox.Plan, env []string, target ...string) (syscall.WaitStatus, string) {
	t.Helper()
	plan.Filesystem.WriteRoots = sandboxRoots(plan.Filesystem.WriteRoots...)
	raw, err := plan.Encode()
	testutil.FailErr(t, "encode plan", err)
	pr, pw, err := os.Pipe()
	testutil.FailErr(t, "pipe", err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := append([]string{"__confine-exec", "--profile-fd", "3", "--"}, target...)
	cmd := exec.CommandContext(ctx, helperBinary(t, self), args...)
	cmd.ExtraFiles = []*os.File{pr}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	go func() {
		defer func() { _ = pw.Close() }()
		_, _ = pw.Write(raw)
	}()
	_ = cmd.Run()
	_ = pr.Close()
	if cmd.ProcessState == nil {
		t.Fatalf("helper did not start: %s", out.String())
	}
	return cmd.ProcessState.Sys().(syscall.WaitStatus), out.String()
}

// Concurrent actions keep their own refusals, including those a descendant
// of the command makes.
func TestLinuxConcurrentActionsKeepTheirOwnRefusals(t *testing.T) {
	self := requireLinuxSandbox(t)
	type run struct {
		lease  *confine.ActionLease
		target string
		out    string
	}
	runs := make([]*run, 2)
	done := make(chan struct{}, len(runs))
	for i := range runs {
		c, lease := boundConfinement(t, confine.Confinement{
			Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
		})
		r := &run{lease: lease, target: net.JoinHostPort("127.0.0.1", strconv.Itoa(int(freePort(t))))}
		runs[i] = r
		// The connection is made by a grandchild of the helper.
		cmd, cleanup, err := confine.Command(t.Context(), helperBinary(t, self), "/bin/sh", []string{"-c", self + " " + probeTestFlag}, *c)
		testutil.FailErr(t, "confine.Command", err)
		cmd.Env = append(probeEnv("tcpconnect"), linuxProbePathsEnv+"="+r.target)
		go func() {
			defer func() { done <- struct{}{} }()
			defer cleanup()
			out, _ := cmd.CombinedOutput()
			r.out = string(out)
		}()
	}
	for range runs {
		<-done
	}
	for i, r := range runs {
		refusals := refusalTargets(r.lease.SettledRefusals(context.Background()))
		if len(refusals) != 1 || refusals["network-outbound remote:"+r.target] != confine.RecoverOutboundPort {
			t.Fatalf("action %d refusals = %v, want only its own %s\n%s", i, refusals, r.target, r.out)
		}
	}
}

// Killing the outer helper ends the whole sandbox: nothing it started survives.
func TestLinuxSupervisorLossEndsTheSandbox(t *testing.T) {
	self := requireLinuxSandbox(t)
	c, lease := boundConfinement(t, confine.Confinement{
		Roots: sandboxRoots(t.TempDir()), Network: confine.NetworkDeny, EgressOff: confine.EgressOffOverride,
	})
	cmd, cleanup, err := confine.Command(t.Context(), helperBinary(t, self), "/bin/sh", []string{"-c", "sleep 30 & sleep 30"}, *c)
	testutil.FailErr(t, "confine.Command", err)
	testutil.FailErr(t, "start", cmd.Start())
	cleanup()
	time.Sleep(time.Second)
	testutil.FailErr(t, "kill outer helper", cmd.Process.Kill())
	_ = cmd.Wait()
	deadline := time.Now().Add(10 * time.Second)
	for {
		survivors, err := lease.Survivors()
		testutil.FailErr(t, "survivors", err)
		if len(survivors) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("processes outlived the killed supervisor: %v", survivors)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
