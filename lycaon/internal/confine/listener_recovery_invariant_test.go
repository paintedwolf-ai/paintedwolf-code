package confine

import (
	"net/netip"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/confine/linuxsandbox"
)

// Invariant I10, routing half: every reported listener names local_listen
// with its port, whichever platform reported it, and refusals that withhold
// interface state never route to host execution.
func TestListenerAndProbeRefusalsRouteByWhatTheyAre(t *testing.T) {
	var rules FilesystemRules
	ports := []uint16{1, 80, 3000, 8765, 65535}
	for _, port := range ports {
		report := "Sandbox: Python(41) deny(1) network-bind local:*:" + strconv.Itoa(int(port))
		parsed, ok := parseKernelRefusal(kernelMessage(report))
		if !ok {
			t.Fatalf("kernel report %q did not parse", report)
		}
		checkListenerRoute(t, routeRefusal(rules, parsed.Operation, parsed.Target, false), port)
		for _, addr := range []string{"127.0.0.1", "0.0.0.0", "::1", "::", "::ffff:127.0.0.1"} {
			for _, stream := range []bool{true, false} {
				req := linuxsandbox.SocketRequest{Op: linuxsandbox.OpBind, IP: netip.AddrPortFrom(netip.MustParseAddr(addr), port), Stream: stream}
				d := linuxsandbox.Network{Mode: linuxsandbox.NetworkDeny}.Decide(req)
				if d.Refusal == nil {
					t.Fatalf("contained listener %+v was not reported", req)
				}
				checkListenerRoute(t, routeRefusal(rules, d.Refusal.Operation, d.Refusal.Target, false), port)
			}
		}
	}
	for _, info := range []string{"net.link.addr", "vfs.disk-space", "kern.hostname", ""} {
		if got := routeOperation(rules, "system-info", info); got.Recovery != RecoverNone {
			t.Errorf("system-info %q routes to %s, want none", info, got.Recovery)
		}
	}
	for _, family := range []string{socketFamilyRoute, socketFamilySystem, "1", "27", "29"} {
		for _, typ := range []string{"1", "2", "3"} {
			for _, protocol := range []string{"0", "1", "2"} {
				target := "domain:" + family + " type:" + typ + " protocol:" + protocol
				want := RecoverHostExecution
				if family == socketFamilyRoute || family == socketFamilySystem {
					want = RecoverNone
				}
				if got := routeOperation(rules, "system-socket", target); got.Recovery != want {
					t.Errorf("system-socket %q routes to %s, want %s", target, got.Recovery, want)
				}
			}
		}
	}
}

func checkListenerRoute(t *testing.T, r SandboxRefusal, port uint16) {
	t.Helper()
	if r.Recovery != RecoverLocalListen || r.Port() != strconv.Itoa(int(port)) {
		t.Errorf("listener refusal %q routes to %s port %q, want local_listen port %d", r.Target, r.Recovery, r.Port(), port)
	}
}
