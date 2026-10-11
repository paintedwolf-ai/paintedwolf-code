package confine

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
)

func TestGrantedWriteRootsSourceUnionedIntoWriteRoots(t *testing.T) {
	// A synthetic path outside temp/cache so the union is what puts it in-root.
	granted := "/opt/lycaon-test-durable-cache"
	SetGrantedWriteRootsSource(func(string) []string { return []string{granted} })
	t.Cleanup(func() { SetGrantedWriteRootsSource(nil) })

	roots := WriteRootsForBoundary("", nil, nil, "")
	if !PathWithinWriteRoots(granted, roots) {
		t.Fatalf("durable write_root grant %q not unioned into write roots: %v", granted, roots)
	}
}

func TestGrantedWriteRootsSourceReadLive(t *testing.T) {
	granted := "/opt/lycaon-test-later-cache"
	var enabled bool
	SetGrantedWriteRootsSource(func(string) []string {
		if enabled {
			return []string{granted}
		}
		return nil
	})
	t.Cleanup(func() { SetGrantedWriteRootsSource(nil) })

	if PathWithinWriteRoots(granted, WriteRootsForBoundary("", nil, nil, "")) {
		t.Fatal("grant must not be present before it is enabled")
	}
	enabled = true
	if !PathWithinWriteRoots(granted, WriteRootsForBoundary("", nil, nil, "")) {
		t.Fatal("grant must be picked up live on the next resolve (Revoke drops it likewise)")
	}
}

// Attached (standing) roots refuse stores in both directions: the root itself
// and any root that merely contains one.
func TestAttachedWriteRootRefused(t *testing.T) {
	withShippedCredentialStores(t)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	cases := []struct {
		name string
		root string
		code string // empty => allowed
	}{
		{"home", home, ""},
		{"fs-root", string(filepath.Separator), ""},
		{"relative", "relative/path", WriteRootCodeNotAbsolute},
		{"empty", "  ", WriteRootCodeNotAbsolute},
		{"ssh-secret", filepath.Join(home, ".ssh"), WriteRootCodeSecretStore},
		{"kube-secret", filepath.Join(home, ".kube"), WriteRootCodeSecretStore},
		{"gcloud-secret", filepath.Join(home, ".config", "gcloud"), WriteRootCodeSecretStore},
		{"azure-secret", filepath.Join(home, ".azure"), WriteRootCodeSecretStore},
		{"keychains-secret", filepath.Join(home, "Library", "Keychains"), WriteRootCodeSecretStore},
		{"keychains-parent", filepath.Join(home, "Library"), ""},
		{"docker-config-secret", filepath.Join(home, ".docker", "config.json"), WriteRootCodeSecretStore},
		{"docker-parent", filepath.Join(home, ".docker"), ""},
		{"docker-contexts-secret", filepath.Join(home, ".docker", "contexts"), WriteRootCodeSecretStore},
		// Runtime state beside denied files remains grantable.
		{"docker-buildx-state-ok", filepath.Join(home, ".docker", "buildx", "activity"), ""},
		{"under-home-ok", filepath.Join(home, "go", "pkg", "mod"), ""},
		{"opt-ok", "/opt/shared-cache", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refused, code := AttachedWriteRootRefused(tc.root)
			if tc.code == "" {
				if refused {
					t.Fatalf("AttachedWriteRootRefused(%q)=true code=%q want allowed", tc.root, code)
				}
				return
			}
			if !refused || code != tc.code {
				t.Fatalf("AttachedWriteRootRefused(%q)=(%v,%q) want (true,%q)", tc.root, refused, code, tc.code)
			}
		})
	}
}

