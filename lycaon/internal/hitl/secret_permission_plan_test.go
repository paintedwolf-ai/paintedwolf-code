package hitl_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCompoundPermissionCarriesEveryApprovedPart(t *testing.T) {
	action := hitl.ProposedAction{
Invocation: hitl.ActionInvocation{
Tool: "command",
},
Scope: hitl.ActionScope{
SessionID: "chat-1",
RootSessionID: "chat-1",
ProjectID: "proj",
},
}
	recipient := secretmatch.Recipient{ID: "origin", Label: "http://localhost:8080", Surface: secretmatch.SurfaceHTTPRequest, Kind: secretmatch.DestinationService}
	grant := hitl.ApprovalGrant{ID: "secret-task", Scope: hitl.ApprovalGrantScopeChat, ChatSessionID: "chat-1", ProjectID: "proj",
		Predicate:        hitl.ApprovalGrantPredicate{Category: hitl.ApprovalGrantCategorySecret, Pattern: secretmatch.FingerprintDigest([]secretmatch.SecretFingerprint{"value"})},
		SecretRecipients: []secretmatch.Recipient{recipient}, SecretFingerprints: []string{"value"}, Witness: hitl.SecretReleaseWitness([]secretmatch.Recipient{recipient})}
	permission := &hitl.SecretPermission{ConnectPorts: []uint16{8080}, Fingerprints: []secretmatch.SecretFingerprint{"value"},
		Screen: hitl.SecretScreen{Managed: true, SecretNames: []string{"Service password"}, Recipients: []secretmatch.Recipient{recipient}, DestinationID: recipient.ID, DestinationLabel: recipient.Label, DestinationKind: recipient.Kind, OriginKind: secretmatch.OriginField, SourcePath: "arguments"},
		Offers: []hitl.ApprovalGrantOffer{{ID: grant.ID, Rung: hitl.ApprovalRungChat, Scope: grant.Scope, Grant: grant, Coverage: "Service password with the service"}}}
	plan := facePolicyPlan(t, hitl.FaceContext{}, facePolicyOptions(false))
	combined, err := hitl.ComposeSecretPermission(plan, action, permission, hitl.FaceContext{})
	testutil.FailErr(t, "compose secret permission", err)
	if combined.RecommendedOptionID != "chat" || combined.Presentation.Location == nil || len(combined.Presentation.Location.Recipients) != 1 {
		t.Fatal("compound review lost primary duration or recipients")
	}
	chatOpt, _ := combined.Option("chat")
	if len(chatOpt.Authority) != 3 {
		t.Fatalf("chat authority has %d parts, want capability, secret, and local connection", len(chatOpt.Authority))
	}
	if !strings.Contains(chatOpt.Coverage, "Service password") {
		t.Fatal("disclosure missing from option coverage")
	}
	withoutDisclosure := chatOpt
	withoutDisclosure.Authority = []hitl.ApprovalAuthorityDelta{chatOpt.Authority[0], chatOpt.Authority[2]}
	if combined.OptionContinues(withoutDisclosure) {
		t.Fatal("capability authority alone covered the secret disclosure")
	}
	once, _ := combined.Option("approve_current_action")
	for _, delta := range once.Authority {
		if delta.Grant != nil {
			t.Fatal("once minted reusable secret authority")
		}
	}
	project, _ := combined.Option("project")
	if !project.Disabled {
		t.Fatal("unsupported duration remained enabled")
	}
	if len(plan.Options[1].Authority) != 1 || plan.Presentation.Location != nil {
		t.Fatal("composition mutated the original review")
	}
	permission.Fingerprints = []secretmatch.SecretFingerprint{"unreviewed-value"}
	if _, err := hitl.ComposeSecretPermission(plan, action, permission, hitl.FaceContext{}); err == nil {
		t.Fatal("compound review accepted a grant for different protected values")
	}
}
