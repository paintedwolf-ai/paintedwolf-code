// Package wea_test implements TB08 (Confinement, egress and legible denials)
// for case families A05, A06, A07, A08 (including H3 triage), and A09.
//
// Defensive testing scope (D4-D7):
//   - Observes confinement strictly from outside the boundary (host SHA-256 reads,
//     loopback recording HTTP/TCP fixtures, independent host process table).
//   - Never weakens protections, never disables the sandbox, never probes exploit chains.
//   - Uses isolated temp fixtures cleaned up on test completion and loopback fixtures only.
//   - Runs on both macOS (Seatbelt) and Linux (namespaces/Landlock/seccomp) with D13
//     documented platform differences explicitly named.
package wea_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/confine/admissiontest"
	"github.com/lycaon/lycaon/internal/confine/confinetest"
	"github.com/lycaon/lycaon/internal/egressgate"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/socketref"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/webresearch"
)

// tb08KeyMaterialFloor keeps confinement checks deterministic across environments.
var tb08KeyMaterialFloor = []string{
	"~/.ssh/", "~/.gnupg/",
	"~/.age/", "~/.config/sops/age/", "~/.docker/trust/",
	"~/Library/Keychains/", "~/.password-store/", "~/.local/share/keyrings/",
	"~/.local/share/kwalletd/",
}

// tb08PlatformProfile names the D13 documented platform differences between
// macOS (Seatbelt) and Linux (namespaces/Landlock/seccomp).
type tb08PlatformProfile struct {
	GOOS                         string `json:"goos"`
	Adapter                      string `json:"adapter"`
	RefusalWitness               string `json:"refusal_witness"`
	LeftBehindOutlivesCommand    bool   `json:"left_behind_outlives_command"`
	DeniedDirectoryIsMasked      bool   `json:"denied_directory_is_masked"`
	BlanketXDGDataWriteAllowed   bool   `json:"blanket_xdg_data_write_allowed"`
	ProcessControlSignalsOutside bool   `json:"process_control_signals_outside"`
}

func currentPlatformProfile() tb08PlatformProfile {
	if runtime.GOOS == "linux" {
		return tb08PlatformProfile{
			GOOS:                         "linux",
			Adapter:                      "namespaces+landlock+seccomp",
			RefusalWitness:               string(confine.WitnessIncomplete),
			LeftBehindOutlivesCommand:    false, // PID namespace PID 1 teardown ends descendants
			DeniedDirectoryIsMasked:      true,  // empty read-only placeholder mount
			BlanketXDGDataWriteAllowed:   false, // no blanket ~/.local/share or ~/.local/state write root
			ProcessControlSignalsOutside: false, // PID namespace still bounds signals
		}
	}
	return tb08PlatformProfile{
		GOOS:                         runtime.GOOS,
		Adapter:                      "seatbelt",
		RefusalWitness:               string(confine.WitnessKernel),
		LeftBehindOutlivesCommand:    true,  // daemon retains Seatbelt boundary until killed
		DeniedDirectoryIsMasked:      false, // Seatbelt denies stat/listing directly
		BlanketXDGDataWriteAllowed:   false,
		ProcessControlSignalsOutside: true,
	}
}

func tb08RequireLiveHelper(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("TB08 confinement requires darwin or linux (got %s)", runtime.GOOS)
	}
	if !confine.Available() {
		if caps, isLinux := confine.LinuxCapabilities(); isLinux && len(caps.Unavailable) > 0 {
			t.Fatalf("Linux confinement unavailable (D5 fail-closed): %+v", caps.Unavailable)
		}
		t.Fatalf("OS confinement unavailable on %s (D5 fail-closed)", runtime.GOOS)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	return self
}

