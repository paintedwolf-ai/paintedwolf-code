package mcp

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPrepareStdioCommandRefusesUnsafeProjectRoot(t *testing.T) {
	store := t.TempDir()
	previous := confine.CredentialStorePaths()
	confine.SetCredentialStorePathsSource(func() []string { return []string{store} })
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(func() []string { return previous }) })
	alias := filepath.Join(t.TempDir(), "credential-alias")
	testutil.FailErr(t, "create credential alias", os.Symlink(store, alias))
	for _, root := range []struct{ name, path, code string }{
		{"credential-store", store, confine.WriteRootCodeSecretStore},
		{"credential-alias", alias, confine.WriteRootCodeSecretStore},
		{"relative", "relative-project", confine.WriteRootCodeNotAbsolute},
	} {
		t.Run(root.name, func(t *testing.T) {
			spawn, err := prepareStdioCommand(
				context.Background(), "fixture", "/bin/echo", nil, stdioEnv{}, []string{root.path},
			)
			var refusal *confine.WriteRootRefusalError
			if !errors.As(err, &refusal) || refusal.Code != root.code || refusal.Path != root.path {
				t.Fatalf("prepare error = %v, want typed refusal %s for %s", err, root.code, root.path)
			}
			if spawn.cmd != nil || spawn.cleanup != nil || spawn.releaseEgress != nil {
				t.Fatal("refused root prepared a command or acquired spawn resources")
			}
		})
	}
}

func TestPrepareStdioCommandConfinedWhenAvailable(t *testing.T) {
	if !confine.Available() {
		t.Skip("Seatbelt confinement only on darwin")
	}
	root := t.TempDir()
	spawn, err := prepareStdioCommand(context.Background(), "fixture", "/bin/echo", []string{"hi"}, stdioEnv{base: []string{"PATH=/usr/bin"}}, []string{root})
	testutil.FailErr(t, "prepare", err)
	defer spawn.cleanup()
	defer spawn.releaseEgress()
	if !confine.IsHelperInvocation(spawn.cmd.Args) {
		t.Fatalf("expected confine helper argv, got %v", spawn.cmd.Args)
	}
}

// A confined MCP server's connections are attributed through its lineage. The
// broker must hold that lease while the server runs and drop it on release.
func TestRegisterStdioEgressAttributesTheServerLineage(t *testing.T) {
	registered := map[string]confine.EgressCommand{}
	restore := bindEgress
	bindEgress = func(_ context.Context, c *confine.Confinement, cmd confine.EgressCommand) (func(), error) {
		c.LineageID = "lineage-1"
		registered[c.LineageID] = cmd
		return func() { delete(registered, c.LineageID) }, nil
	}
	t.Cleanup(func() { bindEgress = restore })

	release, err := bindStdioEgress(t.Context(), "fixture", &confine.Confinement{ProxyAddr: "127.0.0.1:1"})
	testutil.FailErr(t, "bind", err)
	cmd, ok := registered["lineage-1"]
	if !ok {
		t.Fatal("the MCP server's lineage was never registered with the egress broker")
	}
	if cmd.ToolCallID != "mcp:fixture" {
		t.Fatalf("attribution = %+v want the MCP server identity", cmd)
	}
	release()
	if _, still := registered["lineage-1"]; still {
		t.Fatal("lineage still registered after the session released it")
	}
}

// The environment the server receives carries an address, never a credential:
// a secret here would change between invocations and be readable by every
// descendant.
func TestPrepareStdioCommandHandsTheServerNoCredential(t *testing.T) {
	if !confine.Available() {
		t.Skip("Seatbelt confinement only on darwin")
	}
	restore := bindEgress
	bindEgress = func(context.Context, *confine.Confinement, confine.EgressCommand) (func(), error) {
		return func() {}, nil
	}
	t.Cleanup(func() { bindEgress = restore })

	root := t.TempDir()
	spawn, err := prepareStdioCommand(context.Background(), "fixture", "/bin/echo", nil, stdioEnv{}, []string{root})
	testutil.FailErr(t, "prepare", err)
	defer spawn.cleanup()

	for _, kv := range spawn.cmd.Env {
		value, ok := strings.CutPrefix(kv, "HTTPS_PROXY=")
		if !ok {
			continue
		}
		u, err := url.Parse(value)
		testutil.FailErr(t, "parse proxy url", err)
		if u.User != nil {
			t.Fatalf("proxy environment carries a credential: %s", kv)
		}
	}
}

