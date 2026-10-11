package contract

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/egressproxy"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/wiring"
)

// Escape tests exercise applied profiles and live mediation.

func requireSeatbeltEscape(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "darwin" || testing.Short() || !confine.Available() {
		t.Skip("darwin + seatbelt sandbox required")
	}
	self, err := os.Executable()
	testutil.FailErr(t, "Executable", err)
	return self
}

func confinedEscapeExit(t *testing.T, self string, c confine.Confinement, name string, args ...string) int {
	t.Helper()
	cmd, cleanup, err := confine.Command(context.Background(), self, name, args, c)
	testutil.FailErr(t, "Command", err)
	defer cleanup()
	_ = cmd.Run()
	return cmd.ProcessState.ExitCode()
}

func assertInProjectWriteOK(t *testing.T, self string, c confine.Confinement, proj string) {
	t.Helper()
	ok := filepath.Join(proj, "escape-suite-ok.txt")
	if code := confinedEscapeExit(t, self, c, "/usr/bin/touch", ok); code != 0 {
		t.Fatalf("positive control: in-project write must succeed, exit=%d", code)
	}
}

func isProfileStagingCandidate(name string) bool {
	return strings.HasPrefix(name, "lycaon-sbpl-") ||
		(strings.HasSuffix(name, ".sb") && strings.Contains(name, "lycaon"))
}

func TestConfinementIntegrityConfigTokenUnreadable(t *testing.T) {
	self := requireSeatbeltEscape(t)
	cfg := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", cfg)
	tokenPath := filepath.Join(cfg, "api.token")
	testutil.FailErr(t, "write api.token", os.WriteFile(tokenPath, []byte("escape-bearer"), 0o600))
	draftDir := filepath.Join(cfg, "drafts", "escape-f1")
	testutil.FailErr(t, "mkdir drafts", os.MkdirAll(draftDir, 0o700))
	draftFile := filepath.Join(draftDir, "ok.txt")
	testutil.FailErr(t, "write draft", os.WriteFile(draftFile, []byte("draft-ok"), 0o600))
	browserCache := filepath.Join(cfg, "browser-cache")
	testutil.FailErr(t, "mkdir browser-cache", os.MkdirAll(browserCache, 0o700))
	browserFile := filepath.Join(browserCache, "ok.bin")
	testutil.FailErr(t, "write browser-cache", os.WriteFile(browserFile, []byte("chrome"), 0o600))

	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}
	if code := confinedEscapeExit(t, self, c, "/bin/cat", tokenPath); code == 0 {
		t.Fatal("confined read of {configdir}/api.token must be denied")
	}
	if code := confinedEscapeExit(t, self, c, "/bin/cat", draftFile); code != 0 {
		t.Fatalf("positive control: {configdir}/drafts must stay readable, exit=%d", code)
	}
	if code := confinedEscapeExit(t, self, c, "/bin/cat", browserFile); code != 0 {
		t.Fatalf("positive control: {configdir}/browser-cache must stay readable, exit=%d", code)
	}
	assertInProjectWriteOK(t, self, c, proj)
}

func TestConfinementIntegrityNoProfileStagingFile(t *testing.T) {
	self := requireSeatbeltEscape(t)
	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}

	tmp := os.TempDir()
	before := map[string]bool{}
	entries, err := os.ReadDir(tmp)
	testutil.FailErr(t, "ReadDir temp before", err)
	for _, e := range entries {
		if isProfileStagingCandidate(e.Name()) {
			before[e.Name()] = true
		}
	}

	cmd, cleanup, err := confine.Command(context.Background(), self, "/bin/echo", []string{"ok"}, c)
	testutil.FailErr(t, "Command", err)
	defer cleanup()

	for _, arg := range cmd.Args {
		if strings.HasSuffix(arg, ".sb") || strings.Contains(arg, "lycaon-sbpl-") {
			t.Fatalf("profile must not be a staging path arg, got %v", cmd.Args)
		}
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("profile must travel on one ExtraFiles pipe, got %d", len(cmd.ExtraFiles))
	}

	after, err := os.ReadDir(tmp)
	testutil.FailErr(t, "ReadDir temp after", err)
	for _, e := range after {
		name := e.Name()
		if isProfileStagingCandidate(name) && !before[name] {
			t.Fatalf("launching Command created profile staging candidate in TempDir: %s", name)
		}
	}
	_ = cmd.Run()
	assertInProjectWriteOK(t, self, c, proj)
}