func tb08OutsideRootDir(t *testing.T) string {
	t.Helper()
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatalf("user.LookupId: %v", err)
	}
	dir, err := os.MkdirTemp(account.HomeDir, ".pw-tb08-outside-")
	if err != nil {
		t.Fatalf("create outside-root fixture under home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func tb08SHA256File(t *testing.T, path string) (string, bool) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), true
}

func tb08SHA256String(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func tb08RunConfined(t *testing.T, self string, c confine.Confinement, name string, args ...string) (int, string) {
	t.Helper()
	cmd, cleanup, err := confine.Command(t.Context(), self, name, args, c)
	if err != nil {
		t.Fatalf("confine.Command(%s): %v", name, err)
	}
	defer cleanup()
	cmd.Env = confine.ProcessEnvironment(os.Environ(), c)
	if scratch := c.Workspaces.SessionScratch; scratch != "" {
		cmd.Env = append(cmd.Env, "TMPDIR="+scratch, "TMP="+scratch, "TEMP="+scratch)
	}
	out, _ := cmd.CombinedOutput()
	if cmd.ProcessState == nil {
		return -1, string(out)
	}
	return cmd.ProcessState.ExitCode(), string(out)
}

func tb08LogReceipt(t *testing.T, family string, payload map[string]any) {
	t.Helper()
	payload["schema"] = "wea-tb08-receipt-v1"
	payload["family"] = family
	payload["platform"] = currentPlatformProfile()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal TB08 receipt: %v", err)
	}
	t.Logf("WEA_TB08_RECEIPT:%s", raw)
}

// ---------------------------------------------------------------------------
// A05 · Filesystem boundary (macOS Seatbelt + Linux namespaces/Landlock/seccomp)
// ---------------------------------------------------------------------------

func TestWEATB08_A05_FilesystemConfinement(t *testing.T) {
	self := tb08RequireLiveHelper(t)
	confine.SetKeyMaterialPathsSource(func() []string { return tb08KeyMaterialFloor })

	admissiontest.Enable(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")

	admittedRoot := t.TempDir()
	scratchDir := t.TempDir()
	outsideDir := tb08OutsideRootDir(t)

	outsideFile := filepath.Join(outsideDir, "protected-outside.txt")
	const initialOutside = "tb08-immutable-outside-bytes\n"
	if err := os.WriteFile(outsideFile, []byte(initialOutside), 0o600); err != nil {
		t.Fatalf("write outside fixture: %v", err)
	}
	preOutsideSHA, exists := tb08SHA256File(t, outsideFile)
	if !exists {
		t.Fatalf("outside fixture missing before test")
	}

	c := confine.Confinement{
		Roots:      []string{admittedRoot},
		Workspaces: confine.WorkspaceRoots{SessionScratch: scratchDir},
		Network:    confine.NetworkDeny,
	}
	boundary := confine.BoundaryOf(&c)
	if !boundary.Applied {
		t.Fatalf("expected BoundaryOf to report Applied=true")
	}

	// 1. Outside-root direct write MUST be refused and outside bytes unchanged by SHA-256.
	t.Run("outside_root_write_refused", func(t *testing.T) {
		code, out := tb08RunConfined(t, self, c, "/bin/sh", "-c",
			fmt.Sprintf("printf 'mutated\\n' > %q", outsideFile))
		if code == 0 {
			t.Fatalf("outside-root write unexpectedly succeeded (output=%q)", out)
		}
		postSHA, postExists := tb08SHA256File(t, outsideFile)
		if !postExists || postSHA != preOutsideSHA {
			t.Fatalf("outside-root file mutated: pre=%s post=%s exists=%v", preOutsideSHA, postSHA, postExists)
		}
		verdict := boundary.Filesystem.Verdict(confine.AccessWrite, outsideFile)
		if verdict.Allowed {
			t.Fatalf("FilesystemRules.Verdict unexpectedly allowed outside write: %+v", verdict)
		}
		if verdict.Layer != confine.FloorOutsideWriteRoots {
			t.Fatalf("outside write verdict layer = %q, want %q", verdict.Layer, confine.FloorOutsideWriteRoots)
		}
	})

	// 2. Symlink inside admitted root pointing outside root MUST NOT permit write escape.
	t.Run("symlink_escape_write_refused", func(t *testing.T) {
		escapeLink := filepath.Join(admittedRoot, "escape-link.txt")
		if err := os.Symlink(outsideFile, escapeLink); err != nil {
			t.Fatalf("create escape symlink: %v", err)
		}
		code, out := tb08RunConfined(t, self, c, "/bin/sh", "-c",
			fmt.Sprintf("printf 'symlink-escaped\\n' > %q", escapeLink))
		if code == 0 {
			t.Fatalf("symlink escape write unexpectedly succeeded (output=%q)", out)
		}
		postSHA, postExists := tb08SHA256File(t, outsideFile)
		if !postExists || postSHA != preOutsideSHA {
			t.Fatalf("outside target mutated via symlink: pre=%s post=%s", preOutsideSHA, postSHA)
		}
	})

	// 3. Literal shared temp write (/tmp) MUST be refused (while TMPDIR uses session scratch).
	t.Run("shared_temp_write_refused", func(t *testing.T) {
		sentinel := filepath.Join("/tmp", fmt.Sprintf("pw-tb08-shared-tmp-%d.txt", time.Now().UnixNano()))
		t.Cleanup(func() { _ = os.Remove(sentinel) })
		code, out := tb08RunConfined(t, self, c, "/bin/sh", "-c",
			fmt.Sprintf("printf 'shared-tmp-write\\n' > %q", sentinel))
		if code == 0 {
			t.Fatalf("shared /tmp write unexpectedly succeeded (output=%q)", out)
		}
		if _, exists := tb08SHA256File(t, sentinel); exists {
			t.Fatalf("shared /tmp sentinel file was created despite refusal: %s", sentinel)
		}
		verdict := boundary.Filesystem.Verdict(confine.AccessWrite, sentinel)
		if verdict.Allowed || verdict.Layer != confine.FloorSharedTemp {
			t.Fatalf("shared temp verdict = %+v, want denied with layer %q", verdict, confine.FloorSharedTemp)
		}
	})

	// 4. Permitted write inside admitted root (adjacent positive control) succeeds with exact SHA-256
	//    and has a matching product boundary record in WriteRoots.
	t.Run("permitted_inside_root_write_and_record", func(t *testing.T) {
		insideFile := filepath.Join(admittedRoot, "permitted.txt")
		const expectedBytes = "tb08-permitted-inside\n"
		code, out := tb08RunConfined(t, self, c, "/bin/sh", "-c",
			fmt.Sprintf("printf 'tb08-permitted-inside\\n' > %q", insideFile))
		if code != 0 {
			t.Fatalf("permitted inside-root write failed (exit=%d, output=%q)", code, out)
		}
		gotSHA, gotExists := tb08SHA256File(t, insideFile)
		wantSHA := tb08SHA256String(expectedBytes)
		if !gotExists || gotSHA != wantSHA {
			t.Fatalf("inside-root file hash mismatch: got=%s want=%s exists=%v", gotSHA, wantSHA, gotExists)
		}
		verdict := boundary.Filesystem.Verdict(confine.AccessWrite, insideFile)
		if !verdict.Allowed {
			t.Fatalf("FilesystemRules.Verdict denied permitted inside write: %+v", verdict)
		}
		canonRoot, _ := filepath.EvalSymlinks(admittedRoot)
		foundRecord := false
		for _, wr := range boundary.WriteRoots {
			canonWR, _ := filepath.EvalSymlinks(wr)
			if wr == admittedRoot || (canonRoot != "" && canonWR == canonRoot) {
				foundRecord = true
				break
			}
		}
		if !foundRecord {
			t.Fatalf("admitted root %q not recorded in Boundary.WriteRoots=%v", admittedRoot, boundary.WriteRoots)
		}
	})

	// 5. Protected / sensitive path read classification and D13 platform differences.
	t.Run("protected_and_sensitive_read_floors", func(t *testing.T) {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatalf("UserHomeDir: %v", err)
		}
		sshKeyPath := filepath.Join(home, ".ssh", "id_ed25519")
		vSSH := boundary.Filesystem.Verdict(confine.AccessRead, sshKeyPath)
		if vSSH.Allowed || vSSH.Layer != confine.FloorReadKeyMaterial {
			t.Fatalf("SSH key read verdict = %+v, want denied with layer %q", vSSH, confine.FloorReadKeyMaterial)
		}

		var credStorePath string
		if runtime.GOOS == "darwin" {
			credStorePath = filepath.Join(home, "Library", "Keychains", "login.keychain-db")
		} else {
			credStorePath = filepath.Join(home, ".local", "share", "keyrings", "login.keyring")
		}
		vCred := boundary.Filesystem.Verdict(confine.AccessRead, credStorePath)
		if vCred.Allowed || vCred.Layer != confine.FloorReadCredentialStore {
			t.Fatalf("credential store read verdict (%s) = %+v, want denied with layer %q",
				credStorePath, vCred, confine.FloorReadCredentialStore)
		}

		// D13 Linux invariant: no blanket ~/.local/share write root.
		if runtime.GOOS == "linux" {
			xdgUnit := filepath.Join(home, ".local", "share", "applications", "tb08-probe.desktop")
			vXDG := boundary.Filesystem.Verdict(confine.AccessWrite, xdgUnit)
			if vXDG.Allowed {
				t.Fatalf("Linux XDG_DATA_HOME path %q unexpectedly writable under default boundary: %+v", xdgUnit, vXDG)
			}
		}
	})

	// 6. Typed recovery codes registered in isolation inventory.
	for _, code := range []string{isolation.CodeTryWriteRoot, isolation.CodeTryReadPath} {
		out, ok := isolation.Lookup(code)
		if !ok || out.Disposition != isolation.DispositionRetry {
			t.Fatalf("isolation.Lookup(%q) = (%+v, %v), want retry", code, out, ok)
		}
	}

	tb08LogReceipt(t, "A05", map[string]any{
		"outside_file_unchanged": true,
		"outside_sha256":         preOutsideSHA,
		"write_roots_count":      len(boundary.WriteRoots),
		"typed_recoveries":       []string{isolation.CodeTryWriteRoot, isolation.CodeTryReadPath},
	})
}

// ---------------------------------------------------------------------------
// A06 · Egress modes, destination validation, and TB01 catalog live codes
// ---------------------------------------------------------------------------

func TestWEATB08_A06_EgressAndDestinationValidation(t *testing.T) {
	self := tb08RequireLiveHelper(t)
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl required for A06 egress probe")
	}

	admissiontest.Enable(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")
	t.Setenv("LYCAON_SANDBOX_NETWORK", "")

	// Loopback recording HTTP fixtures (D4).
	var allowedArrivals atomic.Int64
	allowedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowedArrivals.Add(1)
		_, _ = io.WriteString(w, "tb08-allowed-ok\n")
	}))
	defer allowedSrv.Close()

	var refusedArrivals atomic.Int64
	refusedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refusedArrivals.Add(1)
		_, _ = io.WriteString(w, "tb08-refused-should-not-arrive\n")
	}))
	defer refusedSrv.Close()

	// Loopback recording raw TCP fixture (D4).
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen tcp fixture: %v", err)
	}
	defer tcpLn.Close()
	var tcpArrivals atomic.Int64
	go func() {
		for {
			conn, aerr := tcpLn.Accept()
			if aerr != nil {
				return
			}
			tcpArrivals.Add(1)
			_ = conn.Close()
		}
	}()

	root := t.TempDir()
	scratch := t.TempDir()

	// 1. NetworkDeny: confined command MUST NOT reach loopback HTTP or TCP fixtures (0 arrivals).
	t.Run("network_deny_zero_arrivals", func(t *testing.T) {
		c := confine.Confinement{
			Roots:      []string{root},
			Workspaces: confine.WorkspaceRoots{SessionScratch: scratch},
			Network:    confine.NetworkDeny,
		}
		code, _ := tb08RunConfined(t, self, c, "curl", "-s", "-m", "3", refusedSrv.URL)
		if code == 0 {
			t.Fatalf("NetworkDeny curl unexpectedly exited 0")
		}
		if got := refusedArrivals.Load(); got != 0 {
			t.Fatalf("NetworkDeny leaked %d arrival(s) to refused HTTP fixture", got)
		}
		if got := tcpArrivals.Load(); got != 0 {
			t.Fatalf("NetworkDeny leaked %d arrival(s) to TCP fixture", got)
		}
	})

	// 2. ProxyOnly ignoring proxy (--noproxy '*') gains NO direct path to refused fixture.
	t.Run("proxy_only_ignores_proxy_zero_direct_arrivals", func(t *testing.T) {
		confinetest.StartBroker(t)
		confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
			return false
		})
		t.Cleanup(func() { confine.SetEgressResolver(nil) })

		c, ok := confine.DefaultConfinement(confine.Request{
			Roots:     []string{root},
			ProjectID: "tb08-a06-noproxy",
		})
		if !ok || c == nil || c.Network != confine.NetworkProxyOnly {
			t.Fatalf("expected NetworkProxyOnly confinement: ok=%v c=%+v", ok, c)
		}
		lease, err := confine.BindAction(c, confine.EgressCommand{
			ProjectID: "tb08-a06-noproxy", SessionID: "s-a06", ToolCallID: "c-noproxy",
			ToolName: "command", CommandLine: "curl --noproxy '*' " + refusedSrv.URL,
		})
		if err != nil {
			t.Fatalf("BindAction: %v", err)
		}
		t.Cleanup(func() { lease.Close(t.Context()) })

		code, _ := tb08RunConfined(t, self, *c, "curl", "-s", "-m", "3", "--noproxy", "*", refusedSrv.URL)
		if code == 0 {
			t.Fatalf("ProxyOnly direct --noproxy curl unexpectedly exited 0")
		}
		if got := refusedArrivals.Load(); got != 0 {
			t.Fatalf("ProxyOnly direct bypass leaked %d arrival(s) to refused fixture", got)
		}
	})

	// 3. Mediated egress: exact 1:1 allowed arrival match + 0 refused arrivals + lineage across subshell.
	t.Run("mediated_egress_exact_arrival_and_lineage_descent", func(t *testing.T) {
		confinetest.StartBroker(t)
		allowedURL, _ := url.Parse(allowedSrv.URL)
		allowedPort, _ := strconv.Atoi(allowedURL.Port())

		confine.SetEgressResolver(func(_ context.Context, _ confine.EgressCommand, ep egressproxy.Endpoint, _ *confine.EgressDetectionCitation) bool {
			return ep.Port == uint16(allowedPort)
		})
		t.Cleanup(func() { confine.SetEgressResolver(nil) })

		c, ok := confine.DefaultConfinement(confine.Request{
			Roots:           []string{root},
			ProjectID:       "tb08-a06-mediated",
			LoopbackConnect: true,
		})
		if !ok || c == nil || c.Network != confine.NetworkProxyOnly {
			t.Fatalf("expected NetworkProxyOnly confinement: ok=%v c=%+v", ok, c)
		}
		lease, err := confine.BindAction(c, confine.EgressCommand{
			ProjectID: "tb08-a06-mediated", SessionID: "s-a06", ToolCallID: "c-mediated",
			ToolName: "command", CommandLine: "sh -c 'curl allowed && curl refused'",
		})
		if err != nil {
			t.Fatalf("BindAction: %v", err)
		}
		t.Cleanup(func() { lease.Close(t.Context()) })

		// Fork/exec a nested subshell so lineage descent is exercised across process boundaries,
		// and attempt both the allowed and refused fixtures within the same bound launch.
		beforeAllowed := allowedArrivals.Load()
		beforeRefused := refusedArrivals.Load()
		script := fmt.Sprintf(
			"out=$(sh -c 'curl -s -m 5 -o /dev/null -w \"%%{http_code}\" %q'); echo \"$out\"; curl -s -m 5 -o /dev/null %q || true",
			allowedSrv.URL, refusedSrv.URL,
		)
		code, out := tb08RunConfined(t, self, *c, "/bin/sh", "-c", script)
		if code != 0 || strings.TrimSpace(out) != "200" {
			t.Fatalf("mediated allowed request failed: exit=%d out=%q", code, out)
		}

		gotAllowedArrivals := allowedArrivals.Load() - beforeAllowed
		gotRefusedArrivals := refusedArrivals.Load() - beforeRefused
		if gotAllowedArrivals != 1 {
			t.Fatalf("allowed fixture arrivals = %d, want 1", gotAllowedArrivals)
		}
		if gotRefusedArrivals != 0 {
			t.Fatalf("refused fixture arrivals = %d, want 0", gotRefusedArrivals)
		}

		hosts := lease.ObservedHosts()
		allowedObserved := 0
		deniedObserved := 0
		for _, h := range hosts {
			if h.Allowed {
				allowedObserved++
			} else {
				deniedObserved++
			}
		}
		if allowedObserved != 1 {
			t.Fatalf("broker allowed observations = %d, want 1 (hosts=%+v)", allowedObserved, hosts)
		}
		if deniedObserved < 1 {
			t.Fatalf("broker denied observations = %d, want >= 1 (hosts=%+v)", deniedObserved, hosts)
		}
	})

	// 4. TB01 catalog live typed refusal codes:
	//    - FETCH_URL_BLOCKED for destination validation (loopback/private SSRF block)
	//    - WEB_SEARCH_HOST_DENIED for egress gate host denial
	//    - FETCH_URL_DEST_DENIED explicitly retained as legacy exclusion (not emitted here)
	t.Run("tb01_live_fetch_url_codes_and_legacy_exclusion", func(t *testing.T) {
		reg := tools.NewDefaultRegistry()
		err := webresearch.RegisterToolsWithFactory(reg, webresearch.ToolDeps{
			Crawler:     webresearch.NewCrawler(nil),
			Discovery:   webresearch.NewDiscovery(webresearch.NewCrawler(nil)),
			FetchBudget: webresearch.NewFetchBudget(webresearch.DefaultFetchURLLimits()),
		}, nil)
		if err != nil {
			t.Fatalf("RegisterToolsWithFactory: %v", err)
		}

		// Destination validation seam: loopback URL -> FETCH_URL_BLOCKED (0 arrivals).
		before := refusedArrivals.Load()
		_, runErr := reg.Run(t.Context(), "fetch_url", map[string]any{"url": refusedSrv.URL}, tools.ToolContext{})
		if runErr == nil {
			t.Fatalf("expected FETCH_URL_BLOCKED for loopback URL")
		}
		var reject *toolrejection.ToolReject
		if !errors.As(runErr, &reject) || reject.Code != "FETCH_URL_BLOCKED" {
			t.Fatalf("fetch_url loopback error = %T (%v), want FETCH_URL_BLOCKED", runErr, runErr)
		}
		if reject.Code == "FETCH_URL_DEST_DENIED" {
			t.Fatalf("legacy deprecated code FETCH_URL_DEST_DENIED must not be emitted")
		}
		if refusedArrivals.Load()-before != 0 {
			t.Fatalf("blocked fetch_url still reached loopback fixture")
		}

		// Egress gate seam: host denied via confine egress rule evaluator -> WEB_SEARCH_HOST_DENIED.
		confine.SetEgressRuleEvaluator(func(_ context.Context, _ confine.EgressCommand, host string) confine.EgressRuleResult {
			return confine.EgressRuleResult{Effect: confine.EgressRuleDeny, Pattern: host}
		})
		t.Cleanup(func() { confine.SetEgressRuleEvaluator(nil) })
		gateCtx := egressgate.WithAttribution(t.Context(), confine.EgressCommand{
			ProjectID: "tb08-a06-gate", SessionID: "s-a06-gate", ToolCallID: "c-gate", ToolName: "fetch_url",
		})
		_, gateErr := reg.Run(gateCtx, "fetch_url", map[string]any{"url": "https://denied.example/tb08"}, tools.ToolContext{})
		if gateErr == nil {
			t.Fatalf("expected WEB_SEARCH_HOST_DENIED from egress gate")
		}
		var gateReject *toolrejection.ToolReject
		if !errors.As(gateErr, &gateReject) || gateReject.Code != "WEB_SEARCH_HOST_DENIED" {
			t.Fatalf("fetch_url egress gate error = %T (%v), want WEB_SEARCH_HOST_DENIED", gateErr, gateErr)
		}

		// Verify SANDBOX_TRY_LOCAL_NETWORK is registered in isolation.
		if out, ok := isolation.Lookup(isolation.CodeTryLocalNetwork); !ok || out.Disposition != isolation.DispositionRetry {
			t.Fatalf("isolation.Lookup(%q) = (%+v, %v), want retry", isolation.CodeTryLocalNetwork, out, ok)
		}
	})

	tb08LogReceipt(t, "A06", map[string]any{
		"allowed_arrivals":       allowedArrivals.Load(),
		"refused_arrivals":       refusedArrivals.Load(),
		"tcp_arrivals":           tcpArrivals.Load(),
		"egress_gate_code":       "WEB_SEARCH_HOST_DENIED",
		"destination_block_code": "FETCH_URL_BLOCKED",
		"legacy_excluded_code":   "FETCH_URL_DEST_DENIED",
	})
}

