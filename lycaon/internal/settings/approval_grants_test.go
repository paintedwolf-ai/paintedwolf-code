package settings_test

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func durableGrant(category settings.ApprovalCategory, pattern string, scope hitl.ApprovalGrantScope, projectID string) settings.ApprovalGrant {
	witness := hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy}
	return settings.ApprovalGrant{
		GrantedByPersonID: testutil.HostOwner().ID,
		ID:                hitl.ApprovalGrantID(scope, string(category), pattern, "", projectID, witness, nil),
		Scope:             scope, Category: category, Pattern: pattern, ProjectID: projectID,
		Title: "Allow bounded action", Coverage: pattern, GrantedAt: time.Now().UTC(),
		ExpiresWhen: "when revoked", ReaskWhen: "security state changes", Witness: witness,
	}
}

func TestApprovalGrantOwnerOperationIDLoads(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	raw := []byte("grants:\n  - id: grant-device\n    category: host\n    pattern: example.test\n    title: Allow host\n    scope: device\n    owner_operation_id: checkpoint-1\n")
	testutil.FailErr(t, "write approvals", os.WriteFile(global, raw, 0o600))

	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "load approvals", err)
	grants := store.GlobalGrants()
	if len(grants) != 1 || grants[0].OwnerOperationID != "checkpoint-1" {
		t.Fatalf("grants = %+v", grants)
	}
}

func TestUpsertGlobalGrantPreservesGrantedAt(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	grant := durableGrant(settings.ApprovalCategoryHost, "example.test", hitl.ApprovalGrantScopeDevice, "")
	created, err := store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)
	if !created || len(store.GlobalGrants()) != 1 {
		t.Fatalf("grant not created: %+v", store.GlobalGrants())
	}
	first := store.GlobalGrants()[0].GrantedAt
	grant.GrantedAt = first.Add(time.Hour)
	grant.OwnerOperationID = "replacement"
	created, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "replace grant", err)
	if created || !store.GlobalGrants()[0].GrantedAt.Equal(first) {
		t.Fatal("replace changed grant identity or timestamp")
	}
	if store.GlobalGrants()[0].OwnerOperationID != "" {
		t.Fatal("an active grant was overwritten by a later operation")
	}
}

// Expired grants leave the in-memory view on load and the file on its next write.
func TestExpiredGlobalGrantsArePrunedFromTheFile(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	expired := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	raw := []byte("grants:\n  - id: grant-stale\n    category: host\n    pattern: stale.test\n    title: Allow host\n    scope: device\n    expires_at: " + expired + "\n")
	testutil.FailErr(t, "write approvals", os.WriteFile(global, raw, 0o600))

	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "load approvals", err)
	if got := store.GlobalGrants(); len(got) != 0 {
		t.Fatalf("expired grant survived load: %+v", got)
	}
	live := durableGrant(settings.ApprovalCategoryHost, "live.test", hitl.ApprovalGrantScopeDevice, "")
	_, err = store.UpsertGlobalGrant(live)
	testutil.FailErr(t, "insert live grant", err)
	raw, err = os.ReadFile(global)
	testutil.FailErr(t, "read approvals after write", err)
	if strings.Contains(string(raw), "stale.test") || !strings.Contains(string(raw), "live.test") {
		t.Fatalf("write did not prune the expired grant:\n%s", raw)
	}
}

func TestExpiredGlobalGrantCanBeReissuedWithNewOwner(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	grant := durableGrant(settings.ApprovalCategoryHost, "example.test", hitl.ApprovalGrantScopeDevice, "")
	expired := time.Now().UTC().Add(-time.Minute)
	grant.ExpiresAt = &expired
	grant.OwnerOperationID = "old-operation"
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "insert expired grant", err)

	grant.ExpiresAt = nil
	grant.OwnerOperationID = "new-operation"
	grant.GrantedAt = time.Now().UTC()
	created, err := store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "reissue grant", err)
	if !created {
		t.Fatal("expired grant must be replaced as newly installed authority")
	}
	got := store.GlobalGrants()
	if len(got) != 1 || got[0].OwnerOperationID != "new-operation" {
		t.Fatalf("reissued grant = %+v", got)
	}
}

