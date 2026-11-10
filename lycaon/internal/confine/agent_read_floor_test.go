package confine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
)

func installTestKeyMaterialPaths(t *testing.T) {
	t.Helper()
	previous := keyMaterialPathsSource.Load()
	SetKeyMaterialPathsSource(func() []string {
		return []string{"~/.ssh/", "~/.gnupg/"}
	})
	t.Cleanup(func() {
		if previous == nil {
			SetKeyMaterialPathsSource(nil)
			return
		}
		SetKeyMaterialPathsSource(*previous)
	})
}

func TestCompileAgentReadFloor_ControlPlaneAndKeyMaterial(t *testing.T) {
	installTestKeyMaterialPaths(t)
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	sshDir := filepath.Join(home, ".ssh")
	wtDir := filepath.Join(cfgDir, "worktrees", "wt-1")
	for _, dir := range []string{cfgDir, filepath.Join(cfgDir, "packs"), sshDir, wtDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// 1. Direct configdir.Root() and child <configdir>/packs attachments are wholly denied.
	for _, root := range []string{cfgDir, filepath.Join(cfgDir, "packs")} {
		floor := CompileAgentReadFloor(root)
		if !floor.WhollyDenied() || !floor.IsDeniedRel("settings.yaml") || !floor.IsDeniedRel(".") {
			t.Fatalf("expected %s attachment to be wholly denied to AgentAudience", root)
		}
		if !IsAgentSourceReadDenied(root, filepath.Join(root, "settings.yaml")) {
			t.Fatalf("expected IsAgentSourceReadDenied(%s) to deny", root)
		}
	}

	// 2. Direct ~/.ssh attachment is wholly denied.
	sshFloor := CompileAgentReadFloor(sshDir)
	if !sshFloor.WhollyDenied() || !sshFloor.IsDeniedRel("id_ed25519") {
		t.Fatal("expected direct ~/.ssh attachment to be wholly denied to AgentAudience")
	}

	// 3. Ancestor $HOME attachment denies .paintedwolf and .ssh prefixes while allowing normal project files.
	homeFloor := CompileAgentReadFloor(home)
	if homeFloor.WhollyDenied() {
		t.Fatal("expected $HOME not to be wholly denied")
	}
	if !slices.Contains(homeFloor.DeniedRelPrefixes(), ".paintedwolf") || !slices.Contains(homeFloor.DeniedRelPrefixes(), ".ssh") {
		t.Fatalf("expected .paintedwolf and .ssh in DeniedRelPrefixes, got %v", homeFloor.DeniedRelPrefixes())
	}
	if !homeFloor.IsDeniedRel(".paintedwolf/settings.yaml") || !homeFloor.IsDeniedRel(".ssh/id_ed25519") {
		t.Fatal("expected control-plane and key-material relative paths to be denied under $HOME")
	}
	if homeFloor.IsDeniedRel("projects/app/main.go") {
		t.Fatal("expected normal project file under $HOME to be allowed")
	}
}

func TestCompileAgentReadFloor_OptOutAndPathListSeparator(t *testing.T) {
	installTestKeyMaterialPaths(t)
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	extraA := filepath.Join(home, "secret-a")
	extraB := filepath.Join(home, "secret-b")
	for _, dir := range []string{cfgDir, extraA, extraB} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// os.PathListSeparator joins multiple extra deny roots.
	t.Setenv("LYCAON_SANDBOX_DENY_READ", strings.Join([]string{extraA, extraB}, string(os.PathListSeparator)))
	homeFloor := CompileAgentReadFloor(home)
	for _, want := range []string{"secret-a", "secret-b", ".paintedwolf", ".ssh"} {
		if !slices.Contains(homeFloor.DeniedRelPrefixes(), want) {
			t.Fatalf("expected %q in DeniedRelPrefixes, got %v", want, homeFloor.DeniedRelPrefixes())
		}
	}

	// Opt-out tokens ("off", "none", "disable", "disabled") disable configured/control-plane read floor
	// without treating the opt-out token as a relative path under the root, while pack key-material remains denied.
	for _, token := range []string{"off", "none", "disable", "disabled"} {
		t.Setenv("LYCAON_SANDBOX_DENY_READ", token)
		floor := CompileAgentReadFloor(home)
		if floor.IsDeniedRel(token) || floor.IsDeniedRel(token+"/file.go") {
			t.Fatalf("token %q was wrongly treated as a denied relative path prefix", token)
		}
		if floor.IsDeniedRel(".paintedwolf/settings.yaml") {
			t.Fatalf("token %q should disable control-plane read floor, but .paintedwolf remained denied", token)
		}
		if !floor.IsDeniedRel(".ssh/id_ed25519") {
			t.Fatalf("token %q must still enforce pack KeyMaterialReadDenyPaths (.ssh)", token)
		}
	}
}

func TestCompileAgentReadFloor_ManagedWorkspaceRespectsExplicitExtraDenyRoots(t *testing.T) {
	installTestKeyMaterialPaths(t)
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	wtDir := filepath.Join(cfgDir, "worktrees", "wt-1")
	wtPrivate := filepath.Join(wtDir, "private")
	for _, dir := range []string{cfgDir, wtDir, wtPrivate, filepath.Join(wtDir, "src")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// 1. Default: managed workspace <configdir>/worktrees/wt-1 carves out the control-directory parent,
	// while still denying symlink escapes back into configdir.Root().
	wtFloor := CompileAgentReadFloor(wtDir)
	if wtFloor.WhollyDenied() || wtFloor.IsDeniedRel("src/main.go") ||
		wtFloor.IsDeniedAbs("src/main.go", filepath.Join(wtDir, "src", "main.go")) ||
		IsAgentSourceReadDenied(wtDir, filepath.Join(wtDir, "src", "main.go")) {
		t.Fatal("expected explicit managed workspace to be allowed by default")
	}
	if !wtFloor.IsDeniedAbs("escape", filepath.Join(cfgDir, "settings.yaml")) ||
		!IsAgentSourceReadDenied(wtDir, filepath.Join(cfgDir, "settings.yaml")) {
		t.Fatal("expected escape from managed workspace into configdir.Root() to be denied")
	}

	// 2. Subtree deny inside managed workspace via LYCAON_SANDBOX_DENY_READ:
	// WhollyDenied, IsDeniedRel, IsDeniedAbs, and IsAgentSourceReadDenied must all agree!
	t.Setenv("LYCAON_SANDBOX_DENY_READ", wtPrivate)
	subFloor := CompileAgentReadFloor(wtDir)
	if subFloor.WhollyDenied() {
		t.Fatal("expected subtree deny in managed workspace not to mark whole workspace denied")
	}
	if !slices.Contains(subFloor.DeniedRelPrefixes(), "private") {
		t.Fatalf("expected 'private' in DeniedRelPrefixes, got %v", subFloor.DeniedRelPrefixes())
	}
	if !subFloor.IsDeniedRel("private") || !subFloor.IsDeniedRel("private/secret.txt") ||
		!subFloor.IsDeniedAbs("private/secret.txt", filepath.Join(wtPrivate, "secret.txt")) ||
		!IsAgentSourceReadDenied(wtDir, filepath.Join(wtPrivate, "secret.txt")) {
		t.Fatal("expected LYCAON_SANDBOX_DENY_READ subtree inside managed workspace to be denied across all checks")
	}
	if subFloor.IsDeniedRel("src/main.go") ||
		subFloor.IsDeniedAbs("src/main.go", filepath.Join(wtDir, "src", "main.go")) ||
		IsAgentSourceReadDenied(wtDir, filepath.Join(wtDir, "src", "main.go")) {
		t.Fatal("expected non-denied sibling inside managed workspace to remain allowed")
	}

	// 3. Whole-root deny on managed workspace via LYCAON_SANDBOX_DENY_READ:
	// Managed-workspace carve-out must NEVER override explicit LYCAON_SANDBOX_DENY_READ!
	t.Setenv("LYCAON_SANDBOX_DENY_READ", wtDir)
	wholeFloor := CompileAgentReadFloor(wtDir)
	if !wholeFloor.WhollyDenied() || !wholeFloor.IsDeniedRel(".") || !wholeFloor.IsDeniedRel("src/main.go") ||
		!wholeFloor.IsDeniedAbs("src/main.go", filepath.Join(wtDir, "src", "main.go")) ||
		!IsAgentSourceReadDenied(wtDir, filepath.Join(wtDir, "src", "main.go")) {
		t.Fatal("expected managed workspace explicitly covered by LYCAON_SANDBOX_DENY_READ to be wholly denied across all checks")
	}
}

func TestCompileAgentReadFloor_SessionOwnershipRequired(t *testing.T) {
	installTestKeyMaterialPaths(t)
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	ownScratch := filepath.Join(cfgDir, "scratch", "sess-1")
	ownOutput := filepath.Join(cfgDir, "session-output", "sess-1")
	otherScratch := filepath.Join(cfgDir, "scratch", "other-session")
	otherOutput := filepath.Join(cfgDir, "session-output", "other-session")
	for _, dir := range []string{cfgDir, ownScratch, ownOutput, otherScratch, otherOutput} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// 1. Without invocation session ownership, a concrete <configdir>/scratch/<id> path is wholly denied.
	for _, unowned := range []string{ownScratch, ownOutput, otherScratch, otherOutput, filepath.Join(cfgDir, "scratch")} {
		floor := CompileAgentReadFloor(unowned)
		if !floor.WhollyDenied() || !floor.IsDeniedRel("note.txt") ||
			!floor.IsDeniedAbs("note.txt", filepath.Join(unowned, "note.txt")) ||
			!IsAgentSourceReadDenied(unowned, filepath.Join(unowned, "note.txt")) {
			t.Fatalf("expected %s without invocation session ownership to be wholly denied", unowned)
		}
	}

	// 2. With invocation session ownership (ownScratch, ownOutput), ONLY the invocation's own
	// session directories are allowed.
	for _, owned := range []string{ownScratch, ownOutput} {
		floor := CompileAgentReadFloor(owned, ownScratch, ownOutput)
		if floor.WhollyDenied() || floor.IsDeniedRel("note.txt") ||
			floor.IsDeniedAbs("note.txt", filepath.Join(owned, "note.txt")) ||
			IsAgentSourceReadDenied(owned, filepath.Join(owned, "note.txt"), ownScratch, ownOutput) {
			t.Fatalf("expected invocation-owned session dir %s to be allowed", owned)
		}
		// Escaping from ownScratch into configdir.Root() or into otherScratch is still denied.
		if !floor.IsDeniedAbs("escape", filepath.Join(cfgDir, "settings.yaml")) ||
			!IsAgentSourceReadDenied(owned, filepath.Join(cfgDir, "settings.yaml"), ownScratch, ownOutput) ||
			!IsAgentSourceReadDenied(owned, filepath.Join(otherScratch, "secret.txt"), ownScratch, ownOutput) {
			t.Fatalf("expected escape from invocation-owned session dir %s to be denied", owned)
		}
	}

	// 3. Attaching another session's scratch/output directory (<configdir>/scratch/other-session)
	// while the invocation owns (ownScratch, ownOutput) MUST be wholly denied!
	for _, other := range []string{otherScratch, otherOutput} {
		otherFloor := CompileAgentReadFloor(other, ownScratch, ownOutput)
		if !otherFloor.WhollyDenied() || !otherFloor.IsDeniedRel("secret.txt") ||
			!otherFloor.IsDeniedAbs("secret.txt", filepath.Join(other, "secret.txt")) ||
			!IsAgentSourceReadDenied(other, filepath.Join(other, "secret.txt"), ownScratch, ownOutput) {
			t.Fatalf("expected other session dir %s to be wholly denied even when invocation owns sess-1", other)
		}
	}

	// 4. Explicit LYCAON_SANDBOX_DENY_READ covering ownScratch overrides even invocation ownership.
	t.Setenv("LYCAON_SANDBOX_DENY_READ", ownScratch)
	deniedOwn := CompileAgentReadFloor(ownScratch, ownScratch, ownOutput)
	if !deniedOwn.WhollyDenied() || !IsAgentSourceReadDenied(ownScratch, filepath.Join(ownScratch, "note.txt"), ownScratch, ownOutput) {
		t.Fatal("expected explicit LYCAON_SANDBOX_DENY_READ to deny even invocation-owned scratch")
	}
}
