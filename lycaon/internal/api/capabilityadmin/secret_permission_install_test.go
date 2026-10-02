package capabilityadmin

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
)

type secretInstallRecorder struct {
	hitl.ApprovalGate
	installed bool
}

func (r *secretInstallRecorder) ApplyGrant(grant hitl.ApprovalGrant) (bool, error) {
	created, err := r.ApprovalGate.ApplyGrant(grant)
	if err == nil && created && grant.Predicate.Category == hitl.ApprovalGrantCategorySecret {
		r.installed = true
	}
	return created, err
}

func TestServicePermissionFailureRollsBackSecretAuthority(t *testing.T) {
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "open store", err)
	authority := settings.NewRuleApprovalGate(store, settings.NoSources())
	recipients := []secretmatch.Recipient{{ID: "service", Label: "http://localhost:8080", Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}}
	secret := hitl.ApprovalGrant{ID: "secret", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "task", ProjectID: "project",
		Predicate:          hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"value"})},
		SecretFingerprints: []string{"value"}, SecretRecipients: recipients, Witness: hitl.SecretReleaseWitness(recipients)}
	connection := hitl.ApprovalGrant{ID: "connection", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "task"}
	option := hitl.ApprovalOption{Authority: []hitl.ApprovalAuthorityDelta{
		{Kind: hitl.AuthorityGenericGrant, Grant: &secret},
		{Kind: hitl.AuthorityLoopbackConnectChat, Grant: &connection, ChatSessionID: "task", ConnectPorts: []uint16{8080}},
	}}
	recorder := &secretInstallRecorder{ApprovalGate: authority}
	server := &Handler{Deps: Deps{Gate: recorder}}
	if _, err := server.InstallApprovalOption(t.Context(), "checkpoint", option); err == nil {
		t.Fatal("missing connection runtime unexpectedly installed authority")
	}
	if !recorder.installed {
		t.Fatal("test did not reach the partial-install rollback path")
	}
	if authority.SecretFingerprintsCovered("task", "project", "service", "http_request", []string{"value"}) {
		t.Fatal("partial permission survived failed installation")
	}
}