func TestPrepareStdioCommandRequiresRootsWhenAvailable(t *testing.T) {
	if !confine.Available() {
		t.Skip("Seatbelt confinement only on darwin")
	}
	_, err := prepareStdioCommand(context.Background(), "fixture", "/bin/echo", nil, stdioEnv{}, nil)
	if err == nil {
		t.Fatal("expected error when roots empty")
	}
}

// Servers on protocol 2025-11-25 can still list roots; 2026-07-28 removed
// server-initiated requests.
func TestAdvertiseRootsAddsFileURIs(t *testing.T) {
	root := t.TempDir()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test", Version: "0"}, nil)
	advertiseRoots(client, []string{root})

	saw := make(chan []string, 1)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "fixture", Version: "0"}, &sdkmcp.ServerOptions{
		SupportedProtocolVersions: []string{legacyRootsProtocol},
	})
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "list_roots", Description: "list"}, func(ctx context.Context, req *sdkmcp.CallToolRequest, _ map[string]any) (*sdkmcp.CallToolResult, any, error) {
		res, err := req.Session.ListRoots(ctx, nil) //nolint:staticcheck // roots stay functional through the SEP-2577 deprecation window
		if err != nil {
			return nil, nil, err
		}
		uris := make([]string, 0, len(res.Roots))
		for _, r := range res.Roots {
			uris = append(uris, r.URI)
		}
		saw <- uris
		return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "ok"}}}, nil, nil
	})

	t1, t2 := sdkmcp.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), t1, nil)
	testutil.FailErr(t, "server connect", err)
	t.Cleanup(func() { _ = ss.Close() })

	cs, err := client.Connect(context.Background(), t2, nil)
	testutil.FailErr(t, "client connect", err)
	t.Cleanup(func() { _ = cs.Close() })

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: "list_roots"})
	testutil.FailErr(t, "call", err)
	if res.IsError {
		t.Fatalf("list_roots failed: %v", res.Content)
	}
	uris := <-saw
	want := fileRootURI(mustAbs(t, root))
	for _, u := range uris {
		if u == want {
			return
		}
	}
	t.Fatalf("roots = %v want %s", uris, want)
}

func TestConnectStdioFakeServerSeesRoots(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX", "off")
	root := t.TempDir()
	bin := buildFakeStdioServer(t)
	conn := SDKConnector{}
	sess, err := conn.Connect(context.Background(), MCPProviderEntry{
		ID:      "fixture",
		Command: bin,
		Args:    legacyRootsFixtureArgs,
		Enabled: true,
	}, ConnectOpts{Roots: []string{root}})
	testutil.FailErr(t, "connect", err)
	t.Cleanup(func() { _ = sess.Close() })

	res, err := sess.CallTool(context.Background(), "list_roots", nil)
	testutil.FailErr(t, "list_roots", err)
	text := ExtractToolResultText(res)
	want := fileRootURI(mustAbs(t, root))
	if !strings.Contains(text, want) {
		t.Fatalf("result %q missing root %s", text, want)
	}
}

// legacyRootsProtocol is the newest protocol on which a server may list roots.
const legacyRootsProtocol = "2025-11-25"

// legacyRootsFixtureArgs pins the fake stdio server to that protocol.
var legacyRootsFixtureArgs = []string{"-protocol", legacyRootsProtocol}

func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	testutil.FailErr(t, "abs", err)
	return abs
}

func buildFakeStdioServer(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fake_stdio_server")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./testdata/fake_stdio_server")
	pkgDir, err := os.Getwd()
	testutil.FailErr(t, "cwd", err)
	cmd.Dir = pkgDir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go build fake server: %v", err)
	}
	return out
}
