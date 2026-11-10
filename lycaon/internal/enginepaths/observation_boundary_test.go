package enginepaths

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
)

func TestIsObservedStateExcluded_StrictAncestorExcludesActiveControlDirAndPlacements(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".config", "paintedwolf")
	if err := os.MkdirAll(filepath.Join(cfgDir, "packs"), 0o755); err != nil {
		t.Fatalf("mkdir packs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "worktrees", "wt-1"), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "drafts", "draft-1"), 0o755); err != nil {
		t.Fatalf("mkdir drafts: %v", err)
	}
	placementDir := filepath.Join(home, ".paintedwolf-placement-vol1")
	if err := os.MkdirAll(filepath.Join(placementDir, "worker-branches", "proj-1", "job-1"), 0o755); err != nil {
		t.Fatalf("mkdir placement: %v", err)
	}
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// When strict ancestor ($HOME or ~/.config) is attached:
	// - Entire configdir.Root() (including settings.yaml, packs/, and nested worktrees/) is excluded from parent walk.
	// - Entire .paintedwolf-placement-* is excluded from parent walk.
	// - Sibling user project files remain observable.
	for _, root := range []string{home, filepath.Join(home, ".config")} {
		for _, excluded := range []string{
			cfgDir,
			filepath.Join(cfgDir, "settings.yaml"),
			filepath.Join(cfgDir, "packs", "rule.yaml"),
			filepath.Join(cfgDir, "paintedwolf.db"),
			filepath.Join(cfgDir, "paintedwolf.db-wal"),
			filepath.Join(cfgDir, "store.db-shm"),
			filepath.Join(cfgDir, "worktrees", "wt-1", "main.go"),
			filepath.Join(cfgDir, "drafts", "draft-1", "main.go"),
		} {
			if !IsObservedStateExcluded(root, excluded) {
				t.Errorf("root=%q target=%q: expected excluded under strict ancestor", root, excluded)
			}
		}
	}

	if !IsObservedStateExcluded(home, placementDir) ||
		!IsObservedStateExcluded(home, filepath.Join(placementDir, "worker-branches", "proj-1", "job-1", "main.go")) {
		t.Fatal("expected worker placement directory excluded when ancestor root is attached")
	}

	if IsObservedStateExcluded(home, filepath.Join(home, "projects", "app", "main.go")) {
		t.Fatal("expected sibling user project file to remain observable under home root")
	}

	// Explicit managed-workspace and concrete session scratch/output attachments remain observable when attached directly.
	for _, wsRoot := range []string{
		filepath.Join(cfgDir, "worktrees", "wt-1"),
		filepath.Join(cfgDir, "drafts", "draft-1"),
		filepath.Join(cfgDir, "session-worktrees", "proj-1", "sess-1"),
		filepath.Join(cfgDir, "worker-branches", "proj-1", "job-1"),
		filepath.Join(placementDir, "worker-branches", "proj-1", "job-1"),
		filepath.Join(cfgDir, "scratch", "sess-1"),
		filepath.Join(cfgDir, "session-output", "sess-1"),
		filepath.Join(cfgDir, "sessions", "sess-1", "scratch"),
		filepath.Join(cfgDir, "sessions", "sess-1", "output"),
	} {
		if IsObservedStateExcluded(wsRoot, wsRoot) || IsObservedStateExcluded(wsRoot, filepath.Join(wsRoot, "src", "main.go")) {
			t.Errorf("scoped root %q should be observable when explicitly attached", wsRoot)
		}
		// Escaping from one scoped root into another session or into configdir.Root() is still excluded.
		if !IsObservedStateExcluded(wsRoot, filepath.Join(cfgDir, "settings.yaml")) {
			t.Errorf("scoped root %q must exclude control-plane sibling settings.yaml", wsRoot)
		}
		if !IsObservedStateExcluded(wsRoot, filepath.Join(cfgDir, "scratch", "sess-2", "note.txt")) {
			t.Errorf("scoped root %q must exclude another session's scratch directory", wsRoot)
		}
	}
	// Parent container <cfgDir>/scratch or <cfgDir>/session-output without a concrete session ID remains excluded.
	scratchParent := filepath.Join(cfgDir, "scratch")
	if !IsObservedStateExcluded(scratchParent, scratchParent) ||
		!IsObservedStateExcluded(scratchParent, filepath.Join(scratchParent, "sess-1", "note.txt")) {
		t.Fatal("parent container <cfgDir>/scratch must remain excluded")
	}
	// Worker branch .meta directory under <cfgDir>/worker-branches/<proj>/.meta remains excluded.
	metaDir := filepath.Join(cfgDir, "worker-branches", "proj-1", ".meta")
	if !IsObservedStateExcluded(metaDir, metaDir) || !IsObservedStateExcluded(metaDir, filepath.Join(metaDir, "job-1.json")) {
		t.Fatal("worker-branches .meta directory must remain excluded")
	}
}

