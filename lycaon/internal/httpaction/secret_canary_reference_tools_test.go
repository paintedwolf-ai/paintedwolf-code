package httpaction

import (
	"context"
	"encoding/json"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/config"
	"github.com/lycaon/lycaon/config/configtest"
	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/captureprojection"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/mcp"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// echoScript prints the value it receives in every form the canary checks and
// records what it received, so a clean result proves redaction, not a miss.
const echoScript = `#!/bin/sh
v="${TOKEN:-$1}"
printf %s "$v" > "${SEEN:-seen.txt}"
printf 'plain=%s\n' "$v"
printf 'b64=%s\n' "$(printf %s "$v" | base64)"
printf 'b64url=%s\n' "$(printf %s "$v" | base64 | tr '+/' '-_')"
printf 'basic=%s\n' "$(printf 'user:%s' "$v" | base64)"
printf 'query=%s\n' "$(printf %s "$v" | sed -e 's|%|%25|g' -e 's|/|%2F|g' -e 's|+|%2B|g' -e 's|=|%3D|g' -e 's|&|%26|g' -e 's|?|%3F|g' -e 's|#|%23|g' -e 's| |+|g')"
printf 'path=%s\n' "$(printf %s "$v" | sed -e 's|%|%25|g' -e 's|/|%2F|g' -e 's|?|%3F|g' -e 's|#|%23|g' -e 's| |%20|g')"
printf 'json={"token":"%s"}\n' "$(printf %s "$v" | sed -e 's|\\|\\\\|g' -e 's|"|\\"|g' -e 's|&|\\u0026|g')"
`

// echoForms renders every canary form into one body, as a server or MCP
// provider that reflects its credential would.
func echoForms(value string) string {
	var lines []string
	for name, form := range canaryForms(value) {
		lines = append(lines, name+"="+form)
	}
	encoded, _ := json.Marshal(map[string]string{"token": value})
	return strings.Join(append(lines, string(encoded)), "\n")
}

// canaryCall is one invocation that hands the resolved canary to a consumer
// which echoes it back; received reports what the consumer saw.
type canaryCall struct {
	scheme   string
	args     map[string]any
	received func() string
}

// canaryEchoConnector is an MCP provider that reflects its arguments.
type canaryEchoConnector struct{ received *[]string }

func (c canaryEchoConnector) Connect(context.Context, mcp.MCPProviderEntry, mcp.ConnectOpts) (mcp.ProviderSession, error) {
	return canaryEchoSession(c), nil
}

type canaryEchoSession struct{ received *[]string }

func (s canaryEchoSession) ListTools(context.Context) ([]*sdkmcp.Tool, error) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"token": map[string]any{"type": "string"}}}
	return []*sdkmcp.Tool{
		{Name: "echo", Description: "echo", InputSchema: schema},
		{Name: "reject", Description: "reject", InputSchema: schema},
	}, nil
}

func (s canaryEchoSession) CallTool(_ context.Context, name string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	token, _ := args["token"].(string)
	*s.received = append(*s.received, token)
	return &sdkmcp.CallToolResult{
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: echoForms(token)}},
		IsError: name == "reject",
	}, nil
}

func (canaryEchoSession) Close() error { return nil }