func TestRevokeGlobalGrantInstalledByIsConditional(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	grant := durableGrant(settings.ApprovalCategoryHost, "example.test", hitl.ApprovalGrantScopeDevice, "")
	grant.OwnerOperationID = "checkpoint-new"
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)

	found, err := store.RevokeGlobalGrantInstalledBy(grant.ID, "checkpoint-old")
	testutil.FailErr(t, "revoke foreign installer", err)
	if found || len(store.GlobalGrants()) != 1 {
		t.Fatal("foreign recovery operation revoked active authority")
	}
	found, err = store.RevokeGlobalGrantInstalledBy(grant.ID, "checkpoint-new")
	testutil.FailErr(t, "revoke installer", err)
	if !found || len(store.GlobalGrants()) != 0 {
		t.Fatal("installing recovery operation did not revoke its authority")
	}
}

func TestApprovalGrantRevoke(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	grant := durableGrant(settings.ApprovalCategoryMCP, "github.create_issue", hitl.ApprovalGrantScopeDevice, "")
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)
	found, err := store.RevokeGlobalGrant(grant.ID)
	testutil.FailErr(t, "RevokeGlobalGrant", err)
	if !found {
		t.Fatal("expected revoke to find the grant")
	}
	if len(store.GlobalGrants()) != 0 {
		t.Fatalf("grant survived revoke: %+v", store.GlobalGrants())
	}
	found, err = store.RevokeGlobalGrant(grant.ID)
	testutil.FailErr(t, "repeat revoke", err)
	if found {
		t.Fatal("repeat revoke must report the grant missing")
	}
}

func TestProjectWriteRootOnlyAppliesToOriginatingProject(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	root := filepath.Join(t.TempDir(), "writes")
	grant := durableGrant(settings.ApprovalCategoryWriteRoot, root, hitl.ApprovalGrantScopeProject, "proj-1")
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)
	if got := store.WriteRootsForProject("proj-1"); len(got) != 1 || got[0] != root {
		t.Fatalf("project roots = %v", got)
	}
	if got := store.WriteRootsForProject("proj-2"); len(got) != 0 {
		t.Fatalf("grant escaped project: %v", got)
	}
}

func TestDeviceWriteRootAppliesAcrossProjects(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	root := filepath.Join(t.TempDir(), "writes")
	grant := durableGrant(settings.ApprovalCategoryWriteRoot, root, hitl.ApprovalGrantScopeDevice, "proj-1")
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)
	for _, id := range []string{"proj-1", "proj-2"} {
		got := store.WriteRootsForProject(id)
		if len(got) != 1 || got[0] != root {
			t.Fatalf("device write-root for %s = %v, want [%s]", id, got, root)
		}
	}
}

func TestSocketPathDeviceGrantIsCrossProject(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)

	dir, err := os.MkdirTemp("", "skd") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = ln.Close() })
	resolved, err := filepath.EvalSymlinks(sockPath)
	testutil.FailErr(t, "eval", err)

	witness := hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy}
	grant := settings.ApprovalGrant{
		GrantedByPersonID: testutil.HostOwner().ID,
		ID: hitl.ApprovalGrantID(hitl.ApprovalGrantScopeDevice, string(settings.ApprovalCategorySocketPath),
			sockPath+"\x00"+resolved, "", "", witness, nil),
		Scope: hitl.ApprovalGrantScopeDevice, Category: settings.ApprovalCategorySocketPath,
		Pattern: sockPath, Title: "Allow local service", Coverage: sockPath,
		GrantedAt: time.Now().UTC(), ExpiresWhen: "when revoked", ReaskWhen: "target changes",
		Witness: witness, ApprovedPath: sockPath, ResolvedPath: resolved, Source: "settings",
	}
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)

	// Device grants apply across projects.
	for _, id := range []string{"proj-a", "proj-b"} {
		got := store.SocketPathsForProject(id)
		if len(got) != 1 || got[0].ApprovedPath != sockPath || got[0].ResolvedPath != resolved {
			t.Fatalf("device socket for %s = %+v", id, got)
		}
	}
}

