package capabilityadmin

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/grantedpath"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

func grantedPathOption(t *testing.T, path string, write bool) hitl.ApprovalOption {
	t.Helper()
	return grantedPathOptionAccess(t, hitl.GrantedPathDelta{Path: path, Write: write})
}

func grantedPathOptionAccess(t *testing.T, access hitl.GrantedPathDelta) hitl.ApprovalOption {
	t.Helper()
	expires := time.Now().UTC().Add(time.Hour)
	grant := hitl.ApprovalGrant{
		ID: "grant_fs", Scope: hitl.ApprovalGrantScopeChat,
		Predicate: hitl.ApprovalGrantPredicate{Category: "path", Pattern: access.Path},
		ExpiresAt: &expires, GrantedPath: &access,
	}
	return hitl.ApprovalOption{
		ID: grant.ID, Kind: hitl.ApprovalOptionLease, Scope: grant.Scope,
		DecisionAction: hitl.ApprovalOptionApprove,
		Authority: []hitl.ApprovalAuthorityDelta{{
			Kind: hitl.AuthorityGrantedPath, Grant: &grant, ChatSessionID: "chat-1",
			GrantedPath: &access,
		}},
	}
}

func TestApprovalRecoveryRevokesOnlyItsOwnedGrant(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	s := &Handler{Deps: Deps{Gate: gate}}
	grant := hitl.ApprovalGrant{
		ID: "grant_device", Scope: hitl.ApprovalGrantScopeDevice,
		Predicate: hitl.ApprovalGrantPredicate{Category: string(settings.ApprovalCategoryHost), Pattern: "example.test"},
		Title:     "Allow host", Coverage: "example.test", GrantedAt: time.Now().UTC(),
		ExpiresWhen: "when revoked", ReaskWhen: "host changes",
		GrantedByPersonID: "person-1",
	}
	option := hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{{Kind: hitl.AuthorityGenericGrant, Grant: &grant}}}

	_, err = s.InstallApprovalOption(t.Context(), "checkpoint-new", option)
	testutil.FailErr(t, "InstallApprovalOption", err)
	testutil.FailErr(t, "foreign rollback", s.RollbackApprovalOption(t.Context(), "checkpoint-old", option))
	if len(gate.ListGrants("")) != 1 {
		t.Fatal("foreign recovery operation revoked active grant")
	}
	testutil.FailErr(t, "installing rollback", s.RollbackApprovalOption(t.Context(), "checkpoint-new", option))
	if len(gate.ListGrants("")) != 0 {
		t.Fatal("installing recovery operation left its grant installed")
	}
}

func TestApprovalRecoveryRevokesOnlyItsInstalledAskQuiet(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	s := &Handler{Deps: Deps{Gate: gate}}
	const chatID = "chat-1"
	const key = "authority_misuse:pack/rule"
	quietID := hitl.QuietRecordID(chatID, key, 0)
	option := hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{{
		Kind: hitl.AuthorityAskQuiet, ChatSessionID: chatID,
		AskQuiet: &hitl.AskQuietDelta{ID: quietID, Key: key, Label: "pack / rule"},
	}}}

	_, err = s.InstallApprovalOption(t.Context(), "checkpoint-new", option)
	testutil.FailErr(t, "InstallApprovalOption", err)
	testutil.FailErr(t, "foreign rollback", s.RollbackApprovalOption(t.Context(), "checkpoint-old", option))
	if _, ok := gate.AskQuietLive(chatID, key); !ok {
		t.Fatal("foreign recovery operation revoked active ask quiet")
	}
	testutil.FailErr(t, "installing rollback", s.RollbackApprovalOption(t.Context(), "checkpoint-new", option))
	if _, ok := gate.AskQuietLive(chatID, key); ok {
		t.Fatal("installing recovery operation left its ask quiet installed")
	}
}

func TestTaskApprovalDoesNotReplaceLiveWriteAuthority(t *testing.T) {
	runtime := approvalstate.NewSandboxPathGrantRuntime()
	s := &Handler{Deps: Deps{Authority: Authority{WriteRoots: runtime}}}
	root := t.TempDir()
	option := func(id string) hitl.ApprovalOption {
		grant := hitl.ApprovalGrant{ID: id}
		return hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{{
			Kind: hitl.AuthorityWriteRootChat, Grant: &grant,
			ChatSessionID: "chat-1", WriteRoots: []string{root},
		}}}
	}

	_, err := s.InstallApprovalOption(t.Context(), "checkpoint-1", option("grant_a"))
	testutil.FailErr(t, "install first approval", err)
	rollback, err := s.InstallApprovalOption(t.Context(), "checkpoint-2", option("grant_b"))
	testutil.FailErr(t, "install duplicate approval", err)
	rollback()

	grants := runtime.ListChatGrants("chat-1")
	if len(grants) != 1 || grants[0].ID != "grant_a" || grants[0].SourceCheckpointID != "checkpoint-1" {
		t.Fatalf("live authority changed installer: %+v", grants)
	}
}

