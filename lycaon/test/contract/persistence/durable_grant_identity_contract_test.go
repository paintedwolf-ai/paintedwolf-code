package contract

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func grantWitness() hitl.ApprovalGrantWitness {
	return hitl.ApprovalGrantWitness{FSJailed: true, Egress: hitl.ContainedEgressProxy}
}

func identityGrant(t *testing.T, projectID, projectDir, root string) settings.ApprovalGrant {
	t.Helper()
	witness := grantWitness()
	return settings.ApprovalGrant{
		GrantedByPersonID: testutil.HostOwner().ID,
		ID: hitl.ApprovalGrantID(hitl.ApprovalGrantScopeProject,
			string(settings.ApprovalCategoryWriteRoot), root, "", projectID, witness, nil),
		Scope:       hitl.ApprovalGrantScopeProject,
		Category:    settings.ApprovalCategoryWriteRoot,
		Pattern:     root,
		ProjectID:   projectID,
		ProjectDir:  projectDir,
		Title:       "Allow writes here",
		Coverage:    root,
		GrantedAt:   time.Now().UTC(),
		ExpiresWhen: "when revoked",
		ReaskWhen:   "security state changes",
		Witness:     witness,
	}
}

func TestDurableGrantIDSeparatesProjects(t *testing.T) {
	t.Parallel()
	witness := grantWitness()
	first := hitl.ApprovalGrantID(hitl.ApprovalGrantScopeProject, "write_root", "/tmp/cache", "", "proj-1", witness, nil)
	second := hitl.ApprovalGrantID(hitl.ApprovalGrantScopeProject, "write_root", "/tmp/cache", "", "proj-1", witness, nil)
	if first != second {
		t.Fatal("grant identity is not stable for one project")
	}
	other := hitl.ApprovalGrantID(hitl.ApprovalGrantScopeProject, "write_root", "/tmp/cache", "", "proj-2", witness, nil)
	if first == other {
		t.Fatal("two different projects share one durable grant id")
	}
}

func TestDurableGrantSurvivesFolderChanges(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	root := filepath.Join(tmp, "cache")

	for _, folder := range []string{
		filepath.Join(tmp, "original"), // where it was granted
		filepath.Join(tmp, "moved"),    // the root relocated
		"",                             // every folder detached
	} {
		_, err = store.UpsertGlobalGrant(identityGrant(t, "proj-1", folder, root))
		testutil.FailErr(t, "UpsertGlobalGrant", err)
		if rows := store.GlobalGrants(); len(rows) != 1 {
			t.Fatalf("folder %q minted a second row: %+v", folder, rows)
		}
		got := store.WriteRootsForProject("proj-1")
		if len(got) != 1 || got[0] != root {
			t.Fatalf("grant stopped matching with folder %q: %v", folder, got)
		}
	}
	if got := store.WriteRootsForProject("proj-2"); len(got) != 0 {
		t.Fatalf("project grant reached a different project: %v", got)
	}
}

func TestDeviceWriteRootIdentityIsCrossProject(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	root := filepath.Join(tmp, "cache")
	empty := hitl.ApprovalGrantID(hitl.ApprovalGrantScopeDevice,
		string(settings.ApprovalCategoryWriteRoot), root, "", "", hitl.ApprovalGrantWitness{}, nil)
	again := hitl.ApprovalGrantID(hitl.ApprovalGrantScopeDevice,
		string(settings.ApprovalCategoryWriteRoot), root, "", "", hitl.ApprovalGrantWitness{}, nil)
	if empty != again {
		t.Fatal("device write-root identity is not stable")
	}

	grant := settings.ApprovalGrant{
		GrantedByPersonID: testutil.HostOwner().ID,
		ID:                empty, Scope: hitl.ApprovalGrantScopeDevice, Category: settings.ApprovalCategoryWriteRoot,
		Pattern: root, Title: "Allow writes here", Coverage: root,
		GrantedAt: time.Now().UTC(), ExpiresWhen: "when revoked", ReaskWhen: "a different write root is named",
	}
	_, err = store.UpsertGlobalGrant(grant)
	testutil.FailErr(t, "UpsertGlobalGrant", err)
	for _, id := range []string{"proj-1", "proj-2"} {
		got := store.WriteRootsForProject(id)
		if len(got) != 1 || got[0] != root {
			t.Fatalf("device write-root for %s = %v", id, got)
		}
	}
}

func TestRegrantFromAnotherFolderReplacesTheSameRow(t *testing.T) {
	t.Parallel()
	tmp := t.TempDir()
	store, err := settings.NewApprovalStoreAt(filepath.Join(tmp, "global.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	root := filepath.Join(tmp, "cache")

	created, err := store.UpsertGlobalGrant(identityGrant(t, "proj-1", filepath.Join(tmp, "before"), root))
	testutil.FailErr(t, "first grant", err)
	if !created {
		t.Fatal("first grant was not created")
	}
	created, err = store.UpsertGlobalGrant(identityGrant(t, "proj-1", filepath.Join(tmp, "after"), root))
	testutil.FailErr(t, "regrant", err)
	if created {
		t.Fatal("a folder change minted a second row for the same authority")
	}
	if got := store.GlobalGrants(); len(got) != 1 {
		t.Fatalf("saved approvals hold %d rows for one authority: %+v", len(got), got)
	}
}
