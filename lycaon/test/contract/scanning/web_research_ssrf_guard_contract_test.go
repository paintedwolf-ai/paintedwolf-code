package contract

import (
	"errors"
	"go/ast"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/egress"
	"github.com/lycaon/lycaon/internal/outboundhttp"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// directHTTPClientNames are net/http entry points that dial without the
// outboundhttp destination policy.
var directHTTPClientNames = map[string]bool{
	"Get": true, "Head": true, "Post": true, "PostForm": true,
	"NewRequest": true, "NewRequestWithContext": true,
	"DefaultClient": true, "DefaultTransport": true,
}

// directHTTPClientTypes are net/http types a file may name but not construct.
var directHTTPClientTypes = map[string]bool{"Client": true, "Transport": true}

// Web research reaches pages only through outboundhttp: no production file in
// the package dials through net/http directly or builds its own client.
func TestPageFetchPathsUseGuardedTransport(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "webresearch")
	fset, files := contractcheck.ParseNonTestGoTree(t, dir)
	if len(files) == 0 {
		t.Fatalf("no production files under %s", dir)
	}
	importsOutbound := false
	for _, file := range files {
		path := fset.Position(file.Pos()).Filename
		if contractcheck.ImportAliasFor(file, "github.com/lycaon/lycaon/internal/outboundhttp") != "" {
			importsOutbound = true
		}
		alias := contractcheck.ImportAliasFor(file, "net/http")
		if alias == "" {
			continue
		}
		isHTTP := func(expr ast.Expr, names map[string]bool) bool {
			sel, ok := expr.(*ast.SelectorExpr)
			if !ok {
				return false
			}
			pkg, ok := sel.X.(*ast.Ident)
			return ok && pkg.Name == alias && names[sel.Sel.Name]
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SelectorExpr:
				if isHTTP(node, directHTTPClientNames) {
					t.Errorf("%s uses %s.%s; page and provider fetches go through outboundhttp", path, alias, node.Sel.Name)
				}
			case *ast.CompositeLit:
				if isHTTP(node.Type, directHTTPClientTypes) {
					t.Errorf("%s:%d constructs a net/http client; page and provider fetches go through outboundhttp", path, fset.Position(node.Pos()).Line)
				}
			}
			return true
		})
	}
	if !importsOutbound {
		t.Fatal("no web research file reaches outboundhttp")
	}
}

// The guarded transport refuses loopback and link-local destinations before
// any request reaches them; local authority admits the same loopback server.
func TestOutboundHTTPRefusesPrivateDestinations(t *testing.T) {
	t.Parallel()
	var served atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	port := strconv.Itoa(server.Listener.Addr().(*net.TCPAddr).Port)

	for _, target := range []string{server.URL, "http://169.254.169.254:" + port + "/latest/meta-data"} {
		_, err := outboundhttp.Do(t.Context(), outboundhttp.Request{Method: http.MethodGet, URL: target})
		var denied *egress.DestinationDeniedError
		if !errors.As(err, &denied) {
			t.Errorf("Do(%s) error = %v, want a destination denial", target, err)
		}
	}
	if got := served.Load(); got != 0 {
		t.Fatalf("refused destinations reached the server %d times", got)
	}

	allowLoopback := func(addr netip.Addr, _ uint16) bool { return addr.IsLoopback() }
	response, err := outboundhttp.Do(t.Context(), outboundhttp.Request{Method: http.MethodGet, URL: server.URL, AllowAddress: allowLoopback})
	contractcheck.FailErr(t, "loopback with local authority", err)
	if response.Status != http.StatusNoContent || served.Load() != 1 {
		t.Fatalf("admitted loopback: status %d, served %d; want 204 once", response.Status, served.Load())
	}
}
