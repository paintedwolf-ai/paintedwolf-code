package call_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/call"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

type reservationPresence struct {
	tools.PresenceReporter
	reserved, released []agentpresence.Target
	allReleased        bool
}

func (p *reservationPresence) Reserved(targets []agentpresence.Target) { p.reserved = targets }
func (p *reservationPresence) Released(targets []agentpresence.Target) {
	p.released, p.allReleased = targets, targets == nil
}

func TestHandoffToolsPreserveSessionAgentAndReservationEffects(t *testing.T) {
	database := openCallTestDB(t)
	lookup := call.StoreSessionLookup{Get: func(context.Context, string) (string, error) { return "/tmp/a", nil }}
	manager := call.NewSQLManager(database, lookup)
	registry := tools.NewDefaultRegistry()
	testutil.FailErr(t, "register handoff tools", call.RegisterHandoffTools(registry, call.HandoffToolDeps{Calls: manager, Sessions: lookup}))
	presence := &reservationPresence{}
	invocation := tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: "child", ParentSessionID: "sess-b", HandoffSessionID: "sess-a", Agent: "child-agent", HandoffAgentID: "parent-agent"},
		Source:   tools.InvocationSource{ActiveRootID: "root"},
		Effects:  tools.InvocationEffects{Presence: presence},
	}
	run := func(name string, args map[string]any) {
		t.Helper()
		_, err := registry.Run(t.Context(), name, args, invocation)
		testutil.FailErr(t, "run "+name, err)
	}
	run("handoff_init", map[string]any{})
	run("handoff_reserve", map[string]any{"paths": []string{" src/main.go "}})
	rows, err := manager.ListActiveReservations(t.Context(), "sess-a")
	testutil.FailErr(t, "read handoff reservation", err)
	if len(rows) != 1 || rows[0].Path != "src/main.go" || rows[0].Agent != "parent-agent" {
		t.Fatalf("handoff reservation = %+v", rows)
	}
	if len(presence.reserved) != 1 || presence.reserved[0] != (agentpresence.Target{RootID: "root", Path: "src/main.go"}) {
		t.Fatalf("reservation presence = %+v", presence.reserved)
	}
	run("handoff_release", map[string]any{"paths": []any{" src/main.go ", " "}})
	rows, err = manager.ListActiveReservations(t.Context(), "sess-a")
	testutil.FailErr(t, "read released reservation", err)
	if len(rows) != 0 || len(presence.released) != 1 || presence.released[0].Path != "src/main.go" {
		t.Fatalf("release did not settle reservation: rows=%+v presence=%+v", rows, presence.released)
	}
	run("handoff_reserve", map[string]any{"session_id": "sess-b", "agent": "explicit-agent", "paths": []any{"other.go"}})
	rows, err = manager.ListActiveReservations(t.Context(), "sess-b")
	testutil.FailErr(t, "read explicit reservation", err)
	if len(rows) != 1 || rows[0].Agent != "explicit-agent" {
		t.Fatalf("explicit reservation = %+v", rows)
	}
	run("handoff_release_all", map[string]any{"session_id": "sess-b", "agent": "explicit-agent"})
	if !presence.allReleased {
		t.Fatal("release all omitted presence notification")
	}
	health, err := manager.Health(t.Context(), "sess-b")
	testutil.FailErr(t, "read released session health", err)
	if health.ReservationCount != 0 {
		t.Fatalf("release all left reservations: %+v", health)
	}
	run("handoff_health", map[string]any{"session_id": "sess-b"})
	for _, paths := range []any{[]string{" "}, []any{" ", 3}, "other.go"} {
		if _, err := registry.Run(t.Context(), "handoff_reserve", map[string]any{"paths": paths}, invocation); err == nil {
			t.Fatalf("invalid paths accepted: %+v", paths)
		}
	}
}
