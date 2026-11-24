package gitrepo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	base := []string{
		"-c", "user.name=Test",
		"-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false",
		"-C", dir,
	}
	cmd := exec.Command("git", append(base, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func initCommittedRepo(t *testing.T, dir string) {
	t.Helper()
	testutil.FailErr(t, "mkdir repo", os.MkdirAll(dir, 0o755))
	runGit(t, dir, "init", "-q", "-b", "main")
	testutil.FailErr(t, "mkdir packages/a", os.MkdirAll(filepath.Join(dir, "packages", "a"), 0o755))
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "packages", "a", "hello.txt"), []byte("hello\n"), 0o644))
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-qm", "initial")
}

func TestVerifyRegisteredCheckoutCoversPrimaryLinkedSpacesRelativeAndRejectsForgeries(t *testing.T) {
	t.Parallel()
	base := canonicalTempDir(t)
	primary := filepath.Join(base, "primary")
	initCommittedRepo(t, primary)

	sibling := filepath.Join(base, "sibling with space")
	runGit(t, primary, "worktree", "add", "-q", "-b", "feature", sibling)

	detached := filepath.Join(base, "detached-wt")
	runGit(t, primary, "worktree", "add", "-q", "--detach", detached)

	relativeSibling := filepath.Join(base, "relative-wt")
	runGit(t, primary, "worktree", "add", "-q", "-b", "rel-branch", relativeSibling)
	makeRegistrationRelative(t, relativeSibling)

	commonDir, primaryRoot, bare, ok := gitrepo.CommonRepository(primary)
	if !ok || bare || primaryRoot != primary {
		t.Fatalf("CommonRepository(primary) = (%q, %q, %v, %v)", commonDir, primaryRoot, bare, ok)
	}
	fromLinkedCommon, fromLinkedPrimary, fromLinkedBare, ok := gitrepo.CommonRepository(sibling)
	if !ok || fromLinkedBare || fromLinkedCommon != commonDir || fromLinkedPrimary != primary {
		t.Fatalf("CommonRepository(sibling) = (%q, %q, %v, %v), want (%q, %q, false, true)", fromLinkedCommon, fromLinkedPrimary, fromLinkedBare, ok, commonDir, primary)
	}

	for _, tc := range []struct {
		name string
		dir  string
		kind gitrepo.CheckoutKind
	}{
		{name: "primary", dir: primary, kind: gitrepo.CheckoutPrimary},
		{name: "linked with spaces", dir: sibling, kind: gitrepo.CheckoutLinked},
		{name: "detached", dir: detached, kind: gitrepo.CheckoutLinked},
		{name: "relative pointers", dir: relativeSibling, kind: gitrepo.CheckoutLinked},
	} {
		reg, verified := gitrepo.VerifyRegisteredCheckout(commonDir, tc.dir)
		if !verified || !reg.Available || reg.Kind != tc.kind || reg.Root != tc.dir || reg.CommonDir != commonDir {
			t.Fatalf("%s: got (%+v, %v)", tc.name, reg, verified)
		}
		if reg.AdminIdentity == "" || reg.WorktreeIdentity == "" || reg.CommonIdentity == "" {
			t.Fatalf("%s: expected filesystem identities on %+v", tc.name, reg)
		}
	}

	// Copied .git marker in an unregistered directory must fail reverse-pointer verification.
	bogus := filepath.Join(base, "bogus-copy")
	testutil.FailErr(t, "mkdir bogus", os.MkdirAll(bogus, 0o755))
	markerBytes, err := os.ReadFile(filepath.Join(sibling, ".git"))
	testutil.FailErr(t, "read sibling .git", err)
	testutil.FailErr(t, "write bogus .git", os.WriteFile(filepath.Join(bogus, ".git"), markerBytes, 0o644))
	if reg, verified := gitrepo.VerifyRegisteredCheckout(commonDir, bogus); verified {
		t.Fatalf("accepted copied .git marker: %+v", reg)
	}

	// Fake reverse pointer from an unrelated repository must fail forward verification.
	other := filepath.Join(base, "other-repo")
	initCommittedRepo(t, other)
	fakeAdmin := filepath.Join(other, ".git", "worktrees", "hijack")
	testutil.FailErr(t, "mkdir fake admin", os.MkdirAll(fakeAdmin, 0o755))
	testutil.FailErr(t, "write fake commondir", os.WriteFile(filepath.Join(fakeAdmin, "commondir"), []byte("../..\n"), 0o644))
	testutil.FailErr(t, "write fake gitdir", os.WriteFile(filepath.Join(fakeAdmin, "gitdir"), []byte(filepath.Join(sibling, ".git")+"\n"), 0o644))
	if reg, verified := gitrepo.VerifyRegisteredCheckout(filepath.Join(other, ".git"), sibling); verified {
		t.Fatalf("accepted fake reverse pointer from unrelated repo: %+v", reg)
	}

	// Bare common repository reports bare=true and never verifies itself as an editable checkout.
	bareRepo := filepath.Join(base, "bare.git")
	runGit(t, base, "clone", "-q", "--bare", primary, bareRepo)
	bareCommon, barePrimary, isBare, ok := gitrepo.CommonRepository(bareRepo)
	if !ok || !isBare || barePrimary != "" || bareCommon != bareRepo {
		t.Fatalf("CommonRepository(bare) = (%q, %q, %v, %v)", bareCommon, barePrimary, isBare, ok)
	}
	if reg, verified := gitrepo.VerifyRegisteredCheckout(bareCommon, bareRepo); verified {
		t.Fatalf("bare repository verified as editable checkout: %+v", reg)
	}
	bareLinked := filepath.Join(base, "bare-linked")
	runGit(t, bareRepo, "worktree", "add", "-q", "-b", "from-bare", bareLinked)
	if reg, verified := gitrepo.VerifyRegisteredCheckout(bareCommon, bareLinked); !verified || reg.Kind != gitrepo.CheckoutLinked {
		t.Fatalf("linked worktree of bare repo rejected: %+v, %v", reg, verified)
	}
}