func TestIsObservedStateExcluded_DirectAndChildControlDirAttachment(t *testing.T) {
	base := t.TempDir()
	cfgDir := filepath.Join(base, "paintedwolf-state")
	if err := os.MkdirAll(filepath.Join(cfgDir, "packs", "sub"), 0o755); err != nil {
		t.Fatalf("mkdir packs/sub: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "cache", "nested"), 0o755); err != nil {
		t.Fatalf("mkdir cache/nested: %v", err)
	}
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// Runtime/DB/WAL/SHM/cache/session/secret paths under configdir.Root() must be excluded.
	runtimePaths := []string{
		"store.db",
		"store.db-wal",
		"store.db-shm",
		"store.db-journal",
		"source-observations.db",
		"source-observations.db-wal",
		"web-index.db",
		"paintedwolf.db",
		"paintedwolf.db-wal",
		"paintedwolf.db-shm",
		"source-catalog.db",
		"source-catalog.db-wal",
		"source-catalog.db-shm",
		"runtime.json",
		"daemon.lock",
		"daemon.json",
		"cli.sock",
		"api.token",
		"cache",
		"cache/nested/item.bin",
		"logs/daemon.log",
		"sessions/s1.json",
		"staging/tmp-1",
		"blobs/sha256-1",
		"tls/cert.pem",
		"work/job-1",
		"auth.json",
		"secrets/key.age",
		"master.key",
		"credential-vault.age",
		"credential-vault-identity.age",
		"credential-vault-dev.key",
		"host-identity.pem",
		"locks/store.lock",
		"projects/p1/meta.json",
		"source-content/blob",
		"session-checkpoints/p1/c1",
		"scratch/s1/note.txt",
		"worktrees/wt-1/main.go",
		"drafts/d1/main.go",
		".store-upgrade-123/store.db",
		".restore-staging/file",
		".paintedwolf-tmp-abc",
	}
	for _, rel := range runtimePaths {
		target := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if !IsObservedStateExcluded(cfgDir, target) {
			t.Errorf("direct control-root attachment failed to exclude runtime path %q", rel)
		}
	}

	// If attachment starts inside an excluded runtime child (e.g. <configdir>/cache), it remains excluded.
	cacheRoot := filepath.Join(cfgDir, "cache")
	if !IsObservedStateExcluded(cacheRoot, cacheRoot) ||
		!IsObservedStateExcluded(cacheRoot, filepath.Join(cacheRoot, "nested", "item.bin")) {
		t.Fatal("attachment starting inside <configdir>/cache must remain excluded")
	}

	// Human-authored config files and directories under configdir.Root() remain observable,
	// including files with .db or .lock extensions inside human subtrees (no blanket globs!).
	humanPaths := []string{
		".",
		"settings.yaml",
		"mcp.yaml",
		"source-scope.yaml",
		"agents/reviewer.md",
		"packs/custom.yaml",
		"packs/sub/fixtures.db",
		"packs/sub/custom.lock",
		"skills/my-skill/SKILL.md",
	}
	for _, rel := range humanPaths {
		target := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if IsObservedStateExcluded(cfgDir, target) {
			t.Errorf("direct control-root attachment wrongly excluded human config path %q", rel)
		}
	}

	// Child config attachment (<configdir>/packs) keeps its descendants observable.
	packsRoot := filepath.Join(cfgDir, "packs")
	if IsObservedStateExcluded(packsRoot, filepath.Join(packsRoot, "sub", "fixtures.db")) {
		t.Fatal("child attachment <configdir>/packs wrongly excluded packs/sub/fixtures.db")
	}
}

func TestIsObservedStateExcluded_CanonicalAliasesSymlinksAndCase(t *testing.T) {
	realBase := t.TempDir()
	aliasBaseParent := t.TempDir()
	aliasBase := filepath.Join(aliasBaseParent, "alias-home")
	if err := os.Symlink(realBase, aliasBase); err != nil {
		t.Fatalf("symlink alias home: %v", err)
	}

	cfgReal := filepath.Join(realBase, ".paintedwolf")
	if err := os.MkdirAll(filepath.Join(cfgReal, "packs"), 0o755); err != nil {
		t.Fatalf("mkdir cfgReal/packs: %v", err)
	}
	// Configure configdir via the symlinked alias path.
	cfgViaAlias := filepath.Join(aliasBase, ".paintedwolf")
	t.Setenv(configdir.EnvConfigDir, cfgViaAlias)

	// Attaching realBase must still exclude cfgReal (and vice versa).
	if !IsObservedStateExcluded(realBase, filepath.Join(cfgReal, "settings.yaml")) {
		t.Fatal("canonical root failed to exclude configdir configured via alias")
	}
	if !IsObservedStateExcluded(aliasBase, filepath.Join(cfgReal, "settings.yaml")) {
		t.Fatal("alias root failed to exclude canonical configdir path")
	}

	// Symlink inside a project directory pointing into configdir.Root() must be excluded.
	projDir := filepath.Join(realBase, "project")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir projDir: %v", err)
	}
	escapeLink := filepath.Join(projDir, "linked-control")
	if err := os.Symlink(cfgReal, escapeLink); err != nil {
		t.Fatalf("symlink escapeLink: %v", err)
	}
	if !IsObservedDirChildExcluded(projDir, projDir, "linked-control", true) {
		t.Fatal("expected symlink pointing into configdir.Root() to be excluded")
	}

	// Case-insensitive runtime entry matching under configdir.Root().
	if !IsObservedStateExcluded(cfgReal, filepath.Join(cfgReal, "PaintedWolf.DB-WAL")) ||
		!IsObservedStateExcluded(cfgReal, filepath.Join(cfgReal, "CACHE", "entry")) {
		t.Fatal("expected case-insensitive runtime entry exclusion under configdir.Root()")
	}
}

