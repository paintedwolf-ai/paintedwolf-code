// Package sourceoutage serves a test's source directory over loopback NFS
// through a relay the test can cut and restore, so source-transport outages
// are rehearsed against the operating system's own NFS client.
//
// A test process mounts a Volume; the helper processes it starts cut and
// restore that volume through the control socket named by EnvControl.
package sourceoutage

import (
	"bufio"
	"net"
	"os"
	"strings"
	"testing"
)

// EnvControl names the control socket of a mounted Volume for the helper
// processes a test starts.
const EnvControl = "PW_SOURCE_OUTAGE_CONTROL"

// Controlled reports whether this process runs under a mounted Volume.
func Controlled() bool { return os.Getenv(EnvControl) != "" }

// Stop cuts the transport of the Volume named by EnvControl.
func Stop(t testing.TB) {
	t.Helper()
	request(t, "stop")
}

// Restore reconnects the transport of the Volume named by EnvControl.
func Restore(t testing.TB) {
	t.Helper()
	request(t, "restore")
}

// request returns once the volume has carried out the command.
func request(t testing.TB, command string) {
	t.Helper()
	conn, err := new(net.Dialer).DialContext(t.Context(), "unix", os.Getenv(EnvControl)) // #nosec G704 -- the parent test names its own socket
	if err != nil {
		t.Fatalf("source outage control: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte(command + "\n")); err != nil {
		t.Fatalf("source outage %s: %v", command, err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || strings.TrimSpace(reply) != "ok" {
		t.Fatalf("source outage %s: %q %v", command, reply, err)
	}
}