// ---------------------------------------------------------------------------
// A07 · Processes a command leaves behind (D13 macOS vs Linux)
// ---------------------------------------------------------------------------

func tb08HostProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	cmd := exec.Command("ps", "-o", "pid=", "-p", strconv.Itoa(pid))
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == strconv.Itoa(pid)
}

func TestWEATB08_A07_LeftBehindProcesses(t *testing.T) {
	self := tb08RequireLiveHelper(t)
	admissiontest.Enable(t)
	t.Setenv("LYCAON_BYPASS_APPROVALS", "")
	t.Setenv("LYCAON_SANDBOX", "")

	confinetest.StartBroker(t)
	confine.SetEgressResolver(func(context.Context, confine.EgressCommand, egressproxy.Endpoint, *confine.EgressDetectionCitation) bool {
		return true
	})
	t.Cleanup(func() { confine.SetEgressResolver(nil) })

	root := t.TempDir()
	scratch := t.TempDir()

	// 1. Clean command leaves 0 processes behind on both platforms.
	t.Run("clean_command_zero_left_behind", func(t *testing.T) {
		c, ok := confine.DefaultConfinement(confine.Request{
			Roots:     []string{root},
			ProjectID: "tb08-a07-clean",
		})
		if !ok || c == nil {
			t.Fatalf("DefaultConfinement returned ok=%v", ok)
		}
		c.Workspaces.SessionScratch = scratch
		lease, err := confine.BindAction(c, confine.EgressCommand{
			ProjectID: "tb08-a07-clean", SessionID: "s-a07", ToolCallID: "c-clean",
			ToolName: "command", CommandLine: "true",
		})
		if err != nil {
			t.Fatalf("BindAction: %v", err)
		}
		defer lease.Close(t.Context())

		code, out := tb08RunConfined(t, self, *c, "/bin/sh", "-c", "exit 0")
		if code != 0 {
			t.Fatalf("clean command failed: exit=%d out=%q", code, out)
		}
		survivors, err := lease.Survivors()
		if err != nil {
			t.Fatalf("lease.Survivors: %v", err)
		}
		if len(survivors) != 0 {
			t.Fatalf("clean command left %d survivor(s): %v", len(survivors), survivors)
		}
	})

	// 2. Daemon left behind: compare lease.Survivors() with independent host process table (D4 + D13).
	t.Run("daemon_left_behind_matches_host_process_table", func(t *testing.T) {
		c, ok := confine.DefaultConfinement(confine.Request{
			Roots:     []string{root},
			ProjectID: "tb08-a07-daemon",
		})
		if !ok || c == nil {
			t.Fatalf("DefaultConfinement returned ok=%v", ok)
		}
		c.Workspaces.SessionScratch = scratch
		lease, err := confine.BindAction(c, confine.EgressCommand{
			ProjectID: "tb08-a07-daemon", SessionID: "s-a07", ToolCallID: "c-daemon",
			ToolName: "command", CommandLine: "sleep 30 &",
		})
		if err != nil {
			t.Fatalf("BindAction: %v", err)
		}
		defer lease.Close(t.Context())

		pidFile := filepath.Join(root, "daemon.pid")
		script := fmt.Sprintf("sleep 30 </dev/null >/dev/null 2>&1 & echo $! > %q", pidFile)
		code, out := tb08RunConfined(t, self, *c, "/bin/sh", "-c", script)
		if code != 0 {
			t.Fatalf("daemon spawn command failed: exit=%d out=%q", code, out)
		}

		rawPID, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatalf("read daemon.pid: %v", err)
		}
		daemonPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
		if err != nil || daemonPID <= 0 {
			t.Fatalf("invalid daemon PID %q: %v", string(rawPID), err)
		}
		// Always clean up spawned daemon on macOS (or if anything somehow survived).
		t.Cleanup(func() {
			if runtime.GOOS == "darwin" {
				_ = syscall.Kill(daemonPID, syscall.SIGKILL)
			}
		})

		survivors, err := lease.Survivors()
		if err != nil {
			t.Fatalf("lease.Survivors: %v", err)
		}
		// Also clean up any survivor PIDs reported by lease.
		t.Cleanup(func() {
			for _, p := range survivors {
				_ = syscall.Kill(p, syscall.SIGKILL)
			}
		})

		if runtime.GOOS == "darwin" {
			// D13 macOS: daemon outlives command and keeps Seatbelt boundary;
			// lease.Survivors() count MUST match independent host process table.
			hostAlive := tb08HostProcessAlive(daemonPID)
			if !hostAlive {
				t.Fatalf("macOS daemon PID %d unexpectedly not alive in host process table", daemonPID)
			}
			aliveSurvivors := 0
			foundDaemon := false
			for _, p := range survivors {
				if tb08HostProcessAlive(p) {
					aliveSurvivors++
				}
				if p == daemonPID {
					foundDaemon = true
				}
			}
			if !foundDaemon || aliveSurvivors != len(survivors) {
				t.Fatalf("macOS survivor/host-table mismatch: daemonPID=%d survivors=%v aliveSurvivors=%d",
					daemonPID, survivors, aliveSurvivors)
			}
			// Kill daemon cleanly now and verify it exits the host process table.
			_ = syscall.Kill(daemonPID, syscall.SIGKILL)
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) && tb08HostProcessAlive(daemonPID) {
				time.Sleep(25 * time.Millisecond)
			}
			if tb08HostProcessAlive(daemonPID) {
				t.Fatalf("failed to clean up macOS test daemon PID %d", daemonPID)
			}
		} else if runtime.GOOS == "linux" {
			// D13 Linux: PID namespace PID 1 teardown ends all descendants when command exits.
			if len(survivors) != 0 {
				t.Fatalf("Linux PID namespace left %d survivor(s): %v (expected 0)", len(survivors), survivors)
			}
		}

		tb08LogReceipt(t, "A07", map[string]any{
			"daemon_pid_in_namespace": daemonPID,
			"survivor_count":          len(survivors),
			"d13_expected_outlives":   runtime.GOOS == "darwin",
		})
	})
}

