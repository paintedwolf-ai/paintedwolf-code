package httpaction

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// serveUnixSocket serves handler on a short socket path and returns it.
func serveUnixSocket(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "lyc-http-") //nolint:usetesting // Socket paths must stay short.
	testutil.FailErr(t, "mkdir socket dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", path)
	testutil.FailErr(t, "listen unix", err)
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return path
}

// reviewedSocket is the context the executor hands a call whose socket a
// person approved for the project.
func reviewedSocket(t *testing.T, root, path string) tools.ToolContext {
	t.Helper()
	grant, err := confine.ResolveSocketRequest(path)
	testutil.FailErr(t, "resolve socket", err)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(filepath.Dir(path), "state"))
	owner, err := socketref.Capture(grant.ApprovedPath)
	testutil.FailErr(t, "retain reviewed socket", err)
	t.Cleanup(owner.Close)
	grant.Binding = owner.Binding()
	tctx := sessionContext(root, "call-sock")
	tctx.Socket.SocketGrants = []confine.SocketGrant{grant}
	tctx.Socket.DurableSocketGrants = []confine.SocketGrant{grant}
	tctx.Effects.Out = &tools.ToolInvocationOut{}
	return tctx
}

func TestHTTPRequestUnixSocketRequiresReviewedAuthority(t *testing.T) {
	hit := false
	path := serveUnixSocket(t, func(w http.ResponseWriter, _ *http.Request) { hit = true })
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": "http://localhost/info", "unix_socket": path,
	}, sessionContext(t.TempDir(), "call-sock"))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != isolation.CodeSocketPermitRequired {
		t.Fatalf("err = %v, want %s", err, isolation.CodeSocketPermitRequired)
	}
	if hit {
		t.Fatal("an unreviewed socket was dialed")
	}
}

func TestHTTPRequestUnixSocketRefusesRedirectToAnotherOrigin(t *testing.T) {
	path := serveUnixSocket(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/x", http.StatusFound)
	})
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": "http://localhost/start", "unix_socket": path, "redirects": "safe",
	}, reviewedSocket(t, t.TempDir(), path))
	reject := toolrejection.AsToolReject(err)
	if reject == nil || reject.Code != "HTTP_REQUEST_FAILED" {
		t.Fatalf("err = %v, want a refused cross-origin redirect", err)
	}
}

func TestHTTPRequestKernelPathnameGrantCannotAuthorizeNativeDial(t *testing.T) {
	path := serveUnixSocket(t, func(http.ResponseWriter, *http.Request) { t.Error("kernel-pathname grant reached a native listener") })
	tc := reviewedSocket(t, t.TempDir(), path)
	legacy := tc.Socket.SocketGrants[0]
	legacy.Binding = socketref.Binding{Enforcement: socketref.KernelPathname}
	tc.Socket.SocketGrants = []confine.SocketGrant{legacy}
	tc.Socket.DurableSocketGrants = []confine.SocketGrant{legacy}
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{"url": "http://service.invalid/", "unix_socket": path}, tc)
	reject := toolrejection.AsToolReject(err)
	if reject == nil || (reject.Code != isolation.CodeSocketPathChanged && reject.Code != isolation.CodeSocketPathRefused) {
		t.Fatalf("pathname native authority = %v", err)
	}
}
