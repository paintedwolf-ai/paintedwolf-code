package grantedpath_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/grantedpath"
)

func fileGrant(id, path string, mode grantedpath.Mode) grantedpath.Grant {
	expires := time.Now().Add(time.Hour)
	return grantedpath.Grant{ID: id, Path: path, Mode: mode, ExpiresAt: &expires}
}

func mustGrant(t *testing.T, rt *grantedpath.Runtime, root string, grant grantedpath.Grant) {
	t.Helper()
	created, err := rt.Grant(root, grant)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !created {
		t.Fatal("grant was not created")
	}
}

func TestGrantCoversOnlyItsExactPath(t *testing.T) {
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat", fileGrant("g1", "/srv/notes.md", grantedpath.ModeRead))

	if _, ok := rt.Covers("chat", "", "/srv/notes.md", grantedpath.ModeRead); !ok {
		t.Fatal("exact path is not covered")
	}
	for _, path := range []string{"/srv/notes.md.bak", "/srv/other.md", "/srv/notes.md/child"} {
		if _, ok := rt.Covers("chat", "", path, grantedpath.ModeRead); ok {
			t.Errorf("%s must not be covered", path)
		}
	}
}

func TestTreeGrantCoversDescendantsNotSiblings(t *testing.T) {
	rt := grantedpath.NewRuntime()
	expires := time.Now().Add(time.Hour)
	mustGrant(t, rt, "chat", grantedpath.Grant{
		ID: "g1", Path: "/srv/sdk", Mode: grantedpath.ModeRead, Tree: true, ExpiresAt: &expires,
	})
	for _, path := range []string{"/srv/sdk", "/srv/sdk/release.go", "/srv/sdk/pkg/client.go"} {
		if _, ok := rt.Covers("chat", "", path, grantedpath.ModeRead); !ok {
			t.Errorf("%s should be under the tree grant", path)
		}
	}
	for _, path := range []string{"/srv/sdk2", "/srv/other/release.go", "/srv"} {
		if _, ok := rt.Covers("chat", "", path, grantedpath.ModeRead); ok {
			t.Errorf("%s must not be covered by /srv/sdk", path)
		}
	}
}

func TestReadGrantDoesNotPermitWrite(t *testing.T) {
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat", fileGrant("g1", "/mnt/data/report", grantedpath.ModeRead))

	if _, ok := rt.Covers("chat", "", "/mnt/data/report", grantedpath.ModeWrite); ok {
		t.Fatal("a read grant must not permit a write")
	}
	mustGrant(t, rt, "chat", fileGrant("g2", "/mnt/out/report", grantedpath.ModeWrite))
	if _, ok := rt.Covers("chat", "", "/mnt/out/report", grantedpath.ModeRead); !ok {
		t.Fatal("a write grant should cover reading the same path")
	}
}

func TestExpiredGrantStopsCovering(t *testing.T) {
	rt := grantedpath.NewRuntime()
	past := time.Now().Add(-time.Minute)
	mustGrant(t, rt, "chat", grantedpath.Grant{
		ID: "g1", Path: "/mnt/data/report", Mode: grantedpath.ModeRead, ExpiresAt: &past,
	})
	if _, ok := rt.Covers("chat", "", "/mnt/data/report", grantedpath.ModeRead); ok {
		t.Fatal("an expired grant must not cover")
	}
	if got := rt.List("chat"); len(got) != 0 {
		t.Fatalf("expired grants must not be listed, got %d", len(got))
	}
}

func TestGrantsAreScopedToTheSessionTree(t *testing.T) {
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat-a", fileGrant("g1", "/mnt/data/report", grantedpath.ModeRead))
	if _, ok := rt.Covers("chat-b", "", "/mnt/data/report", grantedpath.ModeRead); ok {
		t.Fatal("a grant must not leak across session trees")
	}
}

func TestGrantIDCannotChangeAuthority(t *testing.T) {
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat-a", fileGrant("g1", "/mnt/data/report", grantedpath.ModeRead))

	for _, grant := range []struct {
		root string
		path string
		mode grantedpath.Mode
	}{
		{root: "chat-a", path: "/mnt/data/other", mode: grantedpath.ModeRead},
		{root: "chat-a", path: "/mnt/data/report", mode: grantedpath.ModeWrite},
		{root: "chat-b", path: "/mnt/data/report", mode: grantedpath.ModeRead},
	} {
		if _, err := rt.Grant(grant.root, fileGrant("g1", grant.path, grant.mode)); err == nil {
			t.Errorf("grant id changed authority to root=%s path=%s mode=%s", grant.root, grant.path, grant.mode)
		}
	}
	tree := fileGrant("g1", "/mnt/data/report", grantedpath.ModeRead)
	tree.Tree = true
	if _, err := rt.Grant("chat-a", tree); err == nil {
		t.Error("grant id changed authority from exact to tree")
	}
}

