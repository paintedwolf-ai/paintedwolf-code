package settings_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/capabilitygrants"
	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// frozenNonSecretTitles lists stable non-secret ladder labels.
var frozenNonSecretTitles = map[string]bool{
	hitl.TitleAllowOnce:           true,
	hitl.TitleAllowFor1Day:        true,
	hitl.TitleAllowForThisChat:    true,
	hitl.TitleAllowForThisProject: true,
	hitl.TitleAllowOnThisDevice:   true,
	hitl.TitleQuietForThisChat:    true,

	hitl.TitleAllowCommandNetworkForThisChat: true,
}

// TestPlanOptionTitlesAreFrozen sweeps every ordinary ladder mint site.
func TestPlanOptionTitlesAreFrozen(t *testing.T) {
	store := ladderTitleStore(t)
	approvals := settings.NewRuleApprovalGate(store, settings.NoSources())
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "network",
			Args: map[string]any{"host": "api.example.com"},
		},
		Scope: hitl.ActionScope{
			SessionID:  "sess-titles",
			ProjectID:  "proj-titles",
			ProjectDir: "/tmp/proj",
		},
	}
	checkOffers := func(label string, offers []hitl.ApprovalGrantOffer) {
		t.Helper()
		if len(offers) == 0 {
			t.Fatalf("%s: empty ladder", label)
		}
		for _, offer := range offers {
			if !frozenNonSecretTitles[offer.Title] {
				t.Fatalf("%s: title %q not in frozen set", label, offer.Title)
			}
		}
	}

	result := &hitl.ApprovalResult{Decision: &gate.Decision{Primary: api.GateUserRule}}
	checkOffers("host", approvals.GrantOffers(action, result))

	pathAction := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool:  "write",
			Files: []string{"/tmp/proj/a.txt"},
		},
		Scope: hitl.ActionScope{
			SessionID:  "sess-titles",
			ProjectID:  "proj-titles",
			ProjectDir: "/tmp/proj",
		},
	}
	checkOffers("path", settings.GrantedPathOffers(pathAction, gate.FileTarget{
		Path: "/tmp/proj/a.txt", Mode: gate.ModeWrite,
	}, &gate.Decision{Primary: api.GateSensitiveLocation}, nil))

	cmd := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "command",
			Args: map[string]any{"command": "git status"},
		},
		Presentation: hitl.ActionPresentation{
			Command: "git status",
		},
		Scope: hitl.ActionScope{
			SessionID:  "sess-titles",
			ProjectID:  "proj-titles",
			ProjectDir: "/tmp/proj",
		},
	}
	checkOffers("exact action", approvals.GrantOffers(cmd, &hitl.ApprovalResult{
		Decision: &gate.Decision{Primary: api.GateExplicitApprovalRequest},
	}))

	checkOffers("socket", capabilitygrants.SocketExecutionGrantOffers(cmd, []confine.SocketGrant{{
		ApprovedPath: "/tmp/svc.sock", ResolvedPath: "/private/tmp/svc.sock",
	}}))

	checkOffers("direct ip", capabilitygrants.DirectIPExecutionGrantOffers(cmd, hitl.DirectIPLease{
		ActionDigest: "action-a", RequestDigest: "req-a", ConfinementDigest: "conf-a",
		DeclaredDestinations: []string{"udp://1.2.3.4:123"}, CommandSummary: "ntpdate",
	}))

	hr := action
	hr.Resources.HostResources = []string{"camera"}
	checkOffers("host resource card", approvals.GrantOffers(hr, &hitl.ApprovalResult{
		Decision: &gate.Decision{Primary: api.GateUserRule}, HostResourceApproval: true,
	}))

	quiet := hitl.QuietOptions(cmd, &gate.Decision{
		Primary:   api.GateAuthorityMisuse,
		ReasonKey: "authority_misuse:aws-cli/s3-remove-bucket",
	}, nil, nil)
	if len(quiet) == 0 {
		t.Fatal("quiet ladder empty")
	}
	for _, opt := range quiet {
		if !frozenNonSecretTitles[opt.Title] {
			t.Fatalf("quiet: title %q not in frozen set", opt.Title)
		}
	}
}

// TestTwoSubjectCardsCarryGroups pins absorbed and host-resource second ladders.
func TestTwoSubjectCardsCarryGroups(t *testing.T) {
	approvals := settings.NewRuleApprovalGate(ladderTitleStore(t), settings.NoSources())
	action := hitl.ProposedAction{
		Invocation: hitl.ActionInvocation{
			Tool: "network",
			Args: map[string]any{"host": "api.example.com"},
		},
		Scope: hitl.ActionScope{
			SessionID:  "sess-groups",
			ProjectID:  "proj-groups",
			ProjectDir: "/tmp/proj",
		},
		Resources: hitl.ActionResources{
			HostResources: []string{"camera"},
		},
	}
	result := &hitl.ApprovalResult{Decision: &gate.Decision{Primary: api.GateUserRule}, HostResourceApproval: true}

	offered := approvals.GrantOffers(action, result)
	var sawHostResources bool
	for _, offer := range offered {
		if offer.Group == hitl.GroupHostResources {
			sawHostResources = true
		}
	}
	if !sawHostResources {
		t.Fatal("host-resource-riding card must tag the second ladder Host resources")
	}

	absorbed := approvals.AbsorbedGrantOffers(action, result)
	if len(absorbed) == 0 {
		t.Fatal("absorbed ladder empty")
	}
	for _, offer := range absorbed {
		if offer.Group != hitl.GroupAlsoAllow && offer.Group != hitl.GroupHostResources {
			t.Fatalf("absorbed offer group = %q, want Also allow or Host resources", offer.Group)
		}
	}
}

func ladderTitleStore(t *testing.T) *settings.ApprovalStore {
	t.Helper()
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "NewApprovalStoreAt", err)
	return store
}
