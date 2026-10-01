package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/internal/confine"
	execpkg "github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
	"github.com/lycaon/lycaon/internal/version"
)

// defaultHTTPClientTimeout bounds one MCP HTTP round-trip.
const defaultHTTPClientTimeout = 120 * time.Second

// ProviderSession is a live MCP provider connection.
type ProviderSession interface {
	ListTools(ctx context.Context) ([]*sdkmcp.Tool, error)
	CallTool(ctx context.Context, name string, args map[string]any) (*sdkmcp.CallToolResult, error)
	Close() error
}

// ConnectOpts carries spawn-time envelope inputs for a SessionConnector.
type ConnectOpts struct {
	// ExtraEnv carries host-minted values for a local stdio child. They are
	// delivered past the inherited-environment filter, so only the host may
	// populate this — see stdioEnv.host.
	ExtraEnv []string
	// Roots advertise the MCP roots capability and confine local stdio processes.
	Roots []string
	// Lifetime bounds a local stdio process. Nil leaves Close as the bound.
	Lifetime context.Context
	// OnToolListChanged fires when the server sends notifications/tools/list_changed.
	// Nil ignores the notification.
	OnToolListChanged func()
	// OnHTTPStatus reports each observed HTTP response status.
	OnHTTPStatus func(code int)
}

// SessionConnector dials an MCP server (stdio subprocess or streamable HTTP).
type SessionConnector interface {
	Connect(ctx context.Context, entry MCPProviderEntry, opts ConnectOpts) (ProviderSession, error)
}

// SDKConnector connects stdio and streamable HTTP transports.
type SDKConnector struct {
	HTTPClient *http.Client
	// AccessToken, when set, supplies an OAuth bearer for HTTP MCP when the entry
	// has no static Authorization header.
	AccessToken func(providerID string) string
}

func (c SDKConnector) Connect(ctx context.Context, entry MCPProviderEntry, opts ConnectOpts) (ProviderSession, error) {
	if err := entry.Validate(); err != nil {
		return nil, err
	}
	transport, err := entry.Transport()
	if err != nil {
		return nil, err
	}
	switch transport {
	case TransportHTTP:
		return c.connectHTTP(ctx, entry, opts)
	case TransportStdio:
		return c.connectStdio(ctx, entry, opts)
	default:
		return nil, fmt.Errorf("mcp provider %q: unsupported transport %q", entry.ID, transport)
	}
}

func (c SDKConnector) connectHTTP(ctx context.Context, entry MCPProviderEntry, opts ConnectOpts) (ProviderSession, error) {
	headers := HTTPAuthHeaders(entry)
	oauthBearer := ""
	if headers.Get("Authorization") == "" && c.AccessToken != nil {
		oauthBearer = strings.TrimSpace(c.AccessToken(entry.ID))
	}
	authHost := ""
	if u, err := url.Parse(strings.TrimSpace(entry.URL)); err == nil {
		authHost = u.Hostname()
	}
	client := httpClientWithAuth(c.HTTPClient, headers, oauthBearer, authHost, opts.OnHTTPStatus)
	sdkClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "lycaon", Version: version.Version}, clientOptions(opts))
	advertiseRoots(sdkClient, opts.Roots)
	transport := &sdkmcp.StreamableClientTransport{
		Endpoint:   strings.TrimSpace(entry.URL),
		HTTPClient: client,
	}
	session, err := sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("connect %q: %w", entry.ID, err)
	}
	return &sdkSession{session: session}, nil
}

// clientOptions advertises tools and tool-list changes only.
func clientOptions(opts ConnectOpts) *sdkmcp.ClientOptions {
	if opts.OnToolListChanged == nil {
		return nil
	}
	notify := opts.OnToolListChanged
	return &sdkmcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *sdkmcp.ToolListChangedRequest) {
			notify()
		},
	}
}

