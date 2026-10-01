package confine_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
)

// keyMaterialTestFloor keeps confinement tests independent of pack contents.
var keyMaterialTestFloor = []string{
	"~/.ssh/", "~/.gnupg/",
	"~/.age/", "~/.config/sops/age/", "~/.docker/trust/",
	"~/Library/Keychains/", "~/.password-store/", "~/.local/share/keyrings/",
}

func TestMain(m *testing.M) {
	confine.SetKeyMaterialPathsSource(func() []string { return keyMaterialTestFloor })
	code := m.Run()
	confine.StopRefusalWatch()
	os.Exit(code)
}

func requireSeatbelt(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	self, err := os.Executable()
	testutil.FailErr(t, "os.Executable failed", err)
	return self
}

func outsideTemporaryWriteRoots(t *testing.T) string {
	t.Helper()
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	testutil.FailErr(t, "look up account home", err)
	dir, err := os.MkdirTemp(account.HomeDir, ".paintedwolf-confine-test-") //nolint:usetesting // Probe must be outside the permitted temporary write roots.
	testutil.FailErr(t, "create outside-write-root fixture", err)
	t.Cleanup(func() { testutil.FailErr(t, "remove outside-write-root fixture", os.RemoveAll(dir)) })
	return dir
}

// confinedExit runs name+args under the sandbox described by c and returns the exit code.
func confinedExit(t *testing.T, self string, c confine.Confinement, name string, args ...string) int {
	t.Helper()
	code, _ := confinedRun(t, self, c, name, args...)
	return code
}

// confinedRun is confinedExit plus the child's combined stdout+stderr.
func confinedRun(t *testing.T, self string, c confine.Confinement, name string, args ...string) (int, string) {
	t.Helper()
	cmd, cleanup, err := confine.Command(context.Background(), self, name, args, c)
	testutil.FailErr(t, "confine.Command failed", err)
	defer cleanup()
	out, _ := cmd.CombinedOutput()
	return cmd.ProcessState.ExitCode(), string(out)
}

// confinedStdout is confinedRun with stdout kept apart from stderr, for probes
// whose answer is stdout; system shims such as xcrun may warn on stderr.
func confinedStdout(t *testing.T, self string, c confine.Confinement, name string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd, cleanup, err := confine.Command(context.Background(), self, name, args, c)
	testutil.FailErr(t, "confine.Command failed", err)
	defer cleanup()
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, _ := cmd.Output()
	return cmd.ProcessState.ExitCode(), string(out), errBuf.String()
}

func TestBuildProfileConfinesWritesAndDeniesNetwork(t *testing.T) {
	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	for _, want := range []string{
		"(version 1)", "(deny default)", "(allow file-read*)", "(allow file-write*",
		`(subpath "/proj")`, `(literal "/dev/null")`,
		"(allow file-ioctl)", "(allow pseudo-tty)", `(literal "/dev/ptmx")`,
		"(allow signal (target same-sandbox))",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("profile missing %q:\n%s", want, p)
		}
	}
	for _, line := range strings.Split(p, "\n") {
		if strings.TrimSpace(line) == "(allow signal)" {
			t.Fatalf("unscoped signal allow must not appear:\n%s", p)
		}
	}
	if strings.Contains(p, "(allow network*)") {
		t.Fatalf("network must be denied by default:\n%s", p)
	}
	for _, forbidden := range []string{"(allow network-outbound", "(allow network-bind", "(allow network-inbound"} {
		if strings.Contains(p, forbidden) {
			t.Fatalf("NetworkDeny profile contains %q:\n%s", forbidden, p)
		}
	}

	pn, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}, Network: confine.NetworkDirectIP})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	if !strings.Contains(pn, `(allow network-outbound (remote ip "*:*"))`) {
		t.Fatalf("NetworkDirectIP should allow remote IP outbound:\n%s", pn)
	}
	if strings.Contains(pn, "(allow network*)") {
		t.Fatalf("NetworkDirectIP must not emit blanket network*:\n%s", pn)
	}
	if !strings.Contains(pn, "(allow network-bind") || !strings.Contains(pn, "(allow network-inbound") {
		t.Fatalf("NetworkDirectIP must carry explicit listener authority:\n%s", pn)
	}

	pp, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkProxyOnly,
		ProxyAddr: "127.0.0.1:8472", SocksProxyAddr: "127.0.0.1:8473",
	})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	for _, endpoint := range []string{"localhost:8472", "localhost:8473"} {
		if !strings.Contains(pp, endpoint) {
			t.Fatalf("NetworkProxyOnly missing private endpoint %s:\n%s", endpoint, pp)
		}
	}
	if strings.Contains(pp, `network-outbound (remote ip "localhost:*"`) {
		t.Fatalf("NetworkProxyOnly must not allow ambient local services:\n%s", pp)
	}
	if strings.Contains(pp, "(allow network*)") {
		t.Fatalf("NetworkProxyOnly must not allow all network:\n%s", pp)
	}
	if strings.Contains(pp, "(allow network-bind") || strings.Contains(pp, "(allow network-inbound") {
		t.Fatalf("NetworkProxyOnly must not carry listener authority:\n%s", pp)
	}
	if _, err := confine.BuildProfile(confine.Confinement{
		Roots: []string{"/proj"}, Network: confine.NetworkProxyOnly,
	}); err == nil {
		t.Fatal("unbound NetworkProxyOnly profile must fail closed")
	}
}

