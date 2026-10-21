//go:build linux

package packagegeneration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/testutil"
)

type installFixture struct {
	t       *testing.T
	base    string
	staging string
	system  string
	layout  layout
}

// newInstallFixture stages a package payload whose generation store lives inside
// it, as the native layout does, with stand-in WebKit helper programs.
func newInstallFixture(t *testing.T) *installFixture {
	t.Helper()
	l := testLayout(t)
	f := &installFixture{t: t, base: l.anchor, staging: filepath.Join(l.anchor, "stage"), system: filepath.Join(l.anchor, "system")}
	l.store = filepath.Join(f.staging, "generations")
	l.staged = "generations"
	f.layout = l
	for _, directory := range []string{f.staging, filepath.Join(f.system, "x86_64-linux-gnu")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			testutil.FailErr(t, "create directory", err)
		}
	}
	for _, path := range []string{f.staging, f.system, filepath.Join(f.system, "x86_64-linux-gnu")} {
		if err := os.Chmod(path, 0o755); err != nil {
			testutil.FailErr(t, "os.Chmod failed", err)
		}
	}
	f.program("x86_64-linux-gnu/WebKitWebProcess")
	f.program("x86_64-linux-gnu/WebKitNetworkProcess")
	f.stage("first")
	return f
}

func (f *installFixture) program(name string) string {
	f.t.Helper()
	path := filepath.Join(f.system, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
	return path
}

// stage writes a complete payload: an engine image, a resource and the catalog.
func (f *installFixture) stage(resource string) {
	f.t.Helper()
	catalog, err := json.Marshal(map[string]any{
		"format_version": 1,
		"architectures":  map[string]any{"amd64": map[string]string{"multiarch": "x86_64-linux-gnu"}},
		"families": map[string]any{
			"deb": map[string][]string{"system_executables": {
				f.system + "/{multiarch}/WebKitWebProcess", f.system + "/{multiarch}/WebKitNetworkProcess",
				f.system + "/{multiarch}/WebKitGPUProcess"},
				"library_roots": {f.system + "/{multiarch}", f.system + "/lib64"}},
			"rpm": map[string][]string{"system_executables": {f.system + "/absent/WebKitWebProcess"},
				"library_roots": {f.system + "/{multiarch}"}},
			"pacman": map[string][]string{"system_executables": {f.system + "/{multiarch}/WebKitWebProcess"},
				"library_roots": {f.system + "/absent"}},
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	files := map[string][]byte{"bin/pw": []byte("#!/bin/sh\nexit 0\n"), "resource.data": []byte(resource), LaunchCatalog: catalog}
	if err := os.MkdirAll(filepath.Join(f.staging, "bin"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(f.staging, "bin"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	payload := []map[string]any{{"path": "bin", "kind": "directory", "mode": 0o755, "size": nil, "sha256": nil, "target": nil}}
	var executables []map[string]any
	for name, raw := range files {
		mode := 0o644
		if name == "bin/pw" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(f.staging, name), raw, os.FileMode(mode)); err != nil {
			f.t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(f.staging, name), os.FileMode(mode)); err != nil {
			f.t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		payload = append(payload, map[string]any{"path": name, "kind": "file", "mode": mode, "size": len(raw),
			"sha256": hex.EncodeToString(sum[:]), "target": nil})
		if name == "bin/pw" {
			executables = append(executables, map[string]any{"path": "/opt/painted-wolf-code/bin/pw", "size": len(raw), "sha256": hex.EncodeToString(sum[:])})
		}
	}
	manifest, err := json.Marshal(map[string]any{"format_version": 2, "package": "painted-wolf-code", "version": "1.0.2",
		"architecture": "x86_64", "install_root": "/opt/painted-wolf-code", "executables": executables, "payload": payload})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.staging, "manifest.json"), manifest, 0o644); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(f.staging, "manifest.json"), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

type recordedPolicy struct {
	selected string
	access   SystemAccess
	commits  int
}

func (p *recordedPolicy) commit(g Generation, access SystemAccess) error {
	p.selected, p.access = g.Root, access
	p.commits++
	return nil
}

func (p *recordedPolicy) selection() (string, error) { return p.selected, nil }

func (f *installFixture) install(policy *recordedPolicy, family string) (Generation, error) {
	f.t.Helper()
	staging, err := os.OpenRoot(f.staging)
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = staging.Close() }()
	return f.layout.install(staging, family, "amd64", policy.commit, policy.selection)
}

func TestInstallSelectsTheStagedPayloadWithTheCatalogsPresentHelpers(t *testing.T) {
	f := newInstallFixture(t)
	policy := &recordedPolicy{}
	generation, err := f.install(policy, "deb")
	testutil.FailErr(t, "f.install failed", err)
	if policy.selected != generation.Root || filepath.Dir(generation.Root) != f.layout.store {
		t.Fatalf("selected %q, generation %q", policy.selected, generation.Root)
	}
	want := []string{filepath.Join(f.system, "x86_64-linux-gnu/WebKitWebProcess"), filepath.Join(f.system, "x86_64-linux-gnu/WebKitNetworkProcess")}
	if strings.Join(policy.access.Executables, ",") != strings.Join(want, ",") {
		t.Fatalf("system executables = %v, want %v (absent GPU process left out)", policy.access.Executables, want)
	}
	if roots := policy.access.LibraryRoots; len(roots) != 1 || roots[0] != filepath.Join(f.system, "x86_64-linux-gnu") {
		t.Fatalf("library roots = %v, want the present multiarch root only", roots)
	}
	if _, err := os.Stat(filepath.Join(generation.Root, LaunchCatalog)); err != nil {
		t.Fatal("the sealed catalog is not part of the generation")
	}
}

func TestAnUpgradeReclaimsOnlyUnheldOldGenerations(t *testing.T) {
	f := newInstallFixture(t)
	policy := &recordedPolicy{}
	first, err := f.install(policy, "deb")
	testutil.FailErr(t, "f.install failed", err)
	held, err := f.layout.openLifetime(first.ManifestSHA256)
	testutil.FailErr(t, "f.layout.openLifetime failed", err)
	defer func() { _ = held.Close() }()
	if err := unix.Flock(int(held.Fd()), unix.LOCK_SH); err != nil {
		testutil.FailErr(t, "unix.Flock failed", err)
	}
	f.stage("second")
	second, err := f.install(policy, "deb")
	testutil.FailErr(t, "f.install failed", err)
	if second.Root == first.Root || policy.selected != second.Root {
		t.Fatalf("upgrade selected %q", policy.selected)
	}
	if _, err := os.Stat(first.Root); err != nil {
		t.Fatalf("a running launch's generation was reclaimed: %v", err)
	}
	if err := unix.Flock(int(held.Fd()), unix.LOCK_UN); err != nil {
		testutil.FailErr(t, "unix.Flock failed", err)
	}
	f.stage("third")
	third, err := f.install(policy, "deb")
	testutil.FailErr(t, "f.install failed", err)
	for _, gone := range []string{first.Root, second.Root} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Fatalf("unheld old generation %s survived: %v", gone, err)
		}
	}
	if _, err := os.Stat(third.Root); err != nil {
		testutil.FailErr(t, "stat path", err)
	}
}

func TestRemovalWithdrawsTheSelectionBeforeReclaiming(t *testing.T) {
	f := newInstallFixture(t)
	policy := &recordedPolicy{}
	generation, err := f.install(policy, "deb")
	testutil.FailErr(t, "f.install failed", err)
	withdrawn := false
	if err := f.layout.remove(func() error { withdrawn = true; policy.selected = ""; return nil }, policy.selection); err != nil {
		testutil.FailErr(t, "f.layout.remove failed", err)
	}
	if !withdrawn {
		t.Fatal("removal reclaimed without withdrawing the selection")
	}
	if _, err := os.Stat(generation.Root); !os.IsNotExist(err) {
		t.Fatalf("removed package kept its unheld generation: %v", err)
	}
	refused := errors.New("policy busy")
	if err := f.layout.remove(func() error { return refused }, policy.selection); !errors.Is(err, refused) {
		t.Fatalf("a failed withdrawal = %v", err)
	}
}

func TestCatalogAndHelperRefusalsSelectNothing(t *testing.T) {
	for name, change := range map[string]func(f *installFixture) string{
		"unknown family":  func(*installFixture) string { return "nix" },
		"no library root": func(*installFixture) string { return "pacman" },
		"writable library root": func(f *installFixture) string {
			_ = os.Chmod(filepath.Join(f.system, "x86_64-linux-gnu"), 0o775)
			return "deb"
		},
		"no helper present": func(*installFixture) string { return "rpm" },
		"writable helper": func(f *installFixture) string {
			_ = os.Chmod(filepath.Join(f.system, "x86_64-linux-gnu/WebKitWebProcess"), 0o775)
			return "deb"
		},
		"helper not program": func(f *installFixture) string {
			_ = os.Chmod(filepath.Join(f.system, "x86_64-linux-gnu/WebKitWebProcess"), 0o644)
			return "deb"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newInstallFixture(t)
			policy := &recordedPolicy{}
			if _, err := f.install(policy, change(f)); err == nil || policy.commits != 0 {
				t.Fatalf("install = %v with %d commits", err, policy.commits)
			}
		})
	}
	f := newInstallFixture(t)
	staging, err := os.OpenRoot(f.staging)
	testutil.FailErr(t, "os.OpenRoot failed", err)
	defer func() { _ = staging.Close() }()
	policy := &recordedPolicy{}
	if _, err := f.layout.install(staging, "deb", "riscv64", policy.commit, policy.selection); err == nil || policy.commits != 0 {
		t.Fatalf("unknown architecture = %v", err)
	}
}

func TestStagingVerificationExcludesOnlyTheStore(t *testing.T) {
	f := newInstallFixture(t)
	policy := &recordedPolicy{}
	if _, err := f.install(policy, "deb"); err != nil {
		testutil.FailErr(t, "f.install failed", err)
	}
	if err := os.WriteFile(filepath.Join(f.staging, "stray"), []byte("unlisted"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := os.Chmod(filepath.Join(f.staging, "stray"), 0o644); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	f.stage("changed")
	if _, err := f.install(policy, "deb"); err == nil || !strings.Contains(err.Error(), "unlisted payload object") {
		t.Fatalf("unlisted staging object = %v", err)
	}
}

func TestAForeignStoreEntryIsReportedNotRemoved(t *testing.T) {
	f := newInstallFixture(t)
	policy := &recordedPolicy{}
	if _, err := f.install(policy, "deb"); err != nil {
		testutil.FailErr(t, "f.install failed", err)
	}
	foreign := filepath.Join(f.layout.store, "notes")
	if err := os.Mkdir(foreign, 0o755); err != nil {
		testutil.FailErr(t, "os.Mkdir failed", err)
	}
	if err := f.layout.reclaimUnselected(policy.selection); err == nil || !strings.Contains(err.Error(), "foreign store entry") {
		t.Fatalf("foreign entry = %v", err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("foreign store entry was removed")
	}
}