func TestConfinementIntegrityLauncherUntamperable(t *testing.T) {
	self := requireSeatbeltEscape(t)
	exe := self
	if rp, err := filepath.EvalSymlinks(self); err == nil {
		exe = rp
	}
	launcherDir := filepath.Dir(exe)
	c := confine.Confinement{Roots: []string{launcherDir}}

	sibling := filepath.Join(launcherDir, "lycaon_escape_f3_sibling.txt")
	_ = os.Remove(sibling)
	t.Cleanup(func() { _ = os.Remove(sibling) })
	if code := confinedEscapeExit(t, self, c, "/usr/bin/touch", sibling); code != 0 {
		t.Fatalf("positive control: sibling write must succeed, exit=%d", code)
	}

	q := func(s string) string {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	if code := confinedEscapeExit(t, self, c, "/bin/bash", "-c", "printf x >"+q(exe)); code == 0 {
		t.Fatal("write over launcher must be denied")
	}
	if code := confinedEscapeExit(t, self, c, "/bin/bash", "-c", ": >"+q(exe)); code == 0 {
		t.Fatal("truncate of launcher must be denied")
	}
	if code := confinedEscapeExit(t, self, c, "/bin/rm", "-f", exe); code == 0 {
		t.Fatal("unlink of launcher must be denied")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("launcher must still exist: %v", err)
	}
	if code := confinedEscapeExit(t, self, c, "/bin/dd", "if=/dev/zero", "of="+exe, "bs=1", "count=1"); code == 0 {
		t.Fatal("create-at/overwrite of launcher must be denied")
	}
	src := filepath.Join(launcherDir, "lycaon_escape_f3_rename_src")
	testutil.FailErr(t, "write rename src", os.WriteFile(src, []byte("x"), 0o600))
	t.Cleanup(func() { _ = os.Remove(src) })
	if code := confinedEscapeExit(t, self, c, "/bin/mv", "-f", src, exe); code == 0 {
		t.Fatal("rename-over launcher must be denied")
	}
}

func TestConfinementIntegrityUnattributedProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(upstream.Close)

	deciderCalled := false
	var resolveOK bool
	p := newTestBrokerFor(func(_ context.Context, _ string, _ egressproxy.Endpoint) bool {
		deciderCalled = true
		return true
	}, func(_, _ netip.AddrPort) (egressproxy.Peer, bool) {
		if !resolveOK {
			return egressproxy.Peer{}, false
		}
		return egressproxy.Peer{Lineage: "live-command", Leased: true}, true
	})
	p.SetDialEndpointForTest(func(ctx context.Context, _ egressproxy.Endpoint) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "tcp", upstream.Listener.Addr().String())
	})
	_, err := p.Start()
	testutil.FailErr(t, "proxy Start", err)
	t.Cleanup(func() { _ = p.Close() })

	pu, err := url.Parse("http://" + p.Addr())
	testutil.FailErr(t, "parse proxy", err)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get(upstream.URL)
	testutil.FailErr(t, "proxy GET", err)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unattributed caller must get 403, got %d", resp.StatusCode)
	}
	if deciderCalled {
		t.Fatal("decider must not run for unattributed callers")
	}

	resolveOK = true
	liveResp, err := client.Get(upstream.URL)
	testutil.FailErr(t, "live GET", err)
	body, _ := io.ReadAll(liveResp.Body)
	_ = liveResp.Body.Close()
	if liveResp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("positive control: attributed peer must forward, got %d %q", liveResp.StatusCode, body)
	}
}