func TestExpiredGrantRenewsWithNewInstaller(t *testing.T) {
	rt := grantedpath.NewRuntime()
	past := time.Now().Add(-time.Minute)
	mustGrant(t, rt, "chat", grantedpath.Grant{
		ID: "g1", SourceCheckpointID: "checkpoint-old", Path: "/mnt/data/report",
		Mode: grantedpath.ModeRead, ExpiresAt: &past,
	})
	future := time.Now().Add(time.Hour)
	created, err := rt.Grant("chat", grantedpath.Grant{
		ID: "g1", SourceCheckpointID: "checkpoint-new", Path: "/mnt/data/report",
		Mode: grantedpath.ModeRead, ExpiresAt: &future,
	})
	if err != nil || !created {
		t.Fatalf("renew grant: created=%v err=%v", created, err)
	}
	if rt.RevokeInstalledBy("chat", "g1", "checkpoint-old") {
		t.Fatal("expired installer revoked renewed authority")
	}
	if !rt.RevokeInstalledBy("chat", "g1", "checkpoint-new") {
		t.Fatal("renewed installer could not revoke its authority")
	}
}

func TestRevokeByIDEndsAccessWithoutOwningSession(t *testing.T) {
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat-a", fileGrant("shared-id", "/mnt/a/report", grantedpath.ModeRead))
	if !rt.RevokeByID("shared-id") {
		t.Fatal("revoke by id reported nothing to remove")
	}
	if _, ok := rt.Covers("chat-a", "", "/mnt/a/report", grantedpath.ModeRead); ok {
		t.Fatal("shared revoke path left live runtime authority")
	}
}

func TestUncleanedPathsStillMatch(t *testing.T) {
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat", fileGrant("g1", "/mnt/data/./report", grantedpath.ModeRead))
	if _, ok := rt.Covers("chat", "", "/mnt/data/report", grantedpath.ModeRead); !ok {
		t.Fatal("an uncleaned path should still match its grant")
	}
}

func TestForgetDropsSessionGrants(t *testing.T) {
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat", fileGrant("g1", "/mnt/data/report", grantedpath.ModeRead))
	rt.Forget("chat")
	if _, ok := rt.Covers("chat", "", "/mnt/data/report", grantedpath.ModeRead); ok {
		t.Fatal("forgetting a session must drop its grants")
	}
}

func TestDurableGrantsAreConsultedPerProject(t *testing.T) {
	rt := grantedpath.NewRuntime()
	expires := time.Now().Add(time.Hour)
	rt.SetDurableSource(func(projectID string) []grantedpath.Grant {
		if projectID != "project-site" {
			return nil
		}
		return []grantedpath.Grant{{
			ID: "saved", Path: "/mnt/assets/logo.png",
			Mode: grantedpath.ModeRead, ExpiresAt: &expires,
		}}
	})
	if _, ok := rt.Covers("chat", "project-site", "/mnt/assets/logo.png", grantedpath.ModeRead); !ok {
		t.Fatal("a durable grant should cover its project")
	}
	if _, ok := rt.Covers("chat", "project-other", "/mnt/assets/logo.png", grantedpath.ModeRead); ok {
		t.Fatal("a durable grant must not reach a project it was not reviewed under")
	}
}

func TestSymlinkUnderTreeGrantCannotReachOutside(t *testing.T) {
	granted := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	link := filepath.Join(granted, "link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rt := grantedpath.NewRuntime()
	expires := time.Now().Add(time.Hour)
	mustGrant(t, rt, "chat", grantedpath.Grant{
		ID: "g1", Path: granted, Mode: grantedpath.ModeRead, Tree: true, ExpiresAt: &expires,
	})
	if _, ok := rt.Covers("chat", "", filepath.Join(granted, "inside.txt"), grantedpath.ModeRead); !ok {
		t.Fatal("a plain descendant should be covered")
	}
	if _, ok := rt.Covers("chat", "", link, grantedpath.ModeRead); ok {
		t.Fatal("a symlink pointing outside the tree must not be covered")
	}
}

func TestSymlinkAliasOfCoveredPathMatches(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(real, []byte("r"), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	alias := filepath.Join(dir, "alias.txt")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rt := grantedpath.NewRuntime()
	mustGrant(t, rt, "chat", fileGrant("g1", real, grantedpath.ModeRead))
	if _, ok := rt.Covers("chat", "", alias, grantedpath.ModeRead); !ok {
		t.Fatal("an alias of the granted file resolves to the same location and should match")
	}
}