// Protected descendants use the protected-grant lane.
func TestGrantedWriteRootRefused(t *testing.T) {
	withShippedCredentialStores(t)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	cases := []struct {
		name string
		root string
		code string // empty => allowed
	}{
		{"fs-root", string(filepath.Separator), ""},
		{"relative", "relative/path", WriteRootCodeNotAbsolute},
		{"empty", "  ", WriteRootCodeNotAbsolute},
		{"ssh-key-material", filepath.Join(home, ".ssh"), WriteRootCodeSecretStore},
		{"kube-secret", filepath.Join(home, ".kube"), WriteRootCodeSecretStore},
		{"keychains-key-material", filepath.Join(home, "Library", "Keychains"), WriteRootCodeSecretStore},
		{"docker-config-secret", filepath.Join(home, ".docker", "config.json"), WriteRootCodeSecretStore},
		{"docker-contexts-secret", filepath.Join(home, ".docker", "contexts"), WriteRootCodeSecretStore},
		{"docker-trust-key-material", filepath.Join(home, ".docker", "trust"), WriteRootCodeSecretStore},
		{"inside-store", filepath.Join(home, ".config", "gcloud", "logs"), WriteRootCodeSecretStore},
		// The ask adjudicated these; the floor protects what sits inside.
		{"docker-parent-ok", filepath.Join(home, ".docker"), ""},
		{"config-parent-ok", filepath.Join(home, ".config"), ""},
		{"library-parent-ok", filepath.Join(home, "Library"), ""},
		{"home-ok", home, ""},
		{"docker-buildx-state-ok", filepath.Join(home, ".docker", "buildx", "activity"), ""},
		{"under-home-ok", filepath.Join(home, "go", "pkg", "mod"), ""},
		{"opt-ok", "/opt/shared-cache", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refused, code := GrantedWriteRootRefused(tc.root)
			if tc.code == "" {
				if refused {
					t.Fatalf("GrantedWriteRootRefused(%q)=true code=%q want allowed", tc.root, code)
				}
				return
			}
			if !refused || code != tc.code {
				t.Fatalf("GrantedWriteRootRefused(%q)=(%v,%q) want (true,%q)", tc.root, refused, code, tc.code)
			}
		})
	}
}

func TestControlPlaneReadAndWriteLanesAreDistinct(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	project := t.TempDir()
	agents := filepath.Join(project, "AGENTS.md")
	overlay := filepath.Join(project, ".paintedwolf", "approvals.yaml")
	hostConfig := filepath.Join(configDir, "approvals.yaml")

	for _, path := range []string{agents, overlay} {
		if ControlPlaneReadDenied(path) {
			t.Errorf("project governance path %q must remain readable", path)
		}
		if ControlPlaneWriteDenied(path) {
			t.Errorf("project policy classified as host control plane: %q", path)
		}
		if _, ok := AgentPolicyPath(path, project); !ok {
			t.Errorf("project policy bypassed its review classification: %q", path)
		}
	}
	if !ControlPlaneReadDenied(hostConfig) || !ControlPlaneWriteDenied(hostConfig) {
		t.Fatalf("active host control plane must deny both lanes: %q", hostConfig)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve executable: %v", err)
	}
	if !ControlPlaneWriteDenied(exe) {
		t.Fatalf("active launcher must be write-denied: %q", exe)
	}
}