func TestDefaultConfinementPolicy(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")

	// Bypass disables confinement entirely.
	t.Setenv("LYCAON_BYPASS_APPROVALS", "1")
	if c, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}}); on || c != nil {
		t.Fatalf("bypass must disable confinement, got on=%v c=%v", on, c)
	}
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")

	// Rootless (no project) → not confined.
	if _, on := confine.DefaultConfinement(confine.Request{}); on {
		t.Fatal("empty project must not confine")
	}

	// Main off-switch disables everything.
	t.Setenv("LYCAON_SANDBOX", "off")
	if _, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}}); on {
		t.Fatal("LYCAON_SANDBOX=off must disable confinement")
	}
	t.Setenv("LYCAON_SANDBOX", "")

	if confine.Available() {
		// Default (any egress posture): proxy-only — direct sockets denied; HTTP(S) via proxy.
		c, on := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}})
		if !on || c == nil || c.Network != confine.NetworkProxyOnly {
			t.Fatalf("default confinement should be proxy-only: on=%v c=%+v", on, c)
		}
		restore := confine.SetBrokerForTest(nil, egressproxy.Addrs{HTTP: "127.0.0.1:8080", SOCKS: "127.0.0.1:1080"})
		defer restore()
		lease, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "default-test"})
		testutil.FailErr(t, "bind default egress", err)
		defer lease.Close(t.Context())
		if c.SocksProxyEnv {
			t.Fatal("default confinement must not enable SocksProxyEnv")
		}
		if env := confine.ProxyEnv(*c); len(env) == 0 {
			t.Fatal("proxy-only confinement must inject HTTP(S)_PROXY env")
		} else if strings.Contains(strings.Join(env, "\n"), "ALL_PROXY=") {
			t.Fatal("default ProxyEnv must omit ALL_PROXY")
		}
		cSocks, onSocks := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}, SocksProxyEnv: true})
		if !onSocks || cSocks == nil || !cSocks.SocksProxyEnv {
			t.Fatalf("SocksProxyEnv request not applied: on=%v c=%+v", onSocks, cSocks)
		}
		socksLease, err := confine.BindAction(cSocks, confine.EgressCommand{ToolCallID: "socks-test"})
		testutil.FailErr(t, "bind socks egress", err)
		defer socksLease.Close(t.Context())
		if !strings.Contains(strings.Join(confine.ProxyEnv(*cSocks), "\n"), "ALL_PROXY=socks5h://") {
			t.Fatal("SocksProxyEnv must inject ALL_PROXY")
		}
		confine.SetEgressPosture(confine.PostureAsk)
		if c, _ := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}}); c == nil || c.Network != confine.NetworkProxyOnly {
			t.Fatalf("Ask posture should still be proxy-only: %+v", c)
		}
		confine.SetEgressPosture(confine.PostureObserve)
		// LYCAON_SANDBOX_NETWORK=deny blocks all egress.
		t.Setenv("LYCAON_SANDBOX_NETWORK", "deny")
		if c, _ := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}}); c == nil || c.Network != confine.NetworkDeny {
			t.Fatalf("LYCAON_SANDBOX_NETWORK=deny should deny network: %+v", c)
		}
		for _, value := range []string{"allow", "unrestricted", "off"} {
			t.Setenv("LYCAON_SANDBOX_NETWORK", value)
			if c, _ := confine.DefaultConfinement(confine.Request{Roots: []string{"/proj"}}); c == nil || c.Network != confine.NetworkProxyOnly {
				t.Fatalf("LYCAON_SANDBOX_NETWORK=%q must not bypass proxy egress: %+v", value, c)
			}
		}
	}
}

func TestBuildProfileDeniesSecretReads(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "/opt/secrets")
	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	// Inspect only the read-deny block.
	readDeny, _ := profileBlock(t, p, blockDenyRead, 0)
	assertBlockCoversPath(t, readDeny, "/opt/secrets", "configured extra deny-read root")
	// Key material is read-denied until a declared read_path grant punches an
	// exact file. Credential stores stay readable so their CLIs can authenticate.
	for _, rel := range []string{"/.ssh", "/.gnupg"} {
		if !strings.Contains(readDeny, rel) {
			t.Fatalf("%s must be read-denied at the kernel:\n%s", rel, p)
		}
	}
	if strings.Contains(readDeny, "/.aws") {
		t.Fatalf("/.aws must stay readable — its CLI authenticates from it:\n%s", p)
	}

	// The switch disables configured control-plane denials; the key-material
	// floor stays.
	t.Setenv("LYCAON_SANDBOX_DENY_READ", "off")
	p2, _ := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	if strings.Contains(p2, "/opt/secrets") {
		t.Fatalf("LYCAON_SANDBOX_DENY_READ=off should drop configured read-denies:\n%s", p2)
	}
}

func TestBuildProfileDeniesPerActionSecretReads(t *testing.T) {
	project := t.TempDir()
	secret := filepath.Join(project, ".env.local")
	testutil.FailErr(t, "create per-action secret fixture", os.WriteFile(secret, []byte("TOKEN=fixture\n"), 0o600))
	c := confine.Confinement{ProjectID: "project", Roots: []string{project}, ReadDenyPaths: []string{secret}, Network: confine.NetworkDeny}
	profile, err := confine.BuildProfile(c)
	testutil.FailErr(t, "build per-action read-deny profile", err)
	resolvedSecret := fspath.CanonicalPath(secret)
	if !strings.Contains(profile, "(subpath \""+resolvedSecret+"\")") {
		t.Fatalf("profile does not deny per-action secret read %q: %s", secret, profile)
	}
}

// The configuration root remains outside the child read boundary.
func TestBuildProfileDeniesReadingOwnConfigDir(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	// The assertion targets the governing read-deny block.
	readDeny, _ := profileBlock(t, p, blockDenyRead, 0)
	assertBlockCoversPath(t, readDeny, cfg, "read deny (product config dir)")
}

// Agent workspaces override the configuration read denial.
func TestBuildProfileAllowsReadingAgentWorkspaces(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)

	roots := enginepaths.AgentWorkspaceRootsUnder(cfg)
	if len(roots) == 0 {
		t.Fatal("no agent workspace roots declared")
	}
	// Materialize roots before canonicalizing their spellings.
	for _, root := range roots {
		testutil.FailErr(t, "mkdir workspace", os.MkdirAll(root, 0o700))
	}

	// Isolate read allow-back from write-root coverage.
	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{t.TempDir()}})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	allowBack := readAllowBackBlock(t, p)
	for _, root := range roots {
		assertBlockCoversPath(t, allowBack, root, "read allow-back")
	}
}