// Every tool whose contract resolves managed-secret references returns a
// result free of the resolved value, whatever its consumer echoes back.
func TestSecretCanaryNeverEchoedByAnyReferenceTool(t *testing.T) {
	service, _, _ := newTestSecrets(t)
	meta := hostSecret(t, service, "canary", "Canary", canaryValue)
	root := t.TempDir()
	script := filepath.Join(root, "echo.sh")
	testutil.FailErr(t, "write echo script", os.WriteFile(script, []byte(echoScript), 0o755))
	seen := func(name string) func() string {
		return func() string {
			body, _ := os.ReadFile(filepath.Join(root, name))
			return string(body)
		}
	}

	var wire []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		wire = append(wire, token)
		_, _ = w.Write([]byte(echoForms(token)))
	}))
	defer server.Close()

	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open approvals", err)
	authority := settings.NewRuleApprovalGate(store, settings.NoSources())
	review := &canaryReview{t: t, authority: authority}
	registry := tools.NewDefaultRegistry()
	executor := toolexecution.NewExecutor(nil, registry, "implement")
	executor.Approvals.SetCheckpointManager(review, authority)
	t.Cleanup(func() { confine.SetEgressResolver(nil) })
	executor.Secrets.SetSecretResolver(service)
	matcher := testSecretMatcher(t)
	executor.Secrets.SetSecretMatcher(matcher)
	executor.Capabilities.SetLoopbackConnectGate(review)
	executor.Boundary.SetSessionLoopbackGrant(review.SessionLoopbackGrant)

	testutil.FailErr(t, "register http_request", Register(registry, Deps{
		Boundary: testBoundary(), SecretMatcher: matcher, SecretAsk: executor.AskSecretScreen, Secrets: service,
	}))
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "load tool profiles", err)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, profiles)
	background := bgprocess.NewRegistry(bgprocess.DefaultConfig(), bgprocess.Hooks{})
	background.Output.SetCaptureProjector(captureprojection.New(secretmatch.NewInertMatcher(), nil))
	runner := hostcmd.NewRunner()
	command := &command.CommandTool{Runner: runner, Boundary: boundary, Background: background}
	verify := &native.VerifyTool{Runner: runner, Boundary: boundary, Background: background}
	testutil.FailErr(t, "register command", registry.Register("command", command.Run))
	testutil.FailErr(t, "register verify", registry.Register("verify", verify.Run))
	testutil.FailErr(t, "register terminal tools", native.RegisterTerminalSessionTools(registry, background))

	configtest.Overlay(t, map[config.Rel]string{config.DistroMCP: "providers:\n  - id: canary\n    url: http://127.0.0.1:9/mcp\n    enabled: false\n"})
	global := filepath.Join(t.TempDir(), "mcp.yaml")
	testutil.FailErr(t, "write mcp config", os.WriteFile(global, []byte("providers:\n  - id: canary\n    url: http://127.0.0.1:8765/mcp\n    enabled: true\n"), 0o600))
	var received []string
	providers, err := mcp.NewRuntime(mcp.RuntimeOptions{
		StatePath: t.TempDir(), GlobalOverridePath: global, Connector: canaryEchoConnector{received: &received},
	})
	testutil.FailErr(t, "open mcp registry", err)
	providers.Tools.SetToolRegistry(registry)
	providers.Calls.SetSecretScreen(matcher, executor.AskSecretScreen)
	testutil.FailErr(t, "load mcp providers", providers.Catalog.Load(t.Context()))
	mcpReceived := func() string {
		if len(received) == 0 {
			return ""
		}
		return received[len(received)-1]
	}

	terminal := ""
	harnesses := map[string][]canaryCall{
		"command": {
			{scheme: "argument", args: map[string]any{"command": "sh echo.sh " + meta.Reference, "env": map[string]any{"SEEN": "seen-command-arg.txt"}}, received: seen("seen-command-arg.txt")},
			{scheme: "environment", args: map[string]any{"command": "sh echo.sh", "env": map[string]any{"TOKEN": meta.Reference, "SEEN": "seen-command-env.txt"}}, received: seen("seen-command-env.txt")},
		},
		"verify": {
			{scheme: "environment", args: map[string]any{"command": "sh echo.sh", "env": map[string]any{"TOKEN": meta.Reference, "SEEN": "seen-verify.txt"}}, received: seen("seen-verify.txt")},
		},
		"terminal_open": {
			{scheme: "environment", args: map[string]any{"command": "sh echo.sh", "env": map[string]any{"TOKEN": meta.Reference, "SEEN": "seen-terminal-open.txt"}}, received: seen("seen-terminal-open.txt")},
		},
		"terminal_send": {
			{scheme: "input", args: map[string]any{"input": "SEEN=seen-terminal-send.txt sh echo.sh '" + meta.Reference + "'{Enter}"}, received: seen("seen-terminal-send.txt")},
		},
		"http_request": {
			{scheme: "bearer", args: map[string]any{
				"url": server.URL + "/echo", "auth": map[string]any{"scheme": "bearer", "token": meta.Reference},
				"capability_request": loopbackCapability(t, server.URL),
			}, received: func() string {
				if len(wire) == 0 {
					return ""
				}
				return wire[len(wire)-1]
			}},
		},
		mcp.QualifiedToolName("canary", "echo"): {
			{scheme: "result", args: map[string]any{"token": meta.Reference}, received: mcpReceived},
		},
		mcp.QualifiedToolName("canary", "reject"): {
			{scheme: "error result", args: map[string]any{"token": meta.Reference}, received: mcpReceived},
		},
	}

	declared := toolcontract.SecretReferenceTools()
	for _, name := range []string{mcp.QualifiedToolName("canary", "echo"), mcp.QualifiedToolName("canary", "reject")} {
		definition, ok := registry.Definition(name)
		if !ok || !definition.Contract.AcceptsSecretReferences() {
			t.Fatalf("%s: MCP tools must declare a secret reference surface", name)
		}
		declared = append(declared, name)
	}
	for _, tool := range declared {
		if contract, ok := toolcontract.Lookup(tool); ok && contract.SecretReferenceSurface.IsFile() {
			continue
		}
		calls, ok := harnesses[tool]
		if !ok {
			t.Errorf("%s resolves managed-secret references but has no canary harness", tool)
			continue
		}
		for i, call := range calls {
			t.Run(tool+"/"+call.scheme, func(t *testing.T) {
				tctx := sessionContext(root, strings.ReplaceAll(tool+"-"+call.scheme, " ", "-"))
				tctx.Identity.ToolCallID += "-" + string(rune('a'+i))
				if tool == "terminal_send" {
					terminal = openCanaryShell(t, executor, root)
					call.args["id"] = terminal
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				out, err := executor.Invoke(ctx, tool, call.args, tctx)
				if err != nil && toolrejection.AsToolReject(err) == nil && !strings.HasPrefix(tool, "mcp_") {
					t.Fatalf("%s (%s): %v", tool, call.scheme, err)
				}
				if got := waitReceived(call.received); got != canaryValue {
					t.Fatalf("%s (%s): the consumer did not receive the resolved canary: %q (result %s, err %v)", tool, call.scheme, got, out, err)
				}
				assertNoCanary(t, tool, call.scheme, out)
				echoed := out
				if err != nil {
					assertNoCanary(t, tool, call.scheme+" error", err.Error())
					echoed += err.Error()
				}
				if reject := toolrejection.AsToolReject(err); reject != nil {
					data, marshalErr := json.Marshal(reject.Data)
					testutil.FailErr(t, "encode reject data", marshalErr)
					assertNoCanary(t, tool, call.scheme+" reject data", string(data))
					echoed += string(data)
				}
				if !strings.Contains(echoed, meta.Reference) {
					t.Errorf("%s (%s): the echo was dropped rather than written as its reference: %s", tool, call.scheme, echoed)
				}
			})
		}
	}
}

// openCanaryShell starts an interactive shell for terminal_send.
func openCanaryShell(t *testing.T, executor *toolexecution.Executor, root string) string {
	t.Helper()
	out, err := executor.Invoke(t.Context(), "terminal_open", map[string]any{"command": "sh"}, sessionContext(root, "terminal-shell"))
	testutil.FailErr(t, "open canary shell", err)
	var opened struct {
		ID string `json:"id"`
	}
	testutil.FailErr(t, "decode canary shell", json.Unmarshal([]byte(out), &opened))
	t.Cleanup(func() {
		_, _ = executor.Invoke(context.Background(), "terminal_close", map[string]any{"id": opened.ID}, sessionContext(root, "terminal-close"))
	})
	return opened.ID
}

// waitReceived lets a pty-driven consumer finish writing what it saw.
func waitReceived(received func() string) string {
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got := received(); got == canaryValue || time.Now().After(deadline) {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
}
