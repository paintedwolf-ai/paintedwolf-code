package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/pkg/api"
)

// Every tool-approval card renders the same ladder: day, chat, and one durable
// slot that is present even when the reason cannot reach it — disabled, with a
// note — plus the one quiet row, for every reuse ceiling a gate can have.
func TestEveryGateOffersTheFixedSlots(t *testing.T) {
	t.Parallel()
	approvals := settings.NewRuleApprovalGate(mustApprovalStore(t), settings.NoSources())
	for _, g := range gate.All() {
		reuse := gate.ReuseFor(g)
		if !reuse.Offered() || g == api.GateSecretOutbound {
			// The secret ladder is minted by the secret screen, not the gate;
			// the secret card test pins its slots.
			continue
		}
		action := hitl.ProposedAction{
			Tool: "fetch_url", Args: map[string]any{"url": "https://api.example.com"},
			SessionID: "sess-slots", ProjectID: "proj-slots", ProjectDir: "/tmp/proj",
		}
		decision := &gate.Decision{Primary: g, Posture: gate.PostureBalanced}
		var offers []hitl.ApprovalGrantOffer
		switch {
		case g == api.GateAgentPolicyChange:
			policyAction := action
			policyAction.Tool = "write"
			policyAction.AgentPolicy = []hitl.AgentPolicyTarget{{Path: "/tmp/proj/AGENTS.md", Surface: "agents_md"}}
			offers = approvals.GrantOffers(policyAction, &hitl.ApprovalResult{Decision: decision})
		case gate.IsFilesystem(g):
			pathAction := hitl.ProposedAction{
				Tool: "write", Files: []string{"/tmp/other/a.txt"},
				SessionID: "sess-slots", ProjectID: "proj-slots", ProjectDir: "/tmp/proj",
			}
			offers = settings.GrantedPathOffers(pathAction, gate.FileTarget{
				Path: "/tmp/other/a.txt", Mode: gate.ModeWrite, OutsideRoots: true,
			}, decision, nil)
		default:
			offers = approvals.GrantOffers(action, &hitl.ApprovalResult{Decision: decision})
		}
		if len(offers) == 0 {
			t.Errorf("%s: no ladder at all", g)
			continue
		}
		var day, chat, durable *hitl.ApprovalGrantOffer
		for i := range offers {
			offer := &offers[i]
			if offer.Group != "" {
				continue
			}
			switch offer.Rung {
			case hitl.ApprovalRungDay:
				day = offer
			case hitl.ApprovalRungChat:
				chat = offer
			case hitl.ApprovalRungProject, hitl.ApprovalRungDevice:
				durable = offer
			case hitl.ApprovalRungRedacted, hitl.ApprovalRungTracked, hitl.ApprovalRungUnchanged, hitl.ApprovalRungOnce:
			}
		}
		if day == nil || chat == nil || durable == nil {
			t.Errorf("%s: ladder is missing a slot (day=%v chat=%v durable=%v): %+v", g, day != nil, chat != nil, durable != nil, offers)
			continue
		}
		if day.Disabled || chat.Disabled {
			t.Errorf("%s: day and chat rungs are always selectable", g)
		}
		if durable.Disabled && durable.Note == "" {
			t.Errorf("%s: a disabled durable slot must say why", g)
		}
		if !durable.Disabled && reuse.Scope == gate.ScopeChat {
			t.Errorf("%s: chat-ceiling gate offered a selectable durable rung", g)
		}
	}
}

func TestNoProjectKeepsTheDurableSlotDisabled(t *testing.T) {
	t.Parallel()
	approvals := settings.NewRuleApprovalGate(mustApprovalStore(t), settings.NoSources())
	action := hitl.ProposedAction{
		Tool: "fetch_url", Args: map[string]any{"url": "https://api.example.com"},
		SessionID: "sess-noproj",
	}
	offers := approvals.GrantOffers(action, &hitl.ApprovalResult{Decision: &gate.Decision{Primary: api.GateUserRule}})
	found := false
	for _, offer := range offers {
		if offer.Rung == hitl.ApprovalRungProject || offer.Rung == hitl.ApprovalRungDevice {
			found = true
			if !offer.Disabled || offer.Note != hitl.NoteNoProjectOpen {
				t.Fatalf("durable slot without a project = %+v", offer)
			}
		}
	}
	if !found {
		t.Fatalf("durable slot vanished without a project: %+v", offers)
	}
}

func TestQuietIsOneRowOnEveryDiscretionaryGate(t *testing.T) {
	t.Parallel()
	action := hitl.ProposedAction{Tool: "command", SessionID: "chat-1", RootSessionID: "chat-1", Command: "echo"}
	for _, g := range gate.All() {
		decision := &gate.Decision{Primary: g, ReasonKey: string(g) + ":subject"}
		quiet := hitl.QuietOptions(action, decision, nil, nil)
		if g == api.GateAgentPolicyChange {
			if len(quiet) != 0 {
				t.Errorf("%s: agent-policy review offers quiet options: %+v", g, quiet)
			}
			continue
		}
		if len(quiet) != 1 || quiet[0].Rung != hitl.ApprovalRungChat || quiet[0].Group != hitl.GroupQuiet {
			t.Errorf("%s: quiet slot = %+v, want one task row", g, quiet)
		}
	}
}