func TestBuildProfileDeniesWritingSecretStoresEvenWhenHomeIsGranted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))
	// The test installs the catalog normally supplied at startup.
	confine.SetCredentialStorePathsSource(func() []string {
		return []string{
			"~/.aws/", "~/.kube/", "~/.config/gcloud/", "~/.azure/",
			"~/.docker/config.json", "~/.docker/contexts", "~/.docker/cli-plugins",
		}
	})
	t.Cleanup(func() { confine.SetCredentialStorePathsSource(nil) })

	p, err := confine.BuildProfile(confine.Confinement{
		Roots:             []string{t.TempDir()},
		GrantedWriteRoots: []string{home},
	})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		resolvedHome = home
	}
	// The ancestor is present in the write allow block.
	allowWrite, _ := profileBlock(t, p, blockAllowWrite, 0)
	assertBlockCoversPath(t, allowWrite, resolvedHome, "write-root allow (attached $HOME)")

	// Protected descendants remain in the deny block.
	denyWrite, _ := profileBlock(t, p, blockDenyWrite, 0)
	for _, rel := range []string{
		".ssh", ".aws", ".gnupg", ".kube", ".config/gcloud", ".azure",
		".docker/config.json", ".docker/contexts", ".docker/cli-plugins",
	} {
		assertBlockCoversPath(t, denyWrite, filepath.Join(resolvedHome, rel), "write hard-deny ("+rel+")")
	}
	// Uncatalogued siblings remain writable.
	buildx := filepath.Join(resolvedHome, ".docker", "buildx", "activity")
	if strings.Contains(denyWrite, buildx) {
		t.Fatalf("buildx runtime state must stay writable, deny block was:\n%s", denyWrite)
	}
}

func TestProxyEnvHTTPOnlyByDefault(t *testing.T) {
	c := confine.Confinement{
		Roots:          []string{"/p"},
		Network:        confine.NetworkProxyOnly,
		ProxyAddr:      "127.0.0.1:8080",
		SocksProxyAddr: "127.0.0.1:1080",
	}
	env := confine.ProxyEnv(c)
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HTTP_PROXY=http://127.0.0.1:8080",
		"HTTPS_PROXY=http://127.0.0.1:8080",
		"NO_PROXY=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ProxyEnv missing %q:\n%s", want, joined)
		}
	}
	for _, forbid := range []string{"ALL_PROXY=", "all_proxy=", "socks5h://"} {
		if strings.Contains(joined, forbid) {
			t.Fatalf("default ProxyEnv must omit SOCKS (%q present):\n%s", forbid, joined)
		}
	}
}

func TestProxyEnvSocksOptIn(t *testing.T) {
	c := confine.Confinement{
		Roots:          []string{"/p"},
		Network:        confine.NetworkProxyOnly,
		ProxyAddr:      "127.0.0.1:8080",
		SocksProxyAddr: "127.0.0.1:1080",
		SocksProxyEnv:  true,
	}
	env := confine.ProxyEnv(c)
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"HTTP_PROXY=http://127.0.0.1:8080",
		"ALL_PROXY=socks5h://127.0.0.1:1080",
		"all_proxy=socks5h://127.0.0.1:1080",
		"NO_PROXY=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("ProxyEnv missing %q:\n%s", want, joined)
		}
	}
	// Opt-in without SOCKS addr still omits ALL_PROXY.
	c2 := confine.Confinement{ProxyAddr: "127.0.0.1:9", SocksProxyEnv: true}
	if strings.Contains(strings.Join(confine.ProxyEnv(c2), "\n"), "ALL_PROXY=") {
		t.Fatal("ALL_PROXY must be omitted without SocksProxyAddr")
	}
}

func TestProcessEnvironmentReplacesAmbientProxy(t *testing.T) {
	c := confine.Confinement{
		Network:        confine.NetworkProxyOnly,
		ProxyAddr:      "127.0.0.1:8080",
		SocksProxyAddr: "127.0.0.1:1080",
	}
	base := []string{
		"PATH=/bin",
		"ALL_PROXY=socks5h://evil:x@9.9.9.9:9",
		"all_proxy=socks5h://evil:x@9.9.9.9:9",
		"HTTP_PROXY=http://ambient:x@9.9.9.9:8",
	}
	got := confine.ProcessEnvironment(base, c)
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "9.9.9.9") {
		t.Fatalf("ambient SOCKS/HTTP must not survive ProcessEnvironment:\n%s", joined)
	}
	if strings.Contains(joined, "ALL_PROXY=") || strings.Contains(joined, "all_proxy=") {
		t.Fatalf("HTTP-only ProcessEnvironment left ALL_PROXY:\n%s", joined)
	}
	if !strings.Contains(joined, "HTTP_PROXY=http://127.0.0.1:8080") {
		t.Fatalf("host HTTP_PROXY missing:\n%s", joined)
	}
	if !strings.Contains(joined, "PATH=/bin") {
		t.Fatal("unrelated env dropped")
	}
}

func TestProcessEnvironmentOptInKeepsSocks(t *testing.T) {
	c := confine.Confinement{
		Network:        confine.NetworkProxyOnly,
		ProxyAddr:      "127.0.0.1:8080",
		SocksProxyAddr: "127.0.0.1:1080",
		SocksProxyEnv:  true,
	}
	got := confine.ProcessEnvironment([]string{"ALL_PROXY=socks5h://evil@9.9.9.9:9"}, c)
	joined := strings.Join(got, "\n")
	if !strings.Contains(joined, "ALL_PROXY=socks5h://127.0.0.1:1080") {
		t.Fatalf("opt-in ALL_PROXY missing:\n%s", joined)
	}
	if strings.Contains(joined, "9.9.9.9") {
		t.Fatalf("ambient ALL_PROXY survived overlay:\n%s", joined)
	}
}