// A reviewed store-ancestor lease prepares as a plain root while the hard-deny
// floor keeps the catalogued store denied inside it.
func TestGrantedStoreAncestorPreparesWithFloorIntact(t *testing.T) {
	withShippedCredentialStores(t)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	dockerParent := filepath.Join(home, ".docker")

	prepared, err := prepareRequest(Request{
		Roots:             []string{t.TempDir()},
		GrantedWriteRoots: []string{dockerParent},
	})
	if err != nil {
		t.Fatalf("prepareRequest(granted %q): %v", dockerParent, err)
	}
	if len(prepared.GrantedWriteRoots) != 1 || prepared.GrantedWriteRoots[0] != dockerParent {
		t.Fatalf("granted roots = %v, want %q applied", prepared.GrantedWriteRoots, dockerParent)
	}

	specs, err := HardDenyWriteSpecs(Confinement{
		Roots:             prepared.Roots,
		GrantedWriteRoots: prepared.GrantedWriteRoots,
	})
	if err != nil {
		t.Fatalf("HardDenyWriteSpecs: %v", err)
	}
	store := fspath.CanonicalPath(filepath.Join(dockerParent, "config.json"))
	found := false
	for _, s := range specs {
		if s.Subpath == store {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("hard-deny specs %v do not cover %q — the granted ancestor must not open the store", specs, store)
	}
}

// Broker and boundary share the same path classifier.
func TestGrantedLaneAgreesWithSplitTaskOverlay(t *testing.T) {
	withShippedCredentialStores(t)
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home dir")
	}
	paths := []string{
		filepath.Join(home, ".docker"),
		filepath.Join(home, ".docker", "buildx", "activity"),
		filepath.Join(home, ".docker", "config.json"),
		filepath.Join(home, ".kube"),
		filepath.Join(home, ".config"),
		home,
		"/opt/shared-cache",
	}
	roots, protected := SplitChatOverlay(paths)
	for _, root := range roots {
		if refused, code := GrantedWriteRootRefused(root); refused {
			t.Errorf("SplitChatOverlay routed %q as a plain root but GrantedWriteRootRefused refuses it (%s)", root, code)
		}
	}
	for _, grant := range protected {
		if refused, _ := GrantedWriteRootRefused(grant.ApprovedPath); !refused {
			t.Errorf("SplitChatOverlay routed %q as protected but the plain-root lane would accept it", grant.ApprovedPath)
		}
	}
}

func withShippedCredentialStores(t *testing.T) {
	t.Helper()
	SetCredentialStorePathsSource(func() []string { return credentialStorePathFixture })
	t.Cleanup(func() { SetCredentialStorePathsSource(nil) })
}

// Attached ancestor roots inherit every denied descendant; granted ancestors
// do not — the ask reviewed them and the floor protects the descendants.
func TestAncestorsOfCredentialStoresSplitByLane(t *testing.T) {
	withShippedCredentialStores(t)
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("no home directory")
	}

	ancestors := []string{
		filepath.Join(home, ".config"),
		filepath.Join(home, "Library"),
		home,
	}
	for _, p := range ancestors {
		if ok, code := AttachedWriteRootRefused(p); ok {
			t.Errorf("AttachedWriteRootRefused(%q) refused with %s; floors protect contained stores", p, code)
		}
		if ok, code := GrantedWriteRootRefused(p); ok {
			t.Errorf("GrantedWriteRootRefused(%q) = true (%s), want allowed — the floor protects the stores inside", p, code)
		}
	}

	descendants := []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws", "cli"),
	}
	for _, p := range descendants {
		if ok, _ := AttachedWriteRootRefused(p); !ok {
			t.Errorf("AttachedWriteRootRefused(%q) = false, want refused", p)
		}
		if ok, _ := GrantedWriteRootRefused(p); !ok {
			t.Errorf("GrantedWriteRootRefused(%q) = false, want refused — store descendants travel as protected grants", p)
		}
	}

	allowed := []string{
		filepath.Join(home, "Library", "Caches"),
		filepath.Join(home, "code", "myrepo"),
		filepath.Join(home, "cache", "objects"),
		"/opt/homebrew",
	}
	for _, p := range allowed {
		if ok, code := AttachedWriteRootRefused(p); ok {
			t.Errorf("AttachedWriteRootRefused(%q) = true (%s), want allowed", p, code)
		}
		if ok, code := GrantedWriteRootRefused(p); ok {
			t.Errorf("GrantedWriteRootRefused(%q) = true (%s), want allowed", p, code)
		}
	}
}

