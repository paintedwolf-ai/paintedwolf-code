package confine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPolicyWriteAuthorityIsExactAndRevalidated(t *testing.T) {
	dir := t.TempDir()
	roots := []string{dir}
	path := filepath.Join(dir, "AGENTS.md")
	grant := confine.NewProtectedPathGrant(path)
	approved, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{grant}, roots)
	testutil.FailErr(t, "validate new instruction file", err)
	if len(approved) != 1 || approved[0].Subtree {
		t.Fatal("instruction approval widened")
	}
	testutil.FailErr(t, "substitute directory", os.Mkdir(path, 0o755))
	if _, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{grant}, roots); err == nil {
		t.Fatal("file grant accepted a directory")
	}
	if _, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{confine.NewProtectedPathGrant(path)}, roots); err == nil {
		t.Fatal("instruction approval accepted a subtree")
	}
	testutil.FailErr(t, "remove fixture directory", os.Remove(path))
	testutil.FailErr(t, "substitute symlink", os.Symlink(filepath.Join(dir, "other.md"), path))
	if _, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{grant}, roots); err == nil {
		t.Fatal("instruction approval followed a symlink")
	}
	outside := filepath.Join(t.TempDir(), "AGENTS.md")
	if _, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{confine.NewProtectedPathGrant(outside)}, roots); err == nil {
		t.Fatal("agent-policy approval accepted a file no project loads")
	}
}

// A directory grant is agent policy only inside a tree a loader reads whole.
func TestPolicyWriteSubtreeStaysInsideALoaderTree(t *testing.T) {
	dir := t.TempDir()
	roots := []string{dir}
	skills := filepath.Join(dir, ".agents", "skills")
	skill := filepath.Join(skills, "review")
	testutil.FailErr(t, "create skill", os.MkdirAll(skill, 0o755))
	for _, tree := range []string{skills, skill} {
		approved, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{confine.NewProtectedPathGrant(tree)}, roots)
		testutil.FailErr(t, "validate "+tree, err)
		if len(approved) != 1 || !approved[0].Subtree {
			t.Fatalf("%s: approval = %+v, want one subtree", tree, approved)
		}
	}
	for _, wider := range []string{filepath.Join(dir, ".agents"), dir} {
		if _, err := confine.ValidatePolicyWriteGrants([]confine.ProtectedPathGrant{confine.NewProtectedPathGrant(wider)}, roots); err == nil {
			t.Fatalf("agent-policy approval accepted %s, which holds more than a loader tree", wider)
		}
	}
}

func TestSeatbeltPolicyApprovalCannotBeReplacedByAncestorGrant(t *testing.T) {
	self := requireSeatbelt(t)
	dir := t.TempDir()
	target, sibling := filepath.Join(dir, "AGENTS.md"), filepath.Join(dir, "nested", "AGENTS.md")
	testutil.FailErr(t, "create nested directory", os.MkdirAll(filepath.Dir(sibling), 0o755))
	boundary := confine.Confinement{Roots: []string{dir}, ProtectedWriteGrants: []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(dir)}}
	if code := confinedExit(t, self, boundary, "/usr/bin/touch", target); code == 0 {
		t.Fatal("ancestor grant allowed instruction write")
	}
	boundary.PolicyWriteGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(target)}
	if code := confinedExit(t, self, boundary, "/usr/bin/touch", target); code != 0 {
		t.Fatalf("approved instruction write denied: %d", code)
	}
	if code := confinedExit(t, self, boundary, "/usr/bin/touch", sibling); code == 0 {
		t.Fatal("exact approval allowed nested instructions")
	}
	boundary.PolicyWriteGrants = nil
	if code := confinedExit(t, self, boundary, "/usr/bin/touch", target); code == 0 {
		t.Fatal("invocation grant escaped its boundary")
	}
}

// One approved skill tree lets a command create a new skill, and nothing else.
func TestSeatbeltSkillTreeGrantCoversANewSkill(t *testing.T) {
	self := requireSeatbelt(t)
	dir := t.TempDir()
	skills := filepath.Join(dir, ".agents", "skills")
	testutil.FailErr(t, "create skills", os.MkdirAll(skills, 0o755))
	created := filepath.Join(skills, "ship", "SKILL.md")
	boundary := confine.Confinement{Roots: []string{dir}}
	create := "mkdir -p \"$(dirname \"$1\")\" && : > \"$1\""
	if code := confinedExit(t, self, boundary, "/bin/sh", "-c", create, "sh", created); code == 0 {
		t.Fatal("unapproved command created a skill")
	}
	boundary.PolicyWriteGrants = []confine.ProtectedPathGrant{confine.NewProtectedPathGrant(skills)}
	if code := confinedExit(t, self, boundary, "/bin/sh", "-c", create, "sh", created); code != 0 {
		t.Fatalf("approved skill tree refused a new skill: %d", code)
	}
	if code := confinedExit(t, self, boundary, "/usr/bin/touch", filepath.Join(dir, "AGENTS.md")); code == 0 {
		t.Fatal("skill tree approval reached project instructions")
	}
}