func TestDenyProcessEnvironmentStripsEveryProxyRoute(t *testing.T) {
	base := []string{
		"PATH=/bin", "HTTP_PROXY=http://127.0.0.1:8080", "https_proxy=http://localhost:8081",
		"ALL_PROXY=socks5h://127.0.0.1:1080", "NO_PROXY=example.com", "no_proxy=example.net",
	}
	got := strings.Join(confine.ProcessEnvironment(base, confine.Confinement{Network: confine.NetworkDeny}), "\n")
	for _, key := range []string{"HTTP_PROXY=", "https_proxy=", "ALL_PROXY=", "NO_PROXY=", "no_proxy="} {
		if strings.Contains(got, key) {
			t.Fatalf("deny process environment retained %s:\n%s", key, got)
		}
	}
	if !strings.Contains(got, "PATH=/bin") {
		t.Fatalf("deny process environment lost unrelated state:\n%s", got)
	}
}

func TestGitSSHEnvRoutesThroughConnector(t *testing.T) {
	c := confine.Confinement{
		Network:        confine.NetworkProxyOnly,
		SocksProxyAddr: "127.0.0.1:1080",
	}
	env := confine.GitSSHEnv(c, "/bin/lycaon", "/tmp/known_hosts")
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"GIT_SSH_COMMAND=",
		"ssh-proxy-command",
		"LYCAON_SOCKS_PROXY=127.0.0.1:1080",
		"LYCAON_SSH_KNOWN_HOSTS=/tmp/known_hosts",
		"StrictHostKeyChecking=accept-new",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("GitSSHEnv missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "github.com") {
		t.Fatal("GitSSHEnv must not rewrite vendor hosts")
	}
	if confine.GitSSHEnv(confine.Confinement{Network: confine.NetworkDirectIP}, "/bin/lycaon", "/tmp/k") != nil {
		t.Fatal("GitSSHEnv only under proxy-only")
	}
}

func TestGitSSHEnvQuotesAppBundleExecutablePath(t *testing.T) {
	c := confine.Confinement{
		Network:        confine.NetworkProxyOnly,
		SocksProxyAddr: "127.0.0.1:1080",
	}
	env := confine.GitSSHEnv(c, "/Applications/Painted Wolf Code.app/Contents/MacOS/painted-wolf", "/tmp/known hosts")
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "UserKnownHostsFile=/tmp/known hosts") {
		t.Fatalf("known-hosts option missing: %s", joined)
	}
	sshCommand := strings.TrimPrefix(env[0], "GIT_SSH_COMMAND=")
	parsed, err := exec.Command("/bin/sh", "-c", `set -- `+sshCommand+`; printf '%s\n' "$@"`).Output()
	testutil.FailErr(t, "parse GIT_SSH_COMMAND", err)
	args := strings.Split(strings.TrimSpace(string(parsed)), "\n")
	// The path with spaces remains one argv element.
	knownHostsArgs := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "UserKnownHostsFile=") {
			continue
		}
		knownHostsArgs++
		if !strings.HasPrefix(arg, "UserKnownHostsFile=/tmp/known hosts") {
			t.Fatalf("outer shell split known-hosts path: %q", args)
		}
	}
	if knownHostsArgs != 1 {
		t.Fatalf("known-hosts option must be one argument, got %d: %q", knownHostsArgs, args)
	}
	if !slices.Contains(args, "ProxyCommand='/Applications/Painted Wolf Code.app/Contents/MacOS/painted-wolf' ssh-proxy-command %h %p") {
		t.Fatalf("outer shell split ProxyCommand executable: %q", args)
	}
}

func TestProtectedPathCannotBeGrantedAsWriteRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "read home directory", err)
	path := filepath.Join(home, ".ssh", "authorized_keys")
	if refused, code := confine.AttachedWriteRootRefused(path); !refused {
		t.Fatalf("protected path accepted as write root, code=%q", code)
	}
}

func TestSeatbeltDraftWorkspaceVenvInterpreterExecutes(t *testing.T) {
	self := requireSeatbelt(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for venv confinement probe")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := filepath.Join(home, ".config", "paintedwolf-dev")
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	draft, err := project.DraftWorkspaceDir("venv-probe")
	testutil.FailErr(t, "project.DraftWorkspaceDir failed", err)
	testutil.FailErr(t, "mkdir draft", os.MkdirAll(draft, 0o700))
	venv := filepath.Join(draft, ".venv")
	confinement := confine.Confinement{Roots: []string{draft}}
	code, out := confinedRun(t, self, confinement, python, "-m", "venv", venv)
	if code != 0 {
		t.Fatalf("create draft venv under confinement, exit=%d out=%s", code, out)
	}

	interpreter := filepath.Join(venv, "bin", "python3")
	code, out = confinedRun(t, self, confinement, interpreter, "-c", "print('venv-ok')")
	if code != 0 || strings.TrimSpace(out) != "venv-ok" {
		t.Fatalf("draft venv interpreter must execute under confinement, exit=%d out=%s", code, out)
	}
	code, out = confinedRun(t, self, confinement, interpreter, "-m", "ensurepip", "--upgrade")
	if code != 0 {
		t.Fatalf("draft venv bootstrap must execute under confinement, exit=%d out=%s", code, out)
	}
}

func TestBuildProfileIncludesStandardCacheRoots(t *testing.T) {
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", "/opt/customcache")
	p, err := confine.BuildProfile(confine.Confinement{Roots: []string{"/proj"}})
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	// Explicit write roots extend the default filesystem scope.
	allowWrite, _ := profileBlock(t, p, blockAllowWrite, 0)
	if !strings.Contains(allowWrite, `(subpath "/opt/customcache")`) {
		t.Fatalf("extra write-root not honored:\n%s", p)
	}
}

// Applied profiles enforce filesystem and network rules.
func TestSeatbeltEnforces(t *testing.T) {
	self := requireSeatbelt(t)
	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}

	// Project writes are allowed.
	inFile := filepath.Join(proj, "ok.txt")
	if code := confinedExit(t, self, c, "/usr/bin/touch", inFile); code != 0 {
		t.Fatalf("in-project write should succeed, exit=%d", code)
	}
	if _, err := os.Stat(inFile); err != nil {
		t.Fatalf("in-project file not created: %v", err)
	}

	// Outside-root writes are denied.
	escape := filepath.Join(outsideTemporaryWriteRoots(t), "escape")
	_ = os.Remove(escape)
	if code := confinedExit(t, self, c, "/usr/bin/touch", escape); code == 0 {
		_ = os.Remove(escape)
		t.Fatalf("write outside allowed roots should be blocked by the sandbox, but it succeeded")
	}
	if _, err := os.Stat(escape); err == nil {
		_ = os.Remove(escape)
		t.Fatalf("escape file was created despite the sandbox")
	}

	// Denied network requests fail.
	if code := confinedExit(t, self, c, "/usr/bin/curl", "-s", "--max-time", "3", "http://example.com"); code == 0 {
		t.Fatalf("network should be denied, but curl succeeded")
	}

	// Terminal control remains available.
	if code := confinedExit(t, self, c, "/usr/bin/python3", "-c",
		"import tty,sys; tty.setcbreak(sys.stdin.fileno()); print('ok')"); code != 0 {
		// A direct terminal probe distinguishes missing TTYs from boundary denials.
		// The probe reports its own errno, since shims may write unrelated
		// permission warnings to stderr.
		_, out, stderr := confinedStdout(t, self, confine.Confinement{Roots: []string{proj}}, "/usr/bin/python3", "-c",
			"import os,termios\n"+
				"try:\n"+
				"    termios.tcgetattr(os.open('/dev/tty', os.O_RDWR))\n"+
				"    print('ok')\n"+
				"except OSError as e:\n"+
				"    print('errno', e.errno)\n"+
				"except termios.error as e:\n"+
				"    print('errno', e.args[0])\n")
		switch strings.TrimSpace(out) {
		case "ok":
		case "errno 1":
			t.Fatalf("tty ioctl blocked by sandbox: stderr=%s", stderr)
		default:
			// Missing controlling terminals are acceptable here.
			t.Logf("tty ioctl probe skipped (no usable /dev/tty): out=%s stderr=%s", out, stderr)
		}
	}
}

