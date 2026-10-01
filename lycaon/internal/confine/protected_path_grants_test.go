package confine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
)

func credentialStoreFixture(t *testing.T) (dir, target string) {
	t.Helper()
	home := t.TempDir()
	dir = filepath.Join(home, ".docker")
	if err := os.MkdirAll(filepath.Join(dir, "cli-plugins"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return dir, filepath.Join(dir, "config.json")
}

func TestProtectedFileGrantCoversOnlyTheApprovedFile(t *testing.T) {
	dir, _ := credentialStoreFixture(t)
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	target := filepath.Join(resolved, "config.json")
	var b strings.Builder
	FilesystemRules{rules: []fsRule{protectedGrantRule([]ProtectedPathGrant{{ResolvedPath: target}})}}.render(&b, "")
	profile := b.String()
	if !strings.Contains(profile, "(literal \""+target+"\")") {
		t.Fatalf("profile does not cover approved file %q: %s", target, profile)
	}
	for _, path := range []string{
		filepath.Join(resolved, ".tmp-config.json2451927"),
		filepath.Join(resolved, "config.json.lock"),
		filepath.Join(resolved, "config.json.bak"),
		filepath.Join(resolved, "config.json1234567"),
		filepath.Join(resolved, "cli-plugins", "docker-buildx"),
		filepath.Join(resolved, "cli-plugins", "config.json"),
		filepath.Join(resolved, "contexts", "meta", "config.json"),
		filepath.Join(resolved, "trust", "config.json"),
		filepath.Join(resolved, "daemon.json"),
		filepath.Join(filepath.Dir(resolved), ".ssh", "config.json"),
	} {
		if strings.Contains(profile, path) {
			t.Errorf("grant covers unapproved path %q", path)
		}
	}
}

func TestProtectedGrantAppliesForAFileThatDoesNotExistYet(t *testing.T) {
	_, target := credentialStoreFixture(t)

	grant := NewProtectedPathGrant(target)
	applied, dropped, err := validateProtectedPathGrants([]ProtectedPathGrant{grant})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(applied) != 1 || len(dropped) != 0 {
		t.Fatalf("applied=%d dropped=%+v; the first login on a machine creates this file", len(applied), dropped)
	}
	if applied[0].Subtree {
		t.Fatal("a missing file resolved to a subtree grant")
	}
}

// The repoint attack sockets already defend against, in its filesystem form:
// between approval and use the file becomes a symlink somewhere else.
func TestProtectedGrantIsDroppedWhenTheTargetBecomesASymlink(t *testing.T) {
	dir, target := credentialStoreFixture(t)
	grant := NewProtectedPathGrant(target)

	elsewhere := filepath.Join(filepath.Dir(dir), "authorized_keys")
	if err := os.WriteFile(elsewhere, []byte("ssh-ed25519 AAAA\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(elsewhere, target); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	applied, dropped, err := validateProtectedPathGrants([]ProtectedPathGrant{grant})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(applied) != 0 {
		t.Fatal("a repointed protected file was still granted")
	}
	if len(dropped) != 1 || dropped[0].Reason != ProtectedDropIsSymlink {
		t.Fatalf("dropped = %+v, want a symlink drop", dropped)
	}
}

// A directory approval covers its subtree; a file approval whose target became
// a directory is repointed authority.
func TestProtectedGrantDirectorySemantics(t *testing.T) {
	dir, target := credentialStoreFixture(t)

	subtree := NewProtectedPathGrant(dir)
	if !subtree.Subtree {
		t.Fatalf("directory approval %q did not resolve to a subtree grant", dir)
	}
	applied, dropped, err := validateProtectedPathGrants([]ProtectedPathGrant{subtree})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(applied) != 1 || len(dropped) != 0 {
		t.Fatalf("applied=%d dropped=%+v, want the approved directory subtree", len(applied), dropped)
	}

	fileGrant := NewProtectedPathGrant(target)
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	applied, dropped, err = validateProtectedPathGrants([]ProtectedPathGrant{fileGrant})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(applied) != 0 || len(dropped) != 1 || dropped[0].Reason != ProtectedDropRepointed {
		t.Fatalf("applied=%d dropped=%+v, want a repointed drop for file-approval-turned-directory", len(applied), dropped)
	}
}

func TestProtectedGrantRefusesRelativePaths(t *testing.T) {
	grant := ProtectedPathGrant{ApprovedPath: ".docker/config.json", ResolvedPath: ".docker/config.json"}
	applied, dropped, err := validateProtectedPathGrants([]ProtectedPathGrant{grant})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(applied) != 0 || len(dropped) != 1 || dropped[0].Reason != ProtectedDropNotAbsolute {
		t.Fatalf("applied=%d dropped=%+v, want a not-absolute drop", len(applied), dropped)
	}
}

func TestProtectedGrantCannotOverrideControlPlane(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LYCAON_CONFIG_DIR", configDir)
	target := filepath.Join(configDir, "approvals.yaml")
	grant := NewProtectedPathGrant(target)
	applied, dropped, err := validateProtectedPathGrants([]ProtectedPathGrant{grant})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(applied) != 0 || len(dropped) != 1 || dropped[0].Reason != ProtectedDropControlPlane {
		t.Fatalf("applied=%d dropped=%+v, want a control-plane drop", len(applied), dropped)
	}
}

// An approved key-material file is a valid transaction: the ask named it, the
// grant punches exactly it.
func TestProtectedGrantAppliesToApprovedKeyMaterial(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	target := filepath.Join(sshDir, "authorized_keys")

	grant := NewProtectedPathGrant(target)
	applied, dropped, err := validateProtectedPathGrants([]ProtectedPathGrant{grant})
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(applied) != 1 || len(dropped) != 0 {
		t.Fatalf("applied=%d dropped=%+v, want the approved key-material file", len(applied), dropped)
	}
}

// The profile has to carry the transaction, and it has to carry it after the
// denies: literal for files, subpath for approved directories.
func TestProfileEmitsProtectedGrantsAfterTheDenies(t *testing.T) {
	dir, target := credentialStoreFixture(t)
	fileGrant := NewProtectedPathGrant(target)
	dirGrant := NewProtectedPathGrant(dir)

	var b strings.Builder
	FilesystemRules{rules: []fsRule{protectedGrantRule([]ProtectedPathGrant{fileGrant, dirGrant})}}.render(&b, "")

	profile := b.String()
	if !strings.Contains(profile, "(allow file-write*") {
		t.Fatalf("profile fragment = %q, want an allow block", profile)
	}
	if !strings.Contains(profile, "(literal \""+fileGrant.ResolvedPath+"\")") {
		t.Fatalf("profile fragment = %q, want a literal for the file grant", profile)
	}
	if !strings.Contains(profile, "(subpath \""+dirGrant.ResolvedPath+"\")") {
		t.Fatalf("profile fragment = %q, want a subpath for the directory grant", profile)
	}
}

// The read floor denies key material at the kernel; an approved read grant
// punches exactly the declared file back through.
func TestProfileReadDeniesKeyMaterialWithGrantPunch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(home, ".config", "paintedwolf"))
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	key := filepath.Join(sshDir, "id_ed25519")

	p, err := BuildProfile(Confinement{
		Roots:               []string{t.TempDir()},
		ProtectedReadGrants: []ProtectedPathGrant{NewProtectedPathGrant(key)},
	})
	if err != nil {
		t.Fatalf("BuildProfile: %v", err)
	}
	resolvedSSH := fspath.CanonicalPath(sshDir)
	if !strings.Contains(p, "(deny file-read*") || !strings.Contains(p, "(subpath \""+resolvedSSH+"\")") {
		t.Fatalf("profile must read-deny the key-material tree:\n%s", p)
	}
	if !strings.Contains(p, "(literal \""+fspath.CanonicalPath(key)+"\")") {
		t.Fatalf("approved read grant must punch the exact file:\n%s", p)
	}
}

// TestSplitTaskOverlayRoutesProtectedPaths checks that SplitChatOverlay
// routes protected classifications to exact punches and leaves ordinary
// approvals as plain overlay roots.
func TestSplitTaskOverlayRoutesProtectedPaths(t *testing.T) {
	prevStores := credentialStorePathsSource.Load()
	prevFloor := keyMaterialPathsSource.Load()
	SetCredentialStorePathsSource(func() []string { return credentialStorePathFixture })
	SetKeyMaterialPathsSource(func() []string { return []string{"~/.ssh/"} })
	t.Cleanup(func() {
		credentialStorePathsSource.Store(prevStores)
		keyMaterialPathsSource.Store(prevFloor)
	})

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	ordinary := filepath.Join(home, "go", "pkg", "mod")
	store := filepath.Join(home, ".netrc")
	keyFile := filepath.Join(home, ".ssh", "authorized_keys")

	roots, protected := SplitChatOverlay([]string{ordinary, store, keyFile})
	if len(roots) != 1 || roots[0] != ordinary {
		t.Fatalf("roots = %v, want only the ordinary path", roots)
	}
	if len(protected) != 2 {
		t.Fatalf("protected = %+v, want the store and the key-material file", protected)
	}
	for _, g := range protected {
		if g.ApprovedPath != store && g.ApprovedPath != keyFile {
			t.Errorf("unexpected protected grant %+v", g)
		}
	}
}
