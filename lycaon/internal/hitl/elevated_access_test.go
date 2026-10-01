package hitl

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestElevatedGrantEffects(t *testing.T) {
	tests := []struct {
		category, pattern string
		want              []api.ElevatedAccessEffect
	}{
		{ApprovalGrantCategoryDirectIP, "", []api.ElevatedAccessEffect{api.ElevatedAccessEffectDirectNetwork}},
		{ApprovalGrantCategorySocketPath, "/service.sock", []api.ElevatedAccessEffect{api.ElevatedAccessEffectLocalService}},
		{ApprovalGrantCategoryExecutionCapability, "host_execution", []api.ElevatedAccessEffect{api.ElevatedAccessEffectHostExecution}},
		{ApprovalGrantCategoryExecutionCapability, "process_signal", []api.ElevatedAccessEffect{api.ElevatedAccessEffectProcessControl}},
		{ApprovalGrantCategoryExecutionCapability, "process_list", nil},
		{"host", "example.com", nil}, {"write_root", "/tmp", nil}, {"secret_redact", "", nil},
		{"local_listen", "8080", nil}, {"loopback_connect", "8080", nil},
		{"host_resource", "daemon", nil},
	}
	for _, tt := range tests {
		t.Run(tt.category+tt.pattern, func(t *testing.T) {
			got := ElevatedGrantEffects(ApprovalGrant{Predicate: ApprovalGrantPredicate{Category: tt.category, Pattern: tt.pattern}})
			if !slices.Equal(got, tt.want) {
				t.Fatalf("effects = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestElevatedQuietClassification(t *testing.T) {
	for _, subject := range []string{"direct_ip", "host_execution", "unconfined", "process_control", "native_process:signal", "socket:/var/run/test.sock"} {
		if len(elevatedQuietEffects(string(api.GateUnobservedChannel)+":"+subject)) != 1 {
			t.Errorf("missing effect for %s", subject)
		}
	}
	for _, subject := range []string{"native_process:list", "loopback:8080", "listen:8080", "external link"} {
		if got := elevatedQuietEffects(string(api.GateUnobservedChannel) + ":" + subject); len(got) != 0 {
			t.Errorf("unexpected effect for %s: %v", subject, got)
		}
	}
	if ValidateElevatedEffects([]api.ElevatedAccessEffect{"unknown"}) == nil {
		t.Fatal("unknown persisted effect accepted")
	}
}

func TestExactActionRetainsEveryElevatedBoundary(t *testing.T) {
	action := ProposedAction{Tool: "command", SessionID: "chat", Args: map[string]any{"command": "run"},
		Contained: Contained{DirectIP: true, HostExecution: true, ProcessControl: true, SocketCount: 1}}
	grant := ExactActionOfferAtScope(action, ApprovalGrantScopeChat).Grant
	want := []api.ElevatedAccessEffect{api.ElevatedAccessEffectDirectNetwork, api.ElevatedAccessEffectHostExecution, api.ElevatedAccessEffectLocalService, api.ElevatedAccessEffectProcessControl}
	if got := ElevatedGrantEffects(grant); !slices.Equal(got, want) {
		t.Fatalf("exact action lost authority: %v", got)
	}
	ordinary := ExactActionOfferAtScope(ProposedAction{Tool: "read", SessionID: "chat"}, ApprovalGrantScopeChat).Grant
	if len(ordinary.ElevatedEffects) != 0 {
		t.Fatal("ordinary exact action classified as elevated")
	}
}

func TestApprovalCardElevatedEffectsFollowSavedChoices(t *testing.T) {
	grant := ApprovalGrant{Predicate: ApprovalGrantPredicate{Category: ApprovalGrantCategoryExecutionCapability, Pattern: "host_execution"}}
	quiet := AskQuietDelta{ElevatedEffects: []api.ElevatedAccessEffect{api.ElevatedAccessEffectDirectNetwork}}
	options := []ApprovalOption{
		{Kind: ApprovalOptionCurrentAction, Authority: []ApprovalAuthorityDelta{{Kind: AuthorityCurrentAction, Grant: &grant}}},
		{Kind: ApprovalOptionLease, Disabled: true, Authority: []ApprovalAuthorityDelta{{Kind: AuthorityGenericGrant, Grant: &grant}}},
		{Kind: ApprovalOptionLease, Authority: []ApprovalAuthorityDelta{{Kind: AuthorityGenericGrant, Grant: &grant}}},
		{Kind: ApprovalOptionQuiet, Authority: []ApprovalAuthorityDelta{{Kind: AuthorityAskQuiet, AskQuiet: &quiet}}},
	}
	want := []api.ElevatedAccessEffect{api.ElevatedAccessEffectDirectNetwork, api.ElevatedAccessEffectHostExecution}
	if got := savedOptionElevatedEffects(options); !slices.Equal(got, want) {
		t.Fatalf("card effects = %v, want %v", got, want)
	}
	if got := wireApprovalPlan(&ApprovalPlan{Options: options}).ElevatedEffects; !slices.Equal(got, want) {
		t.Fatalf("wire card effects = %v, want %v", got, want)
	}
	if got := savedOptionElevatedEffects(options[:2]); len(got) != 0 {
		t.Fatalf("one-time and disabled choices advertised saved access: %v", got)
	}
}