// Process signals remain within the applied profile.
func TestSeatbeltSignalSameSandboxOnly(t *testing.T) {
	self := requireSeatbelt(t)
	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}

	// The victim is outside the applied profile.
	victim := exec.Command("/bin/sleep", "60")
	if err := victim.Start(); err != nil {
		testutil.FailErr(t, "victim.Start failed", err)
	}
	defer func() {
		_ = victim.Process.Kill()
		_, _ = victim.Process.Wait()
	}()

	cmd, cleanup, err := confine.Command(context.Background(), self, "/bin/kill",
		[]string{"-9", strconv.Itoa(victim.Process.Pid)}, c)
	testutil.FailErr(t, "confine.Command failed", err)
	defer cleanup()
	out, runErr := cmd.CombinedOutput()
	if runErr == nil {
		t.Fatalf("kill of foreign PID should be denied; out=%s", out)
	}
	// The denied signal leaves the victim alive.
	alive := exec.Command("/bin/kill", "-0", strconv.Itoa(victim.Process.Pid))
	if err := alive.Run(); err != nil {
		t.Fatalf("foreign victim exited after confined kill (signal leaked): %v out=%s", err, out)
	}

	// Descendants inherit the profile and remain signalable.
	same, cleanup2, err := confine.Command(context.Background(), self, "/usr/bin/python3", []string{"-c",
		"import os, signal, sys\n" +
			"pid = os.fork()\n" +
			"if pid == 0:\n" +
			"    import time\n" +
			"    time.sleep(30)\n" +
			"else:\n" +
			"    os.kill(pid, signal.SIGTERM)\n" +
			"    os.waitpid(pid, 0)\n" +
			"    print('ok')\n",
	}, c)
	testutil.FailErr(t, "confine.Command failed", err)
	defer cleanup2()
	same.WaitDelay = 10 * time.Second
	out2, err := same.CombinedOutput()
	if err != nil {
		t.Fatalf("same-sandbox signal should succeed: %v out=%s", err, out2)
	}
	if !strings.Contains(string(out2), "ok") {
		t.Fatalf("same-sandbox signal missing ok: %s", out2)
	}
}

// Managed browsing preserves filesystem and network boundaries.
func TestSeatbeltBrowserProfileEnforces(t *testing.T) {
	self := requireSeatbelt(t)
	proj := t.TempDir()
	confine.TestingSetAutoConfine(t)
	built, ok := confine.BrowserConfinement(confine.Request{Roots: []string{proj}})
	if !ok || built == nil {
		t.Fatal("expected BrowserConfinement on darwin")
	}
	c := *built

	inFile := filepath.Join(proj, "ok.txt")
	if code := confinedExit(t, self, c, "/usr/bin/touch", inFile); code != 0 {
		t.Fatalf("in-project write under browser profile failed: exit %d", code)
	}
	escape := filepath.Join(outsideTemporaryWriteRoots(t), "escape")
	_ = os.Remove(escape)
	if code := confinedExit(t, self, c, "/usr/bin/touch", escape); code == 0 {
		_ = os.Remove(escape)
		t.Fatal("escape write should be denied under browser profile")
	}
	_ = os.Remove(escape)
	if code := confinedExit(t, self, c, "/usr/bin/curl", "-s", "--max-time", "3", "http://example.com"); code == 0 {
		t.Fatal("external curl should be denied under browser NetworkDeny")
	}
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ambient"))
	}))
	defer local.Close()
	code, out := confinedRun(t, self, c, "/usr/bin/curl", "-s", "--max-time", "3", local.URL)
	if code != 0 || !strings.Contains(out, "ambient") {
		t.Fatalf("browser floor must reach host-local TCP: exit=%d out=%q", code, out)
	}
}