func TestDraftWorkspaceGrantableOnBothLanes(t *testing.T) {
	// Workspace carve-outs remain attachable and grantable.
	cfg, err := configdir.UserConfigDir()
	if err != nil || strings.TrimSpace(cfg) == "" {
		t.Skip("no config dir")
	}
	draft := filepath.Join(cfg, "drafts", "parity-draft-id")
	if refused, code := AttachedWriteRootRefused(draft); refused {
		t.Fatalf("AttachedWriteRootRefused(draft scratch)=(%v,%q) want allowed", refused, code)
	}
	if refused, code := GrantedWriteRootRefused(draft); refused {
		t.Fatalf("GrantedWriteRootRefused(draft scratch)=(%v,%q) want allowed", refused, code)
	}
	if refused, code := AttachedWriteRootRefused(cfg); !refused || code != WriteRootCodeSecretStore {
		t.Fatalf("AttachedWriteRootRefused(configdir)=(%v,%q) want refused secret store", refused, code)
	}
	if refused, code := GrantedWriteRootRefused(cfg); !refused || code != WriteRootCodeControlPlane {
		t.Fatalf("GrantedWriteRootRefused(configdir)=(%v,%q) want refused control plane", refused, code)
	}
}

// The approval gate and the path resolver read one verdict for the state tree.
// Reads follow the read-floor switch; writes never do.
func TestControlPlanePathDeniedIsOneVerdictForBothLanes(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	sessions := filepath.Join(configDir, "debug", "sessions")
	draft := filepath.Join(enginepaths.DraftsRootUnder(configDir), "p", "notes.md")
	ordinary := filepath.Join(t.TempDir(), "notes.md")

	if !ControlPlanePathDenied(sessions, false, "") || !ControlPlanePathDenied(sessions, true, "") {
		t.Fatalf("state tree path %q must be denied in both lanes", sessions)
	}
	if ControlPlanePathDenied(draft, false, "") || ControlPlanePathDenied(draft, true, "") {
		t.Fatalf("agent workspace %q is not the control plane", draft)
	}
	if ControlPlanePathDenied(ordinary, false, "") || ControlPlanePathDenied(ordinary, true, "") {
		t.Fatalf("ordinary path %q must not be control-plane denied", ordinary)
	}

	t.Run("read floor off keeps writes denied", func(t *testing.T) {
		t.Setenv("LYCAON_SANDBOX_DENY_READ", "off")
		if ControlPlanePathDenied(sessions, false, "") {
			t.Fatalf("the read-floor switch must remove the read verdict for %q", sessions)
		}
		if !ControlPlanePathDenied(sessions, true, "") || !ControlPlaneWriteDenied(sessions) {
			t.Fatalf("no switch removes the write verdict for %q", sessions)
		}
	})
}

// A session's own scratch is its workspace, as the floor treats it; every
// other session's scratch stays inside the control plane.
func TestControlPlanePathDeniedLeavesOnlyTheInvocationsScratch(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	own := enginepaths.SessionScratchUnder(configDir, "chat-own")
	worker := enginepaths.SessionScratchUnder(configDir, "worker-1")
	other := enginepaths.SessionScratchUnder(configDir, "chat-other")
	for _, dir := range []string{own, worker, other} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("create scratch %q: %v", dir, err)
		}
	}

	for _, write := range []bool{false, true} {
		if ControlPlanePathDenied(filepath.Join(own, "flow.sh"), write, own) {
			t.Fatalf("own scratch denied (write=%v)", write)
		}
		if ControlPlanePathDenied(own, write, own) {
			t.Fatalf("own scratch root denied (write=%v)", write)
		}
		if !ControlPlanePathDenied(filepath.Join(other, "flow.sh"), write, own) {
			t.Fatalf("another session's scratch allowed (write=%v)", write)
		}
		if !ControlPlanePathDenied(filepath.Join(own, "flow.sh"), write, "") {
			t.Fatalf("scratch allowed without the invocation's scratch root (write=%v)", write)
		}
		// A worker's scratch is a sibling of its coordinator's; each reaches only its own.
		if !ControlPlanePathDenied(filepath.Join(own, "flow.sh"), write, worker) {
			t.Fatalf("worker reached its coordinator's scratch (write=%v)", write)
		}
		if !ControlPlanePathDenied(filepath.Join(worker, "flow.sh"), write, own) {
			t.Fatalf("coordinator reached its worker's scratch (write=%v)", write)
		}
		if ControlPlanePathDenied(filepath.Join(worker, "flow.sh"), write, worker) {
			t.Fatalf("worker scratch denied (write=%v)", write)
		}
	}
	if !ControlPlanePathDenied(filepath.Join(own+"-sibling", "flow.sh"), true, own) {
		t.Fatal("a sibling sharing the scratch root's prefix is not inside it")
	}
}