func TestCompileAgentReadFloor(t *testing.T) {
	home := t.TempDir()
	cfgDir := filepath.Join(home, ".paintedwolf")
	sshDir := filepath.Join(home, ".ssh")
	wtDir := filepath.Join(cfgDir, "worktrees", "wt-1")
	for _, dir := range []string{cfgDir, sshDir, wtDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, cfgDir)

	// 1. Direct configdir.Root() attachment is wholly denied to AgentAudience.
	cfgFloor := CompileAgentReadFloor(cfgDir, nil)
	if !cfgFloor.WhollyDenied() || !cfgFloor.IsDeniedRel("settings.yaml") || !cfgFloor.IsDeniedRel(".") {
		t.Fatal("expected direct configdir.Root() attachment to be wholly denied to AgentAudience")
	}

	// 2. Direct ~/.ssh attachment is wholly denied to AgentAudience.
	sshFloor := CompileAgentReadFloor(sshDir, nil)
	if !sshFloor.WhollyDenied() || !sshFloor.IsDeniedRel("id_ed25519") {
		t.Fatal("expected direct ~/.ssh attachment to be wholly denied to AgentAudience")
	}

	// 3. Ancestor $HOME attachment denies .paintedwolf and .ssh prefixes while allowing normal project files.
	homeFloor := CompileAgentReadFloor(home, nil)
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

	// 4. Explicit managed workspace (<configdir>/worktrees/wt-1) and concrete session scratch/output are allowed to AgentAudience.
	wtFloor := CompileAgentReadFloor(wtDir, nil)
	if wtFloor.WhollyDenied() || wtFloor.IsDeniedRel("src/main.go") {
		t.Fatal("expected explicit managed workspace to be allowed to AgentAudience")
	}
	scratchSessionDir := filepath.Join(cfgDir, "scratch", "sess-1")
	outputSessionDir := filepath.Join(cfgDir, "session-output", "sess-1")
	for _, dir := range []string{scratchSessionDir, outputSessionDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		floor := CompileAgentReadFloor(dir, nil)
		if floor.WhollyDenied() || floor.IsDeniedRel("note.txt") || floor.IsDeniedAbs("note.txt", filepath.Join(dir, "note.txt"), nil) {
			t.Fatalf("expected concrete session dir %s to be allowed to AgentAudience", dir)
		}
		if IsAgentSourceReadDenied(dir, filepath.Join(dir, "note.txt"), nil) {
			t.Fatalf("expected target inside concrete session dir %s to be allowed", dir)
		}
		if !IsAgentSourceReadDenied(dir, filepath.Join(cfgDir, "settings.yaml"), nil) ||
			!IsAgentSourceReadDenied(dir, filepath.Join(cfgDir, "scratch", "sess-2", "note.txt"), nil) {
			t.Fatalf("expected target outside concrete session dir %s to be denied", dir)
		}
	}
	// Parent <cfgDir>/scratch container remains wholly denied.
	if !CompileAgentReadFloor(filepath.Join(cfgDir, "scratch"), nil).WhollyDenied() {
		t.Fatal("expected parent <cfgDir>/scratch container to be wholly denied")
	}
}