func (c SDKConnector) connectStdio(ctx context.Context, entry MCPProviderEntry, opts ConnectOpts) (ProviderSession, error) {
	bin, args, err := ResolveDistroCommand(entry.Command, entry.Args)
	if err != nil {
		return nil, fmt.Errorf("mcp provider %q: %w", entry.ID, err)
	}
	// The stdio child outlives the connection handshake.
	lifetime := opts.Lifetime
	if lifetime == nil {
		lifetime = context.Background()
	}
	spawn, err := prepareStdioCommand(lifetime, entry.ID, bin, args, spawnEnvFor(entry.Env, opts.ExtraEnv), opts.Roots) //nolint:contextcheck // lifetime is the server-lifetime root, not the connect ctx
	if err != nil {
		return nil, fmt.Errorf("mcp provider %q: %w", entry.ID, err)
	}
	defer spawn.cleanup()

	sdkClient := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "lycaon", Version: version.Version}, clientOptions(opts))
	advertiseRoots(sdkClient, opts.Roots)
	transport := &trackedTransport{CommandTransport: &sdkmcp.CommandTransport{Command: spawn.cmd}}
	session, err := sdkClient.Connect(ctx, transport, nil)
	if err != nil {
		// Reap a child created before a failed handshake.
		spawn.releaseEgress()
		reapProcessGroup(spawn.cmd)
		transport.release()
		return nil, fmt.Errorf("connect %q: %w", entry.ID, err)
	}
	return &sdkSession{session: session, cmd: spawn.cmd, releaseEgress: spawn.releaseEgress, untrack: transport.release}, nil
}

// trackedTransport registers the server's process group the moment the SDK
// starts it, so the group dies with the engine even mid-handshake.
type trackedTransport struct {
	*sdkmcp.CommandTransport
	untrack func()
}

func (t *trackedTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	conn, err := t.CommandTransport.Connect(ctx)
	if cmd := t.Command; cmd != nil && cmd.Process != nil {
		// hardenSpawn made the server its own group leader.
		t.untrack = execpkg.TrackProcessGroup(cmd.Process.Pid)
	}
	return conn, err
}

func (t *trackedTransport) release() {
	if t.untrack != nil {
		t.untrack()
	}
}

// stdioSpawn is a prepared local MCP subprocess plus the teardown returned by its envelope.
type stdioSpawn struct {
	cmd *osexec.Cmd
	// releaseEgress unregisters the process proxy token.
	releaseEgress func()
	// cleanup releases spawn-time resources (the sandbox profile pipe) once the
	// command has been started.
	cleanup func()
}

// prepareStdioCommand builds a local MCP process under available confinement.
// lifetime spans the provider session.
func prepareStdioCommand(
	lifetime context.Context,
	providerID, bin string,
	args []string,
	env stdioEnv,
	roots []string,
) (stdioSpawn, error) {
	noop := func() {}
	if err := confine.ValidateAttachedWriteRoots(cleanRootPaths(roots)); err != nil {
		return stdioSpawn{}, fmt.Errorf("validate stdio MCP roots: %w", err)
	}
	conf, ok := stdioConfinement(roots)
	if ok {
		releaseEgress, err := bindStdioEgress(lifetime, providerID, conf)
		if err != nil {
			return stdioSpawn{}, err
		}
		cmd, cleanup, err := execpkg.PrepareCommand(lifetime, bin, args, execpkg.ExecOpts{
			Launch:    execpkg.AgentLaunch(execpkg.LaunchLocalMCP, providerID, conf),
			Env:       env.base,
			AppendEnv: env.host,
		})
		if err != nil {
			releaseEgress()
			return stdioSpawn{}, err
		}
		hardenSpawn(cmd)
		return stdioSpawn{cmd: cmd, releaseEgress: releaseEgress, cleanup: cleanup}, nil
	}
	if confine.Available() && !confine.BypassEnabled() && !confine.SandboxDisabled() && len(cleanRootPaths(roots)) == 0 {
		return stdioSpawn{}, fmt.Errorf("stdio MCP spawn requires project roots for confinement")
	}
	cmd, cleanup, err := execpkg.PrepareCommand(lifetime, bin, args, execpkg.ExecOpts{
		Launch:    execpkg.AgentLaunch(execpkg.LaunchLocalMCP, providerID, nil),
		Env:       env.base,
		AppendEnv: env.host,
	})
	if err != nil {
		return stdioSpawn{}, err
	}
	hardenSpawn(cmd)
	return stdioSpawn{cmd: cmd, releaseEgress: noop, cleanup: cleanup}, nil
}