func TestWriteRootsForBoundarySymmetricAliases(t *testing.T) {
	roots := WriteRootsForBoundary("", nil, nil, "")
	if runtime.GOOS == "darwin" {
		// Both /tmp and /private/tmp must be present.
		hasTmp := false
		hasPrivateTmp := false
		for _, r := range roots {
			if r == "/tmp" {
				hasTmp = true
			}
			if r == "/private/tmp" {
				hasPrivateTmp = true
			}
		}
		if !hasTmp || !hasPrivateTmp {
			t.Fatalf("WriteRootsForBoundary() missing symmetric tmp aliases: hasTmp=%v hasPrivateTmp=%v in %v", hasTmp, hasPrivateTmp, roots)
		}
	}
}

// A sibling that merely shares the /private/tmp spelling is not the temp tree.
func TestWriteRootsForBoundaryAliasesOnlyWholeSegments(t *testing.T) {
	for _, c := range []struct {
		path, root, rest string
		ok               bool
	}{
		{"/private/tmp", "/private/tmp", "", true},
		{"/private/tmp/x", "/private/tmp", "/x", true},
		{"/private/tmpX", "/private/tmp", "", false},
		{"/private/tmpX/y", "/private/tmp", "", false},
		{"/private/variant", "/private/var", "", false},
		{"/tmpfoo", "/tmp", "", false},
	} {
		rest, ok := pathUnder(c.path, c.root)
		if ok != c.ok || rest != c.rest {
			t.Errorf("pathUnder(%q, %q) = %q, %v; want %q, %v", c.path, c.root, rest, ok, c.rest, c.ok)
		}
	}
	if runtime.GOOS != "darwin" {
		return
	}
	roots := WriteRootsForBoundary("", []string{"/private/tmpX/project", "/private/variant/project"}, nil, "")
	for _, r := range roots {
		if strings.HasPrefix(r, "/tmpX") || strings.HasPrefix(r, "/variant") {
			t.Fatalf("WriteRootsForBoundary aliased a sibling of a /private root: %q in %v", r, roots)
		}
	}
}

// Package stores and Go's local telemetry counters are writable by default;
// the tool homes and Go config directory around them are not.
func TestStandardCacheDataRootsNameStoresNotToolHomes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LYCAON_SANDBOX_WRITE_ROOTS", "")
	configDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("user config dir: %v", err)
	}
	roots := map[string]bool{}
	for _, r := range standardCacheDataRoots() {
		roots[r] = true
	}
	for _, want := range []string{".bun/install/cache", ".npm/_cacache", ".cargo/registry", ".cargo/git"} {
		if !roots[filepath.Join(home, want)] {
			t.Errorf("default roots miss package store ~/%s: %v", want, roots)
		}
	}
	for _, refused := range []string{".bun", ".npm", ".cargo"} {
		if roots[filepath.Join(home, refused)] {
			t.Errorf("default roots include tool home ~/%s", refused)
		}
	}
	if !roots[filepath.Join(configDir, "go", "telemetry")] {
		t.Errorf("default roots miss Go telemetry counters under %s: %v", configDir, roots)
	}
	for _, refused := range []string{configDir, filepath.Join(configDir, "go")} {
		if roots[refused] {
			t.Errorf("default roots include Go config directory %s", refused)
		}
	}
}

func TestWriteRootsForBoundaryIncludesSessionScratch(t *testing.T) {
	scratchDir := filepath.Join(t.TempDir(), "scratch", "sess-123")
	roots := WriteRootsForBoundary("", nil, nil, scratchDir)
	if !PathWithinWriteRoots(scratchDir, roots) {
		t.Fatalf("session scratch root %q not unioned into write roots: %v", scratchDir, roots)
	}
}
