//go:build !linux

package confine_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// registerResolvableName publishes a unique .local name that only mDNSResponder
// can answer. Hosts-file names resolve in-process even under deny, and some
// runner images list the machine's own name there.
func registerResolvableName(t *testing.T) string {
	t.Helper()
	label := fmt.Sprintf("pw-confine-%d-%d", os.Getpid(), time.Now().UnixNano())
	name := label + ".local"
	reg := exec.Command("/usr/bin/dns-sd", "-P", label, "_pwconfine._udp", "local", "9", name, resolvableNameAddr)
	testutil.FailErr(t, "start dns-sd registration", reg.Start())
	t.Cleanup(func() {
		_ = reg.Process.Kill()
		_ = reg.Wait()
	})
	var last string
	for ; t.Context().Err() == nil; time.Sleep(100 * time.Millisecond) {
		out, err := exec.Command(resolveProbe[0], append(resolveProbe[1:], name)...).CombinedOutput()
		last = string(out)
		if err == nil && strings.TrimSpace(last) == resolvableNameAddr {
			return name
		}
	}
	t.Fatalf("mDNSResponder did not publish %s: %s", name, last)
	return ""
}