func TestSocketPathProjectGrantStaysInProject(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)

	dir, err := os.MkdirTemp("", "skp") //nolint:usetesting // Short socket path.
	testutil.FailErr(t, "MkdirTemp", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sockPath)
	testutil.FailErr(t, "listen", err)
	t.Cleanup(func() { _ = ln.Close() })
	resolved, err := filepath.EvalSymlinks(sockPath)
	testutil.FailErr(t, "eval", err)

	witness := hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy}
	grant := settings.ApprovalGrant{
		GrantedByPersonID: testutil.HostOwner().ID,
		ID: hitl.ApprovalGrantID(hitl.ApprovalGrantScopeProject, string(settings.ApprovalCategorySocketPath),
			sockPath+"\x00"+resolved, "", "proj-a", witness, nil),
		Scope: hitl.ApprovalGrantScopeProject, Category: settings.ApprovalCategorySocketPath,
		Pattern: sockPath, ProjectID: "proj-a", Title: "Allow local service", Coverage: sockPath,
		GrantedAt: time.Now().UTC(), ExpiresWhen: "when revoked", ReaskWhen: "target changes",
		Witness: witness, ApprovedPath: sockPath, ResolvedPath: resolved, Source: "settings",
	}
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)

	if got := store.SocketPathsForProject("proj-a"); len(got) != 1 {
		t.Fatalf("project A sockets = %+v", got)
	}
	if got := store.SocketPathsForProject("proj-b"); len(got) != 0 {
		t.Fatalf("project B must not inherit project socket: %+v", got)
	}
}

func TestPolicyRulesRejectUnknownEffects(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	unknown := settings.ApprovalRule{Category: settings.ApprovalCategoryTool, Pattern: "read", Effect: settings.ApprovalEffect("permit")}
	if err := store.PutGlobal(settings.ApprovalConfig{Rules: []settings.ApprovalRule{unknown}}); err == nil {
		t.Fatal("expected unknown policy effect rejection")
	}
}

func TestPackageCoordinateDurableGrant(t *testing.T) {
	global, _, _ := testSettingsPaths(t)
	store, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "NewApprovalStoreAt", err)

	expires := time.Now().UTC().Add(7 * 24 * time.Hour)
	witness := hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy}
	pattern := "npm:remote_execute:NPM:prisma:8.0.0-rc.15"
	grant := settings.ApprovalGrant{
		GrantedByPersonID: testutil.HostOwner().ID,
		ID:                "grant-pkg-coord-test",
		Scope:             hitl.ApprovalGrantScopeProject,
		Category:          settings.ApprovalCategoryPackageCoordinate,
		Pattern:           pattern,
		ProjectID:         "proj-prisma",
		Title:             "Allow for this project for 7 days",
		Coverage:          "execution of package prisma@8.0.0-rc.15 for this project",
		GrantedAt:         time.Now().UTC(),
		ExpiresAt:         &expires,
		ExpiresWhen:       "in 7 days or when revoked",
		ReaskWhen:         "a different package version is requested or revoked",
		Witness:           witness,
	}

	created, err := store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant package_coordinate", err)
	if !created {
		t.Fatal("expected package_coordinate grant to be created")
	}

	grants := store.GlobalGrants()
	if len(grants) != 1 || grants[0].Category != settings.ApprovalCategoryPackageCoordinate || grants[0].Pattern != pattern {
		t.Fatalf("unexpected global grants: %+v", grants)
	}

	reloaded, err := settings.NewApprovalStoreAt(global)
	testutil.FailErr(t, "reload approval store", err)
	reloadedGrants := reloaded.GlobalGrants()
	if len(reloadedGrants) != 1 || reloadedGrants[0].ID != grant.ID {
		t.Fatalf("reloaded grants mismatch: %+v", reloadedGrants)
	}

	noExpiry := grant
	noExpiry.ID = "grant-no-expiry"
	noExpiry.ExpiresAt = nil
	if _, err := store.UpsertGlobalGrant(noExpiry); err == nil {
		t.Fatal("expected error for package_coordinate without expires_at")
	}
}
