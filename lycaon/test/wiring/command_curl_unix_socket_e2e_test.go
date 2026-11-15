//go:build darwin || linux

package wiring

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// serveDaemonSocket serves a fixed response on a Unix socket outside the
// project, as a container engine's daemon does.
func serveDaemonSocket(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pw-daemon-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create daemon dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "daemon.sock")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen on daemon socket", err)
	hits := &atomic.Int32{}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, "daemon "+r.URL.Path)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return path, hits
}

// Native curl reaches a Unix socket after one local-service approval: the
// placeholder URL host asks for no loopback port and the socket is no file read.
func TestNativeCurlUnixSocketAsksOnceForTheService(t *testing.T) {
	path, hits := serveDaemonSocket(t)
	link := filepath.Join(filepath.Dir(path), "linked.sock")
	testutil.FailErr(t, "link daemon socket", os.Symlink(path, link))
	program := func(socket string) string {
		return "curl -s --max-time 5 --unix-socket '" + socket + "' http://localhost/version"
	}
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"operand", map[string]any{"command": program(path)}},
		{"declared", map[string]any{"command": program(path), "capability_request": map[string]any{"socket_paths": []any{path}}}},
		{"linked", map[string]any{"command": program(link)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := hits.Load()
			s := newCommandProgramSessionWithArgs(t, tc.args, wire.CreateSessionRequest{}, "")
			cards := runCommandWithApprovals(t, s, "recommended")
			if kind := cards[0].ToolApproval.Plan.Subject.Kind; kind != wire.ApprovalSubjectKindSocketSet {
				t.Fatalf("approval subject = %q, want the local service socket: %+v", kind, cards[0].ToolApproval.Plan)
			}
			outcome := s.outcome(t, "program-1")
			if outcome.Status != 0 || outcome.Stdout != "daemon /version" || hits.Load() != before+1 {
				t.Fatalf("curl did not reach the daemon once: requests=%d outcome=%+v", hits.Load()-before, outcome)
			}
		})
	}
}