// Development build roots under ~/.config/paintedwolf-dev must compile cleanly without Seatbelt errors.
func TestSeatbeltBrowserProfileUnderDevelopmentBuild(t *testing.T) {
	self := requireSeatbelt(t)
	confine.TestingSetAutoConfine(t)
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "user home dir", err)

	devDir := filepath.Join(home, ".config", "paintedwolf-dev")
	browserCache := filepath.Join(devDir, "browser-cache")
	profileDir := filepath.Join(browserCache, "profiles", "profile-test")
	draftDir := filepath.Join(devDir, "drafts", "draft-test-uuid")

	testutil.FailErr(t, "mkdir profileDir", os.MkdirAll(profileDir, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(profileDir) })
	testutil.FailErr(t, "mkdir draftDir", os.MkdirAll(draftDir, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(draftDir) })

	built, ok := confine.BrowserConfinement(confine.Request{
		Roots: []string{browserCache, profileDir, draftDir},
	})
	if !ok || built == nil {
		t.Fatal("expected BrowserConfinement on darwin")
	}

	code, out := confinedRun(t, self, *built, "/usr/bin/true")
	if code != 0 {
		t.Fatalf("browser profile under development roots failed to apply: exit=%d out=%q", code, out)
	}
}

// Configured secret roots remain unreadable.
func TestSeatbeltDeniesSecretReads(t *testing.T) {
	self := requireSeatbelt(t)
	proj := t.TempDir()
	secretDir := t.TempDir()
	secret := filepath.Join(secretDir, "key")
	if err := os.WriteFile(secret, []byte("topsecret"), 0o600); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	t.Setenv("LYCAON_SANDBOX_DENY_READ", secretDir)
	c := confine.Confinement{Roots: []string{proj}}

	if code := confinedExit(t, self, c, "/bin/cat", secret); code == 0 {
		t.Fatal("reading a configured secret dir should be denied by the sandbox")
	}
	okFile := filepath.Join(proj, "ok.txt")
	if err := os.WriteFile(okFile, []byte("hi"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if code := confinedExit(t, self, c, "/bin/cat", okFile); code != 0 {
		t.Fatalf("a normal in-project read should still work, exit=%d", code)
	}
}

// Nested executables retain the write boundary.
func TestSeatbeltFSBlastBoundary(t *testing.T) {
	self := requireSeatbelt(t)
	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}

	// Outside-root writes are denied.
	home, err := os.UserHomeDir()
	testutil.FailErr(t, "os.UserHomeDir failed", err)
	outsideRoot := outsideTemporaryWriteRoots(t)
	escape := filepath.Join(outsideRoot, "escape")
	_ = os.Remove(escape)
	if code := confinedExit(t, self, c, "/bin/sh", "-c", "echo x > "+escape); code == 0 {
		_ = os.Remove(escape)
		t.Fatal("write outside root must be denied by Seatbelt")
	}
	_ = os.Remove(escape)

	// Protected key paths remain write-denied.
	sshDir := filepath.Join(home, ".ssh")
	if st, err := os.Stat(sshDir); err == nil && st.IsDir() {
		probe := filepath.Join(sshDir, ".lycaon_fs_blast_probe")
		if code := confinedExit(t, self, c, "/bin/sh", "-c", "echo x > "+probe); code == 0 {
			_ = os.Remove(probe)
			t.Fatal("writing into ~/.ssh must be denied")
		}
	} else {
		t.Log("skip ~/.ssh probe — directory absent on this host")
	}

	// A nested executable retains the same write boundary.
	outside := filepath.Join(outsideRoot, "find-target")
	if err := os.WriteFile(outside, []byte("keep"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	defer func() { _ = os.Remove(outside) }()
	_ = confinedExit(t, self, c, "/usr/bin/find", filepath.Dir(outside), "-maxdepth", "1",
		"-name", filepath.Base(outside), "-exec", "/bin/rm", "{}", "+")
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("find -exec rm outside root must not delete the file: %v", err)
	}

	// In-project rm -rf succeeds under the jail.
	build := filepath.Join(proj, "build")
	if err := os.MkdirAll(filepath.Join(build, "out"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if code := confinedExit(t, self, c, "/bin/rm", "-rf", build); code != 0 {
		t.Fatalf("in-project rm -rf ./build must succeed under Seatbelt, exit=%d", code)
	}
	if _, err := os.Stat(build); !os.IsNotExist(err) {
		t.Fatalf("in-project build dir should be gone, stat err=%v", err)
	}
}

// Mediated and direct sockets retain separate boundaries.
func TestSeatbeltEgressBoundary(t *testing.T) {
	self := requireSeatbelt(t)
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")
	proj := t.TempDir()

	c, on := confine.DefaultConfinement(confine.Request{Roots: []string{proj}})
	if !on || c == nil || c.Network != confine.NetworkProxyOnly {
		t.Fatalf("DefaultConfinement must be proxy-only: on=%v c=%+v", on, c)
	}
	defer confine.SetBrokerForTest(nil, egressproxy.Addrs{HTTP: "127.0.0.1:8080", SOCKS: "127.0.0.1:1080"})()
	lease, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "seatbelt-boundary"})
	testutil.FailErr(t, "bind egress", err)
	defer lease.Close(t.Context())

	// Loopback HTTPS via the wired proxy address succeeds (proxy-only allows the proxy).
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()
	proxyOnly := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkProxyOnly, ProxyAddr: strings.TrimPrefix(ts.URL, "http://")}
	if code, out := confinedRun(t, self, proxyOnly, "/usr/bin/curl", "-s", "--max-time", "3", ts.URL); code != 0 {
		t.Fatalf("loopback via proxy address must succeed: exit=%d out=%s", code, out)
	}
	unrelated := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ambient"))
	}))
	defer unrelated.Close()
	if code := confinedExit(t, self, proxyOnly, "/usr/bin/curl", "-s", "--max-time", "3", unrelated.URL); code == 0 {
		t.Fatal("proxy-only profile reached an unrelated host-local TCP port")
	}

	// Direct external HTTP denied (not through the proxy).
	if code := confinedExit(t, self, proxyOnly, "/usr/bin/curl", "-s", "--max-time", "3", "http://example.com"); code == 0 {
		t.Fatal("direct external HTTP must be denied under proxy-only")
	}

	// Direct remote sockets remain denied.
	if _, err := exec.LookPath("ssh"); err == nil {
		if code := confinedExit(t, self, proxyOnly, "ssh", "-o", "ConnectTimeout=2", "-o", "BatchMode=yes",
			"-o", "StrictHostKeyChecking=no", "example.com", "true"); code == 0 {
			t.Fatal("raw ssh to a remote host must be denied under proxy-only")
		}
	} else {
		t.Log("skip ssh probe — ssh not on PATH")
	}

	// The live proxy enforces destination policy.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	defer upstream.Close()
	p := newTestBrokerFor(
		func(context.Context, string, egressproxy.Endpoint) bool { return true },
		leasedPeer("cmd"), // attribution is covered in egressproxy's own tests
	)
	grantedPort := uint16(upstream.Listener.Addr().(*net.TCPAddr).Port)
	p.SetLoopbackConnectAuthority(func(_ string, port uint16) bool { return port == grantedPort })
	if _, err := p.Start(); err != nil {
		testutil.FailErr(t, "p.Start failed", err)
	}
	defer func() { _ = p.Close() }()
	via := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkProxyOnly, ProxyAddr: p.Addr()}
	cmd, cleanup, err := confine.Command(context.Background(), self, "/usr/bin/curl",
		[]string{"-sf", "--max-time", "5", upstream.URL}, via)
	testutil.FailErr(t, "confine.Command failed", err)
	defer cleanup()
	cmd.Env = confine.ProcessEnvironment(os.Environ(), via)
	if err := cmd.Run(); err != nil {
		t.Fatalf("proxied HTTP to the granted local port must succeed: %v", err)
	}
}