var bindEgress = func(ctx context.Context, c *confine.Confinement, cmd confine.EgressCommand) (func(), error) {
	lease, err := confine.BindAction(c, cmd)
	if err != nil {
		return nil, err
	}
	return func() { lease.Close(ctx) }, nil
}

// bindStdioEgress attributes a confined process to its provider.
func bindStdioEgress(ctx context.Context, providerID string, conf *confine.Confinement) (func(), error) {
	release, err := bindEgress(ctx, conf, confine.EgressCommand{
		ToolCallID: "mcp:" + providerID,
	})
	if err != nil {
		return nil, err
	}
	return release, nil
}

// stdioConfinement confines local MCP whenever the host boundary is available.
func stdioConfinement(roots []string) (*confine.Confinement, bool) {
	clean := cleanRootPaths(roots)
	if confine.ValidateAttachedWriteRoots(clean) != nil {
		return nil, false
	}
	if confine.BypassEnabled() || confine.SandboxDisabled() || !confine.Available() || len(clean) == 0 {
		return nil, false
	}
	// Preserve the configured network posture.
	if c, ok := safecmd.Confine(clean); ok && c != nil {
		return c, true
	}
	c := &confine.Confinement{Roots: clean, Network: confine.NetworkDeny}
	return c, true
}

func cleanRootPaths(roots []string) []string {
	clean := make([]string, 0, len(roots))
	for _, r := range roots {
		if strings.TrimSpace(r) != "" {
			clean = append(clean, filepath.Clean(strings.TrimSpace(r)))
		}
	}
	return clean
}

// Only sessions negotiated below protocol 2026-07-28 can list these roots;
// later protocols dropped server-initiated requests.
func advertiseRoots(client *sdkmcp.Client, roots []string) {
	if client == nil {
		return
	}
	var list []*sdkmcp.Root //nolint:staticcheck // roots stay functional through the SEP-2577 deprecation window
	for i, path := range roots {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		name := filepath.Base(abs)
		if i == 0 {
			name = "primary"
		}
		list = append(list, &sdkmcp.Root{ //nolint:staticcheck // roots stay functional through the SEP-2577 deprecation window
			URI:  fileRootURI(abs),
			Name: name,
		})
	}
	if len(list) > 0 {
		client.AddRoots(list...) //nolint:staticcheck // roots stay functional through the SEP-2577 deprecation window
	}
}

func fileRootURI(abs string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}

// stdioEnv separates the two environment channels of a local MCP child.
type stdioEnv struct {
	// base is process plumbing plus the operator-declared env map. It crosses the
	// standard inherited-environment filter, so a declared value cannot smuggle a
	// loader-hijack or hook variable into the child.
	base []string
	// host carries host-minted values that must survive that filter — the @self
	// API token, whose key SanitizeEnviron blocks by name.
	host []string
}

// spawnEnvFor builds the environment of a local stdio MCP child: the reduced
// environment (PATH, HOME, temp, locale) plus the operator-declared catalog `env`
// map. The sidecar's ambient credentials never reach third-party server code.
func spawnEnvFor(entryEnv map[string]string, hostEnv []string) stdioEnv {
	reduced := execpkg.ReducedEnviron()
	base := make([]string, 0, len(reduced)+len(entryEnv))
	base = append(base, reduced...)
	for k, v := range entryEnv {
		base = append(base, k+"="+v)
	}
	return stdioEnv{base: base, host: hostEnv}
}

type sdkSession struct {
	session *sdkmcp.ClientSession
	// cmd retains a local process so Close can reap its process group.
	cmd *osexec.Cmd
	// releaseEgress drops the confined server's proxy-token registration. Nil for
	// HTTP and unconfined stdio sessions.
	releaseEgress func()
	// untrack releases a local server's group from the engine-exit reaper once reaped.
	untrack func()
}

func (s *sdkSession) ListTools(ctx context.Context) ([]*sdkmcp.Tool, error) {
	res, err := s.session.ListTools(ctx, &sdkmcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}

func (s *sdkSession) CallTool(ctx context.Context, name string, args map[string]any) (*sdkmcp.CallToolResult, error) {
	return s.session.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
}

func (s *sdkSession) Close() error {
	err := s.session.Close()
	reapProcessGroup(s.cmd)
	if s.untrack != nil {
		s.untrack()
	}
	if s.releaseEgress != nil {
		s.releaseEgress()
	}
	return err
}