func TestTaskApprovalRenewsExpiredListenAuthority(t *testing.T) {
	runtime := approvalstate.NewSandboxPortGrantRuntime()
	past := time.Now().UTC().Add(-time.Minute)
	runtime.GrantChat("chat-1", []uint16{8000}, "grant_a", "checkpoint-old", &past)
	s := &Handler{Deps: Deps{Authority: Authority{Listen: runtime}}}
	grant := hitl.ApprovalGrant{ID: "grant_a"}
	option := hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{{
		Kind: hitl.AuthorityLocalListenChat, Grant: &grant,
		ChatSessionID: "chat-1", ListenPorts: []uint16{8000}, TTLSeconds: 60,
	}}}

	_, err := s.InstallApprovalOption(t.Context(), "checkpoint-new", option)
	testutil.FailErr(t, "renew listen approval", err)
	grants := runtime.ListChatGrants("chat-1")
	if len(grants) != 1 || grants[0].SourceCheckpointID != "checkpoint-new" ||
		grants[0].ExpiresAt == nil || !grants[0].ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("renewed authority = %+v", grants)
	}
}

func TestInstallGrantedPathWidensAccess(t *testing.T) {
	rt := grantedpath.NewRuntime()
	s := &Handler{Deps: Deps{Authority: Authority{GrantedPaths: rt}}}

	rollback, err := s.InstallApprovalOption(context.Background(), "cp-1", grantedPathOption(t, "/etc/hosts", true))
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, ok := rt.Covers("chat-1", "", "/etc/hosts", grantedpath.ModeWrite); !ok {
		t.Fatal("approving must grant the path it named")
	}
	rollback()
	if _, ok := rt.Covers("chat-1", "", "/etc/hosts", grantedpath.ModeWrite); ok {
		t.Fatal("rollback must remove the granted path")
	}
}

func TestInstallGrantedPathKeepsDirection(t *testing.T) {
	rt := grantedpath.NewRuntime()
	s := &Handler{Deps: Deps{Authority: Authority{GrantedPaths: rt}}}

	if _, err := s.InstallApprovalOption(context.Background(), "cp-1", grantedPathOption(t, "/mnt/report.pdf", false)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if _, ok := rt.Covers("chat-1", "", "/mnt/report.pdf", grantedpath.ModeRead); !ok {
		t.Fatal("a read answer must grant the read")
	}
	if _, ok := rt.Covers("chat-1", "", "/mnt/report.pdf", grantedpath.ModeWrite); ok {
		t.Fatal("a read answer must not grant the write")
	}
}

func TestGrantedPathRollbackPreservesIndependentApproval(t *testing.T) {
	rt := grantedpath.NewRuntime()
	s := &Handler{Deps: Deps{Authority: Authority{GrantedPaths: rt}}}
	first := grantedPathOption(t, "/etc/hosts", true)
	second := grantedPathOption(t, "/etc/hosts", true)
	second.Authority[0].Grant.ID = "grant_fs_second"

	_, err := s.InstallApprovalOption(t.Context(), "cp-1", first)
	testutil.FailErr(t, "install first path approval", err)
	rollback, err := s.InstallApprovalOption(t.Context(), "cp-2", second)
	testutil.FailErr(t, "install second path approval", err)
	if grants := rt.List("chat-1"); len(grants) != 2 {
		t.Fatalf("installed path grants = %d, want 2", len(grants))
	}
	rollback()
	if _, ok := rt.Covers("chat-1", "", "/etc/hosts", grantedpath.ModeWrite); !ok {
		t.Fatal("rollback removed independent path authority")
	}
}

func TestGrantedPathIDCollisionFailsInstallation(t *testing.T) {
	rt := grantedpath.NewRuntime()
	s := &Handler{Deps: Deps{Authority: Authority{GrantedPaths: rt}}}

	_, err := s.InstallApprovalOption(t.Context(), "cp-1", grantedPathOption(t, "/etc/hosts", true))
	testutil.FailErr(t, "install first path approval", err)
	if _, err := s.InstallApprovalOption(t.Context(), "cp-2", grantedPathOption(t, "/etc/passwd", true)); err == nil {
		t.Fatal("reused grant id changed path authority")
	}
	if _, ok := rt.Covers("chat-1", "", "/etc/hosts", grantedpath.ModeWrite); !ok {
		t.Fatal("collision changed existing path authority")
	}
}

func TestInstallGrantedPathWithoutRuntimeFails(t *testing.T) {
	s := &Handler{}
	if _, err := s.InstallApprovalOption(context.Background(), "cp-1", grantedPathOption(t, "/etc/hosts", true)); err == nil {
		t.Fatal("installing granted-path authority with no runtime must fail")
	}
}

func TestInstallGrantedPathTreeCoversDescendants(t *testing.T) {
	rt := grantedpath.NewRuntime()
	s := &Handler{Deps: Deps{Authority: Authority{GrantedPaths: rt}}}

	folder := "/srv/sdk"
	_, err := s.InstallApprovalOption(context.Background(), "cp-1", grantedPathOptionAccess(t, hitl.GrantedPathDelta{
		Path: folder, Tree: true,
	}))
	testutil.FailErr(t, "install tree", err)
	for _, path := range []string{folder, folder + "/release.go", folder + "/pkg/client.go"} {
		if _, ok := rt.Covers("chat-1", "", path, grantedpath.ModeRead); !ok {
			t.Fatalf("%s should be under the installed tree", path)
		}
	}
	if _, ok := rt.Covers("chat-1", "", "/srv/other/release.go", grantedpath.ModeRead); ok {
		t.Fatal("a sibling folder must not be covered")
	}
	if _, ok := rt.Covers("chat-1", "", folder+"/release.go", grantedpath.ModeWrite); ok {
		t.Fatal("a read tree must not grant writes")
	}
}
