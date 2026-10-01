package enginepaths

import (
	"path/filepath"
	"slices"
	"testing"
)

func TestSessionWorktreeDirLayout(t *testing.T) {
	root := SessionWorktreesRootUnder("/state/lycaon")
	if root != filepath.FromSlash("/state/lycaon/session-worktrees") {
		t.Fatalf("worktree root = %q", root)
	}
	session := SessionWorktreeDir(root, "/Users/me/repo", "sess-1")
	want := filepath.Join(root, ProjectKey("/Users/me/repo"), "sess-1")
	if session != want {
		t.Fatalf("session dir = %q want %q", session, want)
	}
}

func TestAgentWorkspaceRootsUnder_includesSessionWorktrees(t *testing.T) {
	dir := "/state/lycaon"
	roots := AgentWorkspaceRootsUnder(dir)
	want := SessionWorktreesRootUnder(dir)
	if !slices.Contains(roots, want) {
		t.Fatalf("AgentWorkspaceRootsUnder missing session worktrees carve-out %q; got %v", want, roots)
	}
}

func TestSessionCheckpointDirLayout(t *testing.T) {
	root := SessionCheckpointsRootUnder("/state/lycaon")
	if root != filepath.FromSlash("/state/lycaon/session-checkpoints") {
		t.Fatalf("checkpoint root = %q", root)
	}
	project := ProjectCheckpointDir(root, "/Users/me/repo")
	if want := filepath.Join(root, ProjectKey("/Users/me/repo")); project != want {
		t.Fatalf("project checkpoint dir = %q want %q", project, want)
	}
	// Checkpoint manifests restore paths relative to their source root.
	if other := ProjectCheckpointDir(root, "/Users/me/other-repo"); other == project {
		t.Fatal("checkpoint dirs collide across source roots")
	}
}

// Checkpoints can contain files outside a command's granted read scope.
func TestAgentWorkspaceRootsUnder_excludesSessionCheckpoints(t *testing.T) {
	dir := "/state/lycaon"
	if roots := AgentWorkspaceRootsUnder(dir); slices.Contains(roots, SessionCheckpointsRootUnder(dir)) {
		t.Fatalf("session checkpoints must stay outside the agent-workspace carve-outs; got %v", roots)
	}
}

func TestSessionScratchDirLayout(t *testing.T) {
	root := ScratchRootUnder("/state/lycaon")
	if root != filepath.FromSlash("/state/lycaon/scratch") {
		t.Fatalf("scratch root = %q", root)
	}
	session := SessionScratchUnder("/state/lycaon", "sess-1")
	want := filepath.FromSlash("/state/lycaon/scratch/sess-1")
	if session != want {
		t.Fatalf("session scratch dir = %q want %q", session, want)
	}
}

// Scratch is session-owned and granted per execution; it stays outside the global carve-outs
// so sessions cannot read each other's scratch files.
func TestAgentWorkspaceRootsUnder_excludesScratch(t *testing.T) {
	dir := "/state/lycaon"
	if roots := AgentWorkspaceRootsUnder(dir); slices.Contains(roots, ScratchRootUnder(dir)) {
		t.Fatalf("scratch root must stay outside the global agent-workspace carve-outs; got %v", roots)
	}
}