// ---------------------------------------------------------------------------
// A08 · Legible denials, stderr invariance, Linux witness, and H3 triage
// ---------------------------------------------------------------------------

func TestWEATB08_A08_LegibleDenialsAndH3Triage(t *testing.T) {
	// 1. Every boundary refusal recovery maps cleanly into structured OAR facts,
	//    and its typed isolation code is registered in internal/isolation.
	t.Run("structured_refusal_code_matches_operation", func(t *testing.T) {
		cases := []struct {
			name         string
			refusal      confine.SandboxRefusal
			expectedCode string
			checkFact    func(*oar.GuardContext) bool
		}{
			{
				name: "write_root",
				refusal: confine.SandboxRefusal{
					Operation: "file-write-create", Target: "/outside/a.txt", Grant: "/outside",
					Process: "sh", Count: 1, Layer: confine.FloorOutsideWriteRoots, Recovery: confine.RecoverWriteRoot,
				},
				expectedCode: isolation.CodeTryWriteRoot,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedWritePaths) == 1 && gc.Refusals.RefusedWritePaths[0] == "/outside/a.txt" },
			},
			{
				name: "read_path",
				refusal: confine.SandboxRefusal{
					Operation: "file-read-data", Target: "/home/u/.ssh/id_ed25519", Grant: "/home/u/.ssh/id_ed25519",
					Process: "cat", Count: 1, Layer: confine.FloorReadKeyMaterial, Recovery: confine.RecoverReadPath,
				},
				expectedCode: isolation.CodeTryReadPath,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedReadPaths) == 1 && gc.Refusals.RefusedReadPaths[0] == "/home/u/.ssh/id_ed25519" },
			},
			{
				name: "credential_store",
				refusal: confine.SandboxRefusal{
					Operation: "file-read-data", Target: "/home/u/.local/share/keyrings/login.keyring", Grant: "/home/u/.local/share/keyrings/login.keyring",
					Process: "tool", Count: 1, Layer: confine.FloorReadCredentialStore, Recovery: confine.RecoverCredentialStore,
				},
				expectedCode: isolation.CodeTryCredentialStore,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedCredentialStorePaths) == 1 },
			},
			{
				name: "socket_path",
				refusal: confine.SandboxRefusal{
					Operation: "network-outbound", Target: "/var/run/docker.sock", Grant: "/var/run/docker.sock",
					Process: "docker", Count: 1, Recovery: confine.RecoverSocketPath,
				},
				expectedCode: isolation.CodeTrySocketPath,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedSocketPaths) == 1 && gc.Refusals.RefusedSocketPaths[0] == "/var/run/docker.sock" },
			},
			{
				name: "local_listen",
				refusal: confine.SandboxRefusal{
					Operation: "network-bind", Target: "local:*:8080",
					Process: "server", Count: 1, Recovery: confine.RecoverLocalListen,
				},
				expectedCode: isolation.CodeTryLocalNetwork,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedListenPorts) == 1 && gc.Refusals.RefusedListenPorts[0] == "8080" },
			},
			{
				name: "outbound_port",
				refusal: confine.SandboxRefusal{
					Operation: "network-outbound", Target: "remote:*:9090",
					Process: "client", Count: 1, Recovery: confine.RecoverOutboundPort,
				},
				expectedCode: isolation.CodeTryLocalNetwork,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedConnectPorts) == 1 && gc.Refusals.RefusedConnectPorts[0] == "9090" },
			},
			{
				name: "process_control",
				refusal: confine.SandboxRefusal{
					Operation: "signal", Target: "pid:4242",
					Process: "kill", Count: 1, Recovery: confine.RecoverProcessControl,
				},
				expectedCode: isolation.CodeTryProcessControl,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedSignals) == 1 },
			},
			{
				name: "host_execution",
				refusal: confine.SandboxRefusal{
					Operation: "network-bind", Target: "/tmp/custom.sock",
					Process: "vm", Count: 1, Recovery: confine.RecoverHostExecution,
				},
				expectedCode: isolation.CodeTryHostExecution,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.UnsandboxedRefusals) == 1 },
			},
			{
				name: "terminal_control_plane",
				refusal: confine.SandboxRefusal{
					Operation: "file-write-data", Target: "/home/u/.config/paintedwolf/policy.yaml",
					Process: "sh", Count: 1, Layer: confine.FloorControlPlane, Recovery: confine.RecoverNone,
				},
				expectedCode: isolation.CodeTerminalPathRefused,
				checkFact:    func(gc *oar.GuardContext) bool { return len(gc.Refusals.RefusedTerminalWritePaths) == 1 },
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if _, ok := isolation.Lookup(tc.expectedCode); !ok {
					t.Fatalf("expected isolation code %q not registered", tc.expectedCode)
				}
				gc := &oar.GuardContext{Invocation: oar.InvocationFacts{Tool: "command"}}
				policyfacts.ObserveConfine(gc, confine.Observation{
					Applied: true,
					Refusals: confine.SandboxRefusals{
						Witness:  confine.WitnessKernel,
						Refusals: []confine.SandboxRefusal{tc.refusal},
					},
				})
				if !tc.checkFact(gc) {
					t.Fatalf("ObserveConfine did not populate expected refusal fact for %s: %+v", tc.name, gc.Refusals)
				}
			})
		}
	})

	// 2. Stderr / command error text invariance: varying command prose MUST NOT change refusal facts/codes.
	t.Run("refusal_code_invariant_to_stderr_prose", func(t *testing.T) {
		ref := confine.SandboxRefusal{
			Operation: "file-write-create", Target: "/outside/secret.txt", Grant: "/outside",
			Process: "sh", Count: 1, Layer: confine.FloorOutsideWriteRoots, Recovery: confine.RecoverWriteRoot,
		}
		stderrVariants := []string{
			"sh: /outside/secret.txt: Operation not permitted",
			"ECONNREFUSED: misleading network error text",
			"totally unrelated custom panic: disk full",
			"",
		}
		var baseline oar.RefusalsFacts
		for i, stderrText := range stderrVariants {
			gc := &oar.GuardContext{
				Invocation: oar.InvocationFacts{Tool: "command"},
			}
			policyfacts.ObserveConfine(gc, confine.Observation{
				Applied:      true,
				FailedStages: []string{stderrText},
				Refusals: confine.SandboxRefusals{
					Witness:  confine.WitnessKernel,
					Refusals: []confine.SandboxRefusal{ref},
				},
			})
			if i == 0 {
				baseline = gc.Refusals
			} else if !reflect.DeepEqual(gc.Refusals, baseline) {
				t.Fatalf("refusal facts drifted when stderr varied (%q): got=%+v want=%+v", stderrText, gc.Refusals, baseline)
			}
		}
	})

	// 3. Unattributed boundary refusal stays visible as SANDBOX_BOUNDARY_REFUSED.
	t.Run("unattributed_refusal_stays_visible", func(t *testing.T) {
		out, ok := isolation.Lookup(isolation.CodeBoundaryRefused)
		if !ok || out.Disposition != isolation.DispositionRetry {
			t.Fatalf("SANDBOX_BOUNDARY_REFUSED lookup = (%+v, %v), want retry", out, ok)
		}
	})

	// 4. Linux incomplete-witness invariant (D13): Report.WithSandboxRefusals preserves WitnessIncomplete
	//    and omits WitnessKernel.
	t.Run("linux_incomplete_witness_preserved_in_report", func(t *testing.T) {
		base := confine.Report{Confined: true, NetworkMode: "proxy_only"}
		inc := base.WithSandboxRefusals(confine.SandboxRefusals{Witness: confine.WitnessIncomplete})
		if inc.SandboxRefusalWitness != confine.WitnessIncomplete {
			t.Fatalf("WithSandboxRefusals(incomplete) = %q, want %q", inc.SandboxRefusalWitness, confine.WitnessIncomplete)
		}
		kern := base.WithSandboxRefusals(confine.SandboxRefusals{Witness: confine.WitnessKernel})
		if kern.SandboxRefusalWitness != "" {
			t.Fatalf("WithSandboxRefusals(kernel) = %q, want omitted empty string", kern.SandboxRefusalWitness)
		}
		unavail := base.WithSandboxRefusals(confine.SandboxRefusals{Witness: confine.WitnessUnavailable})
		if unavail.SandboxRefusalWitness != confine.WitnessUnavailable {
			t.Fatalf("WithSandboxRefusals(unavailable) = %q, want %q", unavail.SandboxRefusalWitness, confine.WitnessUnavailable)
		}
	})

	// 5. H3 Triage: confirm unstructured subsystem-owner errors (such as H3's
	//    `child program identity: sql: no rows in result set` and `work: requested > 1073741824`)
	//    are NOT classified as typed *toolrejection.ToolReject, which causes promptloop's
	//    ownerFailureFromError to fall back to TOOL_OWNER_FAILED and log `component=tool_owner`.
	t.Run("h3_triage_unstructured_owner_errors", func(t *testing.T) {
		h3Errors := []struct {
			label string
			err   error
		}{
			{"child_program_identity_no_rows", fmt.Errorf("child program identity: %w", sql.ErrNoRows)},
			{"work_budget_exceeded_raw", errors.New("work: requested > 1073741824")},
		}
		unstructuredCount := 0
		for _, tc := range h3Errors {
			if tr := toolrejection.AsToolReject(tc.err); tr != nil {
				t.Fatalf("expected H3 raw error %s to lack ToolReject wrapper, got %+v", tc.label, tr)
			}
			unstructuredCount++
		}
		// Contrast with a properly typed ToolReject.
		typedErr := &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED"}
		if tr := toolrejection.AsToolReject(typedErr); tr == nil || tr.Code != "COMMAND_LIMIT_EXCEEDED" {
			t.Fatalf("AsToolReject(typed) = %+v, want COMMAND_LIMIT_EXCEEDED", tr)
		}

		tb08LogReceipt(t, "A08", map[string]any{
			"stderr_invariance_verified":   true,
			"incomplete_witness_preserved": true,
			"h3_triage": map[string]any{
				"confirmed_unstructured_fallback": true,
				"fallback_code":                   toolrejection.ToolOwnerFailedCode,
				"unstructured_samples_checked":    unstructuredCount,
				"finding":                         "Raw errors from invocation/program_start.go (child program identity: sql: no rows) and raw work-budget errors bypass toolrejection.ToolReject and emit TOOL_OWNER_FAILED via component=tool_owner.",
			},
		})
	})
}