func TestConfinementIntegrityForeignVarFoldersWriteDenied(t *testing.T) {
	self := requireSeatbeltEscape(t)
	proj := t.TempDir()
	c := confine.Confinement{Roots: []string{proj}}

	tmp := os.TempDir()
	// Sibling temporary subtrees remain read-only.
	systemTmp, err := exec.Command("/usr/bin/getconf", "DARWIN_USER_TEMP_DIR").Output()
	testutil.FailErr(t, "resolve system temporary directory", err)
	foreign, err := os.MkdirTemp(filepath.Dir(filepath.Dir(strings.TrimSpace(string(systemTmp)))), "paintedwolf-escape-foreign-") //nolint:usetesting // Probe must be outside this process's temporary subtree.
	testutil.FailErr(t, "create foreign temporary subtree", err)
	t.Cleanup(func() { _ = os.RemoveAll(foreign) })
	probe := filepath.Join(foreign, "escape.txt")
	if code := confinedEscapeExit(t, self, c, "/usr/bin/touch", probe); code == 0 {
		t.Fatal("write under a foreign /private/var/folders subtree must be denied")
	}
	assertInProjectWriteOK(t, self, c, proj)
	ownTmp := filepath.Join(tmp, "lycaon-escape-f6-own.txt")
	_ = os.Remove(ownTmp)
	t.Cleanup(func() { _ = os.Remove(ownTmp) })
	if code := confinedEscapeExit(t, self, c, "/usr/bin/touch", ownTmp); code != 0 {
		t.Fatalf("positive control: os.TempDir() write must succeed, exit=%d", code)
	}
}

func TestConfinementIntegrityProjectRootFilesystemAccepted(t *testing.T) {
	srv := wiring.BuildForTest(t).Server
	body := `{"roots":[{"path":"/"}]}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects", strings.NewReader(body))
	req.Header.Set("Authorization", api.TestAuthHeader())
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST project root /: status=%d body=%s", w.Code, w.Body.String())
	}

	okDir := t.TempDir()
	okBody := `{"roots":[{"path":` + jsonQuote(okDir) + `}]}`
	okReq := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects", strings.NewReader(okBody))
	okReq.Header.Set("Authorization", api.TestAuthHeader())
	okReq.Header.Set("Content-Type", "application/json")
	okW := httptest.NewRecorder()
	srv.ServeHTTP(okW, okReq)
	if okW.Code != http.StatusCreated {
		t.Fatalf("positive control: normal folder create status=%d body=%s", okW.Code, okW.Body.String())
	}
}

func TestConfinementIntegrityCompositeChain(t *testing.T) {
	t.Run("a_confined_cannot_read_api_token", func(t *testing.T) {
		self := requireSeatbeltEscape(t)
		cfg := t.TempDir()
		t.Setenv("LYCAON_CONFIG_DIR", cfg)
		tokenPath := filepath.Join(cfg, "api.token")
		testutil.FailErr(t, "write token", os.WriteFile(tokenPath, []byte("composite-bearer"), 0o600))
		proj := t.TempDir()
		c := confine.Confinement{Roots: []string{proj}}
		if code := confinedEscapeExit(t, self, c, "/bin/cat", tokenPath); code == 0 {
			t.Fatal("composite (a): confined command must not read api.token")
		}
		assertInProjectWriteOK(t, self, c, proj)
	})

	t.Run("b_token_in_hand_still_refuses_control_plane_root", func(t *testing.T) {
		srv := wiring.BuildForTest(t).Server
		cfg := os.Getenv("LYCAON_CONFIG_DIR")
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/projects",
			strings.NewReader(`{"roots":[{"path":`+jsonQuote(cfg)+`}]}`))
		req.Header.Set("Authorization", api.TestAuthHeader())
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("composite (b): status=%d want 400", w.Code)
		}
		var errResp wire.ErrorResponse
		testutil.FailErr(t, "decode", json.Unmarshal(w.Body.Bytes(), &errResp))
		if errResp.Code != confine.WriteRootCodeSecretStore {
			t.Fatalf("composite (b): code=%q want %q", errResp.Code, confine.WriteRootCodeSecretStore)
		}
	})
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// newTestBrokerFor builds a broker that already knows who is calling it.
func newTestBrokerFor(decide egressproxy.EndpointDecider, resolve egressproxy.PeerResolver) *egressproxy.Broker {
	b := egressproxy.New(decide)
	b.SetPeerResolver(resolve)
	return b
}