// TestProxyOnlyRoutesThroughInAppProxy checks the confined proxy path.
func TestProxyOnlyRoutesThroughInAppProxy(t *testing.T) {
	self := requireSeatbelt(t)
	dir := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("reached"))
	}))
	defer upstream.Close()

	curlVia := func(verdict bool) int {
		p := newTestBrokerFor(
			func(context.Context, string, egressproxy.Endpoint) bool { return verdict },
			leasedPeer("cmd"), // attribution is covered in egressproxy's own tests
		)
		// Inject loopback dialing to isolate proxy policy.
		p.SetDialEndpointForTest(func(ctx context.Context, ep egressproxy.Endpoint) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", fmt.Sprintf("%s:%d", ep.Host, ep.Port))
		})
		if _, err := p.Start(); err != nil {
			testutil.FailErr(t, "p.Start failed", err)
		}
		defer func() { _ = p.Close() }()
		c := confine.Confinement{Roots: []string{dir}, Network: confine.NetworkProxyOnly, ProxyAddr: p.Addr()}
		cmd, cleanup, err := confine.Command(context.Background(), self, "/usr/bin/curl",
			[]string{"-sf", "--max-time", "5", upstream.URL}, c)
		testutil.FailErr(t, "confine.Command failed", err)
		defer cleanup()
		cmd.Env = confine.ProcessEnvironment(os.Environ(), c)
		_ = cmd.Run()
		return cmd.ProcessState.ExitCode()
	}

	if code := curlVia(true); code != 0 {
		t.Fatalf("allowed host should be reachable through the in-app proxy, exit=%d", code)
	}
	if code := curlVia(false); code == 0 {
		t.Fatal("denied host should be refused by the in-app proxy")
	}
}

// Proxy-only egress limits the child to its action endpoint.
func TestSeatbeltProxyOnlyEgress(t *testing.T) {
	self := requireSeatbelt(t)
	proj := t.TempDir()

	// The local server represents the action proxy.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	proxyAddr := strings.TrimPrefix(ts.URL, "http://") // 127.0.0.1:PORT
	c := confine.Confinement{Roots: []string{proj}, Network: confine.NetworkProxyOnly, ProxyAddr: proxyAddr}

	code, out := confinedRun(t, self, c, "/usr/bin/curl", "-s", "--max-time", "3", ts.URL)
	if strings.Contains(out, "seatbelt:") {
		t.Fatalf("proxy-only SBPL profile failed to compile: %s", out)
	}
	// The action proxy address is reachable.
	if code != 0 {
		t.Fatalf("curl to the proxy address should succeed under proxy-only egress, exit=%d out=%s", code, out)
	}
	// Other addresses remain denied.
	if ext, _ := confinedRun(t, self, c, "/usr/bin/curl", "-s", "--max-time", "3", "http://example.com"); ext == 0 {
		t.Fatal("external egress should be denied under proxy-only")
	}
}

func TestBrowserConfinementIsLoopbackOnly(t *testing.T) {
	// An empty value selects the default network mode.
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")

	confine.TestingSetAutoConfine(t)
	if runtime.GOOS != "darwin" || !confine.Available() {
		if c, ok := confine.BrowserConfinement(confine.Request{Roots: []string{"/proj"}}); ok || c != nil {
			t.Fatalf("expected nil when seatbelt unavailable; got ok=%v c=%v", ok, c)
		}
		return
	}
	c, ok := confine.BrowserConfinement(confine.Request{Roots: []string{"/proj"}})
	if !ok || c == nil {
		t.Fatal("expected BrowserConfinement on darwin")
	}
	if c.Network != confine.NetworkDeny {
		t.Fatalf("Network = %v, want NetworkDeny", c.Network)
	}
	if !c.LoopbackConnect || len(c.LoopbackConnectPorts) != 0 {
		t.Fatalf("browser floor must allow un-narrowed host-local connect: %+v", c)
	}
	if c.ProxyAddr != "" || c.SocksProxyAddr != "" || c.LineageID != "" {
		t.Fatalf("browser confine must not wire proxy: %+v", c)
	}
	if !c.Browser {
		t.Fatal("Browser flag must be set")
	}
	if b := confine.BoundaryOf(c); !b.LoopbackConnect || b.Network != confine.NetworkDeny {
		t.Fatalf("browser boundary = %+v", b)
	}
	p, err := confine.BuildProfile(*c)
	testutil.FailErr(t, "confine.BuildProfile failed", err)
	for _, want := range []string{
		"(allow process-info-pidinfo)",
		"(allow iokit*)",
		"(allow mach*)",
		"(allow hid-control)",
		"(allow file-map-executable)",
		"(allow device-microphone)",
		`(allow network-outbound (remote ip "localhost:*"))`,
		"(deny default)",
	} {
		if !strings.Contains(p, want) {
			t.Fatalf("browser profile missing %q:\n%s", want, p)
		}
	}
	for _, forbidden := range []string{
		`(allow network-outbound (remote ip "*:*"))`,
		`(remote tcp`,
	} {
		if strings.Contains(p, forbidden) {
			t.Fatalf("browser profile widened public or proxy egress %q:\n%s", forbidden, p)
		}
	}
}