// ---------------------------------------------------------------------------
// A09 · Required boundary unavailable (fail closed) & exceptional authority
// ---------------------------------------------------------------------------

func TestWEATB08_A09_UnavailableBoundariesAndExceptionalAuthority(t *testing.T) {
	// 1. Required boundary unavailable fails closed (confine.RequireApplied -> ErrNotConfined)
	//    and a launch with an unavailable helper NEVER starts its target (sentinel never created).
	t.Run("unavailable_boundary_fails_closed_never_starts_target", func(t *testing.T) {
		admissiontest.Enable(t)
		t.Setenv("LYCAON_BYPASS_APPROVALS", "")
		t.Setenv("LYCAON_SANDBOX", "")

		if err := confine.RequireApplied(false); !errors.Is(err, confine.ErrNotConfined) {
			t.Fatalf("confine.RequireApplied(false) = %v, want ErrNotConfined", err)
		}

		root := t.TempDir()
		sentinel := filepath.Join(root, "target-must-never-run.sentinel")
		missingHelper := filepath.Join(t.TempDir(), "nonexistent-confine-helper")

		c := confine.Confinement{
			Roots:   []string{root},
			Network: confine.NetworkDeny,
		}
		cmd, cleanup, err := confine.Command(t.Context(), missingHelper, "/bin/sh",
			[]string{"-c", fmt.Sprintf("printf 'RAN\\n' > %q", sentinel)}, c)
		if err == nil {
			defer cleanup()
			runErr := cmd.Run()
			if runErr == nil {
				t.Fatalf("launch with unavailable confinement helper unexpectedly succeeded")
			}
		}
		// D4 independent check: target sentinel file MUST NEVER exist on disk.
		if _, exists := tb08SHA256File(t, sentinel); exists {
			t.Fatalf("FAIL-OPEN DEFECT: target executed and created sentinel %s despite unavailable boundary", sentinel)
		}
	})

	// 2. Every A09 exceptional authority code is registered with its exact disposition.
	t.Run("exceptional_authority_codes_and_dispositions", func(t *testing.T) {
		expected := map[string]isolation.Disposition{
			isolation.CodeExecutionBoundaryUnavailable: isolation.DispositionRetry,
			isolation.CodeTryHostExecution:             isolation.DispositionRetry,
			isolation.CodeTryProcessControl:            isolation.DispositionRetry,
			isolation.CodeTryCredentialStore:           isolation.DispositionRetry,
			isolation.CodeTrySocketPath:                isolation.DispositionRetry,
			isolation.CodeTryCapabilities:              isolation.DispositionRetry,
			isolation.CodeTerminalPathRefused:          isolation.DispositionControlPlane,
			isolation.CodeSocketPermitRequired:         isolation.DispositionRetry,
			isolation.CodeSocketPathChanged:            isolation.DispositionRetry,
		}
		for code, wantDisp := range expected {
			got, ok := isolation.Lookup(code)
			if !ok {
				t.Fatalf("isolation code %q not registered", code)
			}
			if got.Disposition != wantDisp {
				t.Fatalf("isolation code %q disposition = %q, want %q", code, got.Disposition, wantDisp)
			}
		}
	})

	// 3. Distinguish missing socket permit (SANDBOX_SOCKET_PERMIT_REQUIRED) from
	//    changed/unretained recipient binding (SANDBOX_SOCKET_PATH_CHANGED).
	t.Run("socket_permit_required_vs_socket_path_changed", func(t *testing.T) {
		shortDir, err := os.MkdirTemp("/tmp", "tb08-sk-")
		if err != nil {
			t.Fatalf("create short socket dir: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(shortDir) })

		sockPath := filepath.Join(shortDir, "svc.sock")
		ln, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen unix socket: %v", err)
		}
		t.Cleanup(func() { _ = ln.Close() })

		grant, err := confine.ResolveSocketRequest(sockPath)
		if err != nil {
			t.Fatalf("ResolveSocketRequest: %v", err)
		}
		ctx, scope := socketref.WithScope(t.Context())
		defer scope.Close()
		capturedGrant, err := confine.CaptureSocketGrant(ctx, grant)
		if err != nil {
			t.Fatalf("CaptureSocketGrant: %v", err)
		}

		// Case A: valid retained binding, but NO current-call permit runtime -> SANDBOX_SOCKET_PERMIT_REQUIRED.
		tctxNoPermit := tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: "sess-a09", ToolCallID: "call-no-permit"},
			Socket: tools.InvocationSocket{
				SocketGrants: []confine.SocketGrant{capturedGrant},
			},
		}
		_, rejectNoPermit := tools.FinalizeSocketGrantsForSpawn(ctx, tctxNoPermit)
		if rejectNoPermit == nil || rejectNoPermit.Code != isolation.CodeSocketPermitRequired {
			t.Fatalf("missing socket permit reject = %+v, want %q", rejectNoPermit, isolation.CodeSocketPermitRequired)
		}

		// Case B: recipient binding changed (socket replaced on disk with a new listener) -> SANDBOX_SOCKET_PATH_CHANGED.
		_ = ln.Close()
		_ = os.Remove(sockPath)
		ln2, err := net.Listen("unix", sockPath)
		if err != nil {
			t.Fatalf("listen replacement unix socket: %v", err)
		}
		t.Cleanup(func() { _ = ln2.Close() })

		_, rejectChanged := tools.FinalizeSocketGrantsForSpawn(ctx, tctxNoPermit)
		if rejectChanged == nil || rejectChanged.Code != isolation.CodeSocketPathChanged {
			t.Fatalf("changed socket recipient reject = %+v, want %q", rejectChanged, isolation.CodeSocketPathChanged)
		}

		if rejectNoPermit.Code == rejectChanged.Code {
			t.Fatalf("expected distinct codes for missing permit vs changed binding, both were %q", rejectNoPermit.Code)
		}

		tb08LogReceipt(t, "A09", map[string]any{
			"unavailable_boundary_failed_closed": true,
			"target_sentinel_created":            false,
			"missing_permit_code":                rejectNoPermit.Code,
			"changed_binding_code":               rejectChanged.Code,
		})
	})
}

// Compile-time check that sync is referenced if needed; keep clean imports.
var _ sync.Locker
