package blueprint

import (
	"context"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"testing"

	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestUpdateRetargetsProvisionalPathOnce(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, blueprintfile.PlaceholderTitle, "", "plan", "")
	testutil.FailErr(t, "Create", err)
	from := bp.Path
	if !IsProvisionalPath(from) {
		t.Fatalf("minted path %q should be provisional", from)
	}

	title := "Ship OAuth"
	updated, err := mgr.Update(ctx, testdbseed.DefaultProjectID, from, nil, &title)
	testutil.FailErr(t, "first title Update", err)
	if updated.Path == from {
		t.Fatal("provisional path must move onto the declared slug")
	}
	if IsProvisionalPath(updated.Path) {
		t.Fatalf("retargeted path still provisional: %q", updated.Path)
	}
	if got := PathFileStem(updated.Path); got != "ship-oauth" {
		t.Fatalf("stem = %q want ship-oauth", got)
	}
	if _, err := mgr.Get(ctx, testdbseed.DefaultProjectID, from); err == nil {
		t.Fatal("old provisional path must be gone")
	}

	next := "Ship OAuth v2"
	again, err := mgr.Update(ctx, testdbseed.DefaultProjectID, updated.Path, nil, &next)
	testutil.FailErr(t, "second title Update", err)
	if again.Path != updated.Path {
		t.Fatalf("locked path moved: %q → %q", updated.Path, again.Path)
	}
	if ParseTitleFrontmatter(again.Content) != next {
		t.Fatalf("frontmatter title = %q", ParseTitleFrontmatter(again.Content))
	}
}

func TestRetargetToTitleNoopForPlaceholderAndLocked(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	bp, err := mgr.Create(ctx, testdbseed.DefaultProjectID, blueprintfile.PlaceholderTitle, "", "plan", "")
	testutil.FailErr(t, "Create", err)
	got, err := mgr.RetargetToTitle(ctx, testdbseed.DefaultProjectID, bp.Path, blueprintfile.PlaceholderTitle)
	testutil.FailErr(t, "placeholder retarget", err)
	if got != bp.Path {
		t.Fatalf("placeholder moved %q → %q", bp.Path, got)
	}

	locked, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Add OAuth", ConventionPath("add-oauth.md"), "plan", "")
	testutil.FailErr(t, "Create locked", err)
	got, err = mgr.RetargetToTitle(ctx, testdbseed.DefaultProjectID, locked.Path, "Something Else")
	testutil.FailErr(t, "locked retarget", err)
	if got != locked.Path {
		t.Fatalf("locked path moved %q → %q", locked.Path, got)
	}
}

func TestMintCollisionUsesNumericSuffix(t *testing.T) {
	projectDir := t.TempDir()
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRoot(t, sqlDB, testdbseed.DefaultProjectID, projectDir)
	mgr := NewManager(NewFileStoreForTest(projectDir))
	ctx := context.Background()

	first, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Add OAuth", "", "plan", "")
	testutil.FailErr(t, "Create first", err)
	second, err := mgr.Create(ctx, testdbseed.DefaultProjectID, "Add OAuth", "", "plan", "")
	testutil.FailErr(t, "Create second", err)
	if first.Path == second.Path {
		t.Fatal("collision must mint a distinct path")
	}
	if PathFileStem(second.Path) != "add-oauth-2" {
		t.Fatalf("collision stem = %q want add-oauth-2", PathFileStem(second.Path))
	}
	if IsProvisionalPath(second.Path) {
		t.Fatalf("declared collision path should be locked: %q", second.Path)
	}
}