// Draft roots activate confinement.
func TestDefaultConfinementEnabledForDraftScratchRoot(t *testing.T) {
	confine.TestingSetAutoConfine(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	scratch, err := project.DraftWorkspaceDir("draft-project-1")
	testutil.FailErr(t, "project.DraftWorkspaceDir failed", err)
	if !confine.Available() {
		t.Skip("confinement unavailable on this platform")
	}
	c, on := confine.DefaultConfinement(confine.Request{Roots: []string{scratch}})
	if !on || c == nil {
		t.Fatalf("draft scratch root should confine when available: on=%v c=%v", on, c)
	}
	defer confine.SetBrokerForTest(nil, egressproxy.Addrs{HTTP: "127.0.0.1:8080", SOCKS: "127.0.0.1:1080"})()
	lease, err := confine.BindAction(c, confine.EgressCommand{ToolCallID: "draft-scratch-test"})
	testutil.FailErr(t, "bind draft scratch egress", err)
	defer lease.Close(t.Context())
	if _, err := confine.BuildProfile(*c); err != nil {
		t.Fatalf("draft scratch confinement must render a profile: %v", err)
	}
}

// Declared agent workspaces remain readable and writable in the sandbox.
func TestSeatbeltAgentWorkspacesAreReachable(t *testing.T) {
	self := requireSeatbelt(t)
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	secret := filepath.Join(cfg, "credential-vault.age")
	testutil.FailErr(t, "write credentials", os.WriteFile(secret, []byte("key: topsecret\n"), 0o600))

	roots := enginepaths.AgentWorkspaceRootsUnder(cfg)
	if len(roots) == 0 {
		t.Fatal("no agent workspace roots declared")
	}
	elsewhere := t.TempDir()

	for _, root := range roots {
		t.Run(filepath.Base(root), func(t *testing.T) {
			// Runtime workspaces place files under per-item directories.
			item := filepath.Join(root, "item-1")
			testutil.FailErr(t, "mkdir workspace item", os.MkdirAll(item, 0o700))
			probe := filepath.Join(item, "probe.txt")
			testutil.FailErr(t, "seed probe", os.WriteFile(probe, []byte("x\n"), 0o600))

			// Read carve-out: reachable while an unrelated directory is the root.
			outside := confine.Confinement{Roots: []string{elsewhere}}
			if code, out := confinedRun(t, self, outside, "/bin/cat", probe); code != 0 {
				t.Fatalf("workspace must stay readable under the config-dir read deny, exit=%d out=%s", code, out)
			}

			// Write carve-out: writable when it is the project root.
			asRoot := confine.Confinement{Roots: []string{item}}
			if code, out := confinedRun(t, self, asRoot, "/usr/bin/touch", filepath.Join(item, "f.txt")); code != 0 {
				t.Fatalf("writing inside a workspace root must succeed, exit=%d out=%s", code, out)
			}
			if code, out := confinedRun(t, self, asRoot, "/bin/mkdir", filepath.Join(item, "sub")); code != 0 {
				t.Fatalf("mkdir inside a workspace root must succeed, exit=%d out=%s", code, out)
			}

			// Workspace grants exclude credentials and unrelated host state.
			if code := confinedExit(t, self, asRoot, "/bin/cat", secret); code == 0 {
				t.Fatal("workspace root must not confer reads on config-dir credentials")
			}
			if code := confinedExit(t, self, asRoot, "/usr/bin/touch", secret); code == 0 {
				t.Fatal("workspace root must not confer writes on config-dir credentials")
			}
			if code := confinedExit(t, self, asRoot, "/usr/bin/touch", filepath.Join(cfg, "api.token")); code == 0 {
				t.Fatal("workspace root must not allow creating api.token")
			}
			if code := confinedExit(t, self, asRoot, "/bin/mkdir", filepath.Join(cfg, "projects", "x")); code == 0 {
				t.Fatal("workspace root must not confer writes on unrelated host state")
			}
		})
	}
}

// Profiles stay in memory to prevent cross-command replacement.
func TestProfileIsNotStagedInAFile(t *testing.T) {
	self := requireSeatbelt(t)
	root := t.TempDir()
	c := confine.Confinement{Roots: []string{root}, Network: confine.NetworkDeny}

	cmd, cleanup, err := confine.Command(context.Background(), self, "true", nil, c)
	testutil.FailErr(t, "confine.Command failed", err)
	defer cleanup()

	for _, arg := range cmd.Args {
		if strings.HasSuffix(arg, ".sb") {
			t.Fatalf("profile passed as a file path: %v", cmd.Args)
		}
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("profile must travel over one inherited pipe, got %d extra files", len(cmd.ExtraFiles))
	}
	if code := confinedExit(t, self, c, "sh", "-c", "echo hi > /etc/lycaon-escape-probe"); code == 0 {
		t.Fatal("write outside the jail should be denied")
	}
}

// leasedPeer resolves every caller to one live action; attribution itself is
// covered by the broker's own tests.
func leasedPeer(lineage string) egressproxy.PeerResolver {
	return func(_, _ netip.AddrPort) (egressproxy.Peer, bool) {
		return egressproxy.Peer{Lineage: lineage, Leased: true}, true
	}
}

// newTestBrokerFor builds a broker that already knows who is calling it.
func newTestBrokerFor(decide egressproxy.EndpointDecider, resolve egressproxy.PeerResolver) *egressproxy.Broker {
	b := egressproxy.New(decide)
	b.SetPeerResolver(resolve)
	return b
}