func TestInspectListedCheckoutRetainsAdminIdentityWhenWorktreeMissing(t *testing.T) {
	t.Parallel()
	base := canonicalTempDir(t)
	primary := filepath.Join(base, "primary")
	initCommittedRepo(t, primary)
	sibling := filepath.Join(base, "gone-sibling")
	runGit(t, primary, "worktree", "add", "-q", "-b", "gone", sibling)

	commonDir := filepath.Join(primary, ".git")
	before, ok := gitrepo.VerifyRegisteredCheckout(commonDir, sibling)
	if !ok {
		t.Fatal("expected sibling verified before removal")
	}
	testutil.FailErr(t, "remove sibling tree without git prune", os.RemoveAll(sibling))

	inspected := gitrepo.InspectListedCheckout(commonDir, sibling)
	if inspected.Available {
		t.Fatalf("missing sibling reported Available=true: %+v", inspected)
	}
	if inspected.AdminDir != before.AdminDir || inspected.AdminIdentity != before.AdminIdentity {
		t.Fatalf("missing sibling lost admin identity: got %+v, want %+v", inspected, before)
	}
}

func TestProjectSubtreeRespectsPartialRootsAndNestedRepos(t *testing.T) {
	t.Parallel()
	base := canonicalTempDir(t)
	primary := filepath.Join(base, "primary")
	initCommittedRepo(t, primary)

	sibling := filepath.Join(base, "sibling")
	runGit(t, primary, "worktree", "add", "-q", "-b", "sib", sibling)

	nested := filepath.Join(sibling, "packages", "nested-unrelated")
	initCommittedRepo(t, nested)

	// Attached partial root primary/packages/a maps only to sibling/packages/a.
	attachedSub := filepath.Join(primary, "packages", "a")
	wantSub := filepath.Join(sibling, "packages", "a")
	projectedSub, ok := gitrepo.ProjectSubtree(primary, sibling, attachedSub)
	if !ok || projectedSub != wantSub {
		t.Fatalf("unexpected projection: %q (%v), want %q", projectedSub, ok, wantSub)
	}

	// Nested unrelated repository inside sibling must not cross boundary.
	if projected, ok := gitrepo.ProjectSubtree(primary, sibling, filepath.Join(primary, "packages", "nested-unrelated")); ok {
		t.Fatalf("ProjectSubtree crossed nested repository boundary: %q", projected)
	}

	// Linked checkout root also projects back onto primary checkout.
	projectedPrimary, ok := gitrepo.ProjectSubtree(sibling, primary, wantSub)
	if !ok || projectedPrimary != attachedSub {
		t.Fatalf("linked root did not project onto primary checkout: %q (%v), want %q", projectedPrimary, ok, attachedSub)
	}
}

func makeRegistrationRelative(t *testing.T, checkout string) {
	t.Helper()
	dotGit := filepath.Join(checkout, ".git")
	raw, err := os.ReadFile(dotGit)
	testutil.FailErr(t, "read .git", err)
	adminDir := filepath.Clean(strings.TrimSpace(strings.TrimPrefix(string(raw), "gitdir:")))
	relAdmin, err := filepath.Rel(checkout, adminDir)
	testutil.FailErr(t, "rel admin", err)
	relBack, err := filepath.Rel(adminDir, dotGit)
	testutil.FailErr(t, "rel back", err)
	testutil.FailErr(t, "write relative .git", os.WriteFile(dotGit, []byte("gitdir: "+filepath.ToSlash(relAdmin)+"\n"), 0o644))
	testutil.FailErr(t, "write relative gitdir", os.WriteFile(filepath.Join(adminDir, "gitdir"), []byte(filepath.ToSlash(relBack)+"\n"), 0o644))
}
