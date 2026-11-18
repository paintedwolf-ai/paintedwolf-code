//go:build !linux

package confine_test

import (
	"bufio"
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
	output, err := reg.StdoutPipe()
	testutil.FailErr(t, "open dns-sd output", err)
	reg.Stderr = reg.Stdout
	testutil.FailErr(t, "start dns-sd registration", reg.Start())
	t.Cleanup(func() {
		_ = reg.Process.Kill()
		_ = reg.Wait()
	})
	// dns-sd answers once for the record: registered, or why not.
	type registration struct{ reply, transcript string }
	answered := make(chan registration, 1)
	go func() {
		var seen strings.Builder
		for lines := bufio.NewScanner(output); lines.Scan(); {
			seen.WriteString(lines.Text() + "\n")
			if strings.Contains(lines.Text(), "Got a reply for record "+name) {
				answered <- registration{lines.Text(), seen.String()}
				return
			}
		}
		answered <- registration{transcript: seen.String()}
	}()
	got := testutil.Receive(t, "dns-sd answers for "+name, answered)
	if !strings.Contains(got.reply, "Name now registered and active") {
		t.Fatalf("dns-sd did not register %s:\n%s", name, got.transcript)
	}
	out, err := exec.Command(resolveProbe[0], append(resolveProbe[1:], name)...).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != resolvableNameAddr {
		t.Fatalf("registered name %s did not resolve to %s: err=%v probe=%q", name, resolvableNameAddr, err, out)
	}
	return name
}
