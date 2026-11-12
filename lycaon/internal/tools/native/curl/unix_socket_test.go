//go:build darwin || linux

package curl

import (
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

func serveSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pw-curl-sock-") //nolint:usetesting // Unix socket addresses have a kernel byte limit.
	testutil.FailErr(t, "create socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen", err)
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "daemon "+r.Host+r.URL.Path)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return path
}

// The socket is the request's destination: it is reviewed as the operation's
// socket argument, the URL host derives no loopback port, and no file target
// names it.
func TestCurlUnixSocketIsReviewedAsTheDestination(t *testing.T) {
	h := newHarness(t)
	for operand, want := range map[string]string{
		"/run/daemon.sock": "/run/daemon.sock",
		"daemon.sock":      filepath.Join(h.program.Paths.Cwd().Abs, "daemon.sock"),
	} {
		prepared, _, err := h.prepare("--unix-socket", operand, "http://localhost/version")
		testutil.FailErr(t, "prepare "+operand, err)
		if len(prepared.LoopbackPorts) != 0 {
			t.Errorf("%s: loopback ports %v for a socket request", operand, prepared.LoopbackPorts)
		}
		for _, target := range prepared.Review.PathTargets {
			if filepath.Base(target.Path) == "daemon.sock" {
				t.Errorf("%s: socket listed as a file target", operand)
			}
		}
		if got := prepared.Review.Arguments()["unix_socket"]; got != want {
			t.Errorf("%s: reviewed unix_socket = %v, want %s", operand, got, want)
		}
	}
	if prepared, _, err := h.prepare("http://localhost/version"); err != nil || len(prepared.LoopbackPorts) != 1 {
		t.Fatalf("a loopback URL without a socket lost its port: %v %v", prepared.LoopbackPorts, err)
	}
}

func TestCurlUnixSocketReachesItsReviewedService(t *testing.T) {
	h := newHarness(t)
	path := serveSocket(t)
	refused := h.curl(t.Context(), "-s", "--unix-socket", path, "http://localhost/version")
	if reject := toolrejection.AsToolReject(refused.err); reject == nil || reject.Code != isolation.CodeSocketPermitRequired {
		t.Fatalf("unreviewed socket: %v", refused.err)
	}
	// The executor retains a reviewed socket argument as an object, as here.
	ctx, scope := socketref.WithScope(t.Context())
	t.Cleanup(scope.Close)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(filepath.Dir(path), "state"))
	grant, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "resolve socket", err)
	grant.Binding.Enforcement = socketref.RetainedObject
	grant, err = confine.CaptureSocketGrant(ctx, grant)
	testutil.FailErr(t, "retain reviewed socket", err)
	h.tc.Socket.SocketGrants = []confine.SocketGrant{grant}
	r := h.curl(ctx, "-s", "--max-time", "5", "--unix-socket", path, "http://localhost/version")
	testutil.FailErr(t, "reviewed socket request", r.err)
	if r.result.Status != 0 || r.stdout != "daemon localhost/version" {
		t.Fatalf("socket request = %+v stdout %q", r.result, r.stdout)
	}
}
