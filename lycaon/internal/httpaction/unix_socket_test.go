package httpaction

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/isolation"
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
	tctx := sessionContext(root, "call-sock")
	tctx.SocketGrants = []confine.SocketGrant{grant}
	tctx.DurableSocketGrants = []confine.SocketGrant{grant}
	tctx.Out = &tools.ToolInvocationOut{}
	return tctx
}

func TestHTTPRequestUnixSocketRequiresReviewedAuthority(t *testing.T) {
	hit := false
	path := serveUnixSocket(t, func(w http.ResponseWriter, _ *http.Request) { hit = true })
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": "http://localhost/info", "unix_socket": path,
	}, sessionContext(t.TempDir(), "call-sock"))
	reject := tools.AsToolReject(err)
	if reject == nil || reject.Code != isolation.CodeSocketPathChanged {
		t.Fatalf("err = %v, want %s", err, isolation.CodeSocketPathChanged)
	}
	if hit {
		t.Fatal("an unreviewed socket was dialed")
	}
}

func TestHTTPRequestUnixSocketIsTheRecordedDestination(t *testing.T) {
	var seen string
	path := serveUnixSocket(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Host + r.URL.Path
		_, _ = w.Write([]byte("daemon"))
	})
	tctx := reviewedSocket(t, t.TempDir(), path)
	got, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": "http://docker.invalid/v1.41/info", "unix_socket": path,
	}, tctx)
	testutil.FailErr(t, "socket request", err)
	if got.Status != http.StatusOK || got.Body != "daemon" || seen != "docker.invalid/v1.41/info" {
		t.Fatalf("got = %+v seen %q", got, seen)
	}
	want := tctx.SocketGrants[0].ResolvedPath
	if got.UnixSocket != want {
		t.Fatalf("unix_socket = %q, want the reviewed socket %q", got.UnixSocket, want)
	}
	if tctx.Out.RetrievedFrom != "unix:"+want || strings.Contains(tctx.Out.RetrievedFrom, "docker.invalid") {
		t.Fatalf("retrieved from = %q, want the socket rather than the URL host", tctx.Out.RetrievedFrom)
	}
}

func TestHTTPRequestUnixSocketRefusesRedirectToAnotherOrigin(t *testing.T) {
	path := serveUnixSocket(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://elsewhere.invalid/x", http.StatusFound)
	})
	_, err := runRequest(t, Deps{Boundary: testBoundary()}, map[string]any{
		"url": "http://localhost/start", "unix_socket": path, "redirects": "safe",
	}, reviewedSocket(t, t.TempDir(), path))
	reject := tools.AsToolReject(err)
	if reject == nil || reject.Code != "HTTP_REQUEST_FAILED" || !strings.Contains(reject.Data["reason"].(string), "unix socket") {
		t.Fatalf("err = %v, want a refused cross-origin redirect", err)
	}
}
