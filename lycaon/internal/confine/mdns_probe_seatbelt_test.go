package confine_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

// mdnsProbeAddr is TEST-NET-1, so an answer cannot come from a local interface.
const mdnsProbeAddr = "192.0.2.7"

var resolveProbe = []string{"/usr/bin/python3", "-c", "import socket,sys; print(socket.gethostbyname(sys.argv[1]))"}

// registerMDNSName publishes a unique .local name that only mDNSResponder can
// answer. Hosts-file names resolve in-process even under deny, and some runner
// images list the machine's own name there.
func registerMDNSName(t *testing.T) string {
	t.Helper()
	label := fmt.Sprintf("pw-confine-%d-%d", os.Getpid(), time.Now().UnixNano())
	name := label + ".local"
	reg := exec.Command("/usr/bin/dns-sd", "-P", label, "_pwconfine._udp", "local", "9", name, mdnsProbeAddr)
	testutil.FailErr(t, "start dns-sd registration", reg.Start())
	t.Cleanup(func() {
		_ = reg.Process.Kill()
		_ = reg.Wait()
	})
	var last string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		out, err := exec.Command(resolveProbe[0], append(resolveProbe[1:], name)...).CombinedOutput()
		last = string(out)
		if err == nil && strings.TrimSpace(last) == mdnsProbeAddr {
			return name
		}
	}
	t.Fatalf("mDNSResponder did not publish %s: %s", name, last)
	return ""
}

// confinedResolve resolves name under c and returns the exit code and address.
func confinedResolve(t *testing.T, self string, c confine.Confinement, name string) (int, string) {
	t.Helper()
	code, out, _ := confinedStdout(t, self, c, resolveProbe[0], append(resolveProbe[1:], name)...)
	return code, strings.TrimSpace(out)
}
