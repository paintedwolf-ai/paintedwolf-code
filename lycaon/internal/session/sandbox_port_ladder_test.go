package session

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestListenPlanOffersStandardRungs(t *testing.T) {
	t.Parallel()
	broker := &ListenCheckpointBroker{}
	card, err := broker.buildListenCard(tools.LocalListenAsk{
		Command: "docker compose up", ToolName: "command", ProjectDir: "/tmp/proj",
		Ports: []uint16{3000},
	}, "child", "root", []uint16{3000})
	if err != nil {
		t.Fatalf("buildListenCard: %v", err)
	}
	assertStandardPortRungs(t, card.Plan, hitl.ApprovalSubjectLocalListen)
}

func TestLoopbackPlanOffersStandardRungs(t *testing.T) {
	t.Parallel()
	broker := &LoopbackCheckpointBroker{}
	card, err := broker.buildCard(tools.LoopbackConnectAsk{
		Command: "curl localhost:8123", ToolName: "command", ProjectDir: "/tmp/proj",
		Ports: []uint16{8123},
	}, "root", "root", []uint16{8123})
	testutil.FailErr(t, "buildCard", err)
	assertStandardPortRungs(t, card.Plan, hitl.ApprovalSubjectLoopbackConnect)
}

func assertStandardPortRungs(t *testing.T, plan *hitl.ApprovalPlan, kind hitl.ApprovalSubjectKind) {
	t.Helper()
	if plan == nil {
		t.Fatal("plan is nil")
	}
	if plan.Subject.Kind != kind {
		t.Fatalf("subject = %s, want %s", plan.Subject.Kind, kind)
	}
	seen := map[hitl.ApprovalOptionRung]hitl.ApprovalOption{}
	var quietCount int
	for _, option := range plan.Options {
		if option.Kind == hitl.ApprovalOptionQuiet {
			quietCount++
			if !plan.OptionContinues(option) {
				t.Fatalf("quiet %s does not continue %s: %+v", option.Rung, kind, option.Authority)
			}
			continue
		}
		if option.Group != "" {
			continue
		}
		seen[option.Rung] = option
		if !plan.OptionContinues(option) {
			t.Fatalf("%s option does not continue %s: %+v", option.Rung, kind, option.Authority)
		}
	}
	if quietCount != 0 {
		t.Fatalf("quiet options = %d want 0 when chat lease is enabled", quietCount)
	}
	if _, ok := seen[hitl.ApprovalRungOnce]; !ok {
		t.Fatal("missing Allow once")
	}
	if _, ok := seen[hitl.ApprovalRungDay]; !ok {
		t.Fatal("missing Allow for 1 day")
	}
	if _, ok := seen[hitl.ApprovalRungChat]; !ok {
		t.Fatal("missing Allow for this chat")
	}
	// Local-network authority ends with the chat: the durable slot is present,
	// disabled, and says so.
	if project, ok := seen[hitl.ApprovalRungProject]; !ok || !project.Disabled || project.Note != hitl.NoteEndsWithChat {
		t.Fatalf("durable slot = %+v, want disabled with the ends-with-chat note", project)
	}
	if plan.RecommendedOptionID != seen[hitl.ApprovalRungChat].ID {
		t.Fatalf("face = %q, want the chat lease %q", plan.RecommendedOptionID, seen[hitl.ApprovalRungChat].ID)
	}
	for _, rung := range []hitl.ApprovalOptionRung{hitl.ApprovalRungDay, hitl.ApprovalRungChat} {
		option := seen[rung]
		for _, delta := range option.Authority {
			if len(delta.ListenPorts) != 0 || len(delta.ConnectPorts) != 0 {
				t.Fatalf("%s lease is port-narrowed: listen=%v connect=%v", rung, delta.ListenPorts, delta.ConnectPorts)
			}
		}
	}
}

func TestLocalNetworkPlanContinuesBothAxes(t *testing.T) {
	t.Parallel()
	broker := &LocalNetworkCheckpointBroker{}
	card, err := broker.buildCard(tools.LocalNetworkAsk{
		Command: "node server.js", ToolName: "command", ProjectDir: "/tmp/proj",
		ListenPorts: []uint16{3000}, ConnectPorts: []uint16{3000},
	}, "root", "root")
	testutil.FailErr(t, "buildCard", err)
	if card.Decision == nil || card.Decision.Primary != api.GateCapabilityWidening {
		t.Fatalf("primary = %v, want capability_widening", card.Decision)
	}
	plan := card.Plan
	assertStandardPortRungs(t, plan, hitl.ApprovalSubjectActionSet)
	for _, option := range plan.Options {
		if option.Kind == hitl.ApprovalOptionQuiet {
			continue
		}
		if !plan.OptionContinues(option) {
			t.Fatalf("%s does not continue both axes: %+v", option.Rung, option.Authority)
		}
	}
}

func TestListenPlanKeepsChatAuthorityOnChildSession(t *testing.T) {
	broker := &ListenCheckpointBroker{}
	card, err := broker.buildListenCard(tools.LocalListenAsk{ProjectDir: "/tmp/proj", Ports: []uint16{3000}}, "child", "root", []uint16{3000})
	testutil.FailErr(t, "build listen card", err)
	if card.Action.RootSessionID != "root" || card.Action.ChatSession() != "root" || card.Action.SessionID != "child" {
		t.Fatalf("action sessions = root %q chat %q child %q", card.Action.RootSessionID, card.Action.ChatSession(), card.Action.SessionID)
	}
	for _, option := range card.Plan.Options {
		for _, delta := range option.Authority {
			if delta.Kind == hitl.AuthorityLocalListenChat && delta.ChatSession() != "root" {
				t.Fatalf("chat authority session = %q, want root", delta.ChatSession())
			}
		}
	}
}

func TestListenDayGrantExpires(t *testing.T) {
	t.Parallel()
	rt := approvalstate.NewSandboxPortGrantRuntime()
	past := time.Now().UTC().Add(-time.Second)
	rt.GrantChat("chat", []uint16{8000}, "grant_day", "cp1", &past)
	if granted, _ := rt.SessionPorts("chat"); granted {
		t.Fatal("expired day grant still authorizes")
	}
	future := time.Now().UTC().Add(time.Hour)
	rt.GrantChat("chat", []uint16{8000}, "grant_live", "cp2", &future)
	granted, ports := rt.SessionPorts("chat")
	if !granted || len(ports) != 1 || ports[0] != 8000 {
		t.Fatalf("live day grant: granted=%v ports=%v", granted, ports)
	}
}
