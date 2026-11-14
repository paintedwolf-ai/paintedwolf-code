package prompts

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolpresentation"
)

func requestableRows(names ...string) []RequestableToolView {
	rows := make([]RequestableToolView, 0, len(names))
	for _, name := range names {
		rows = append(rows, RequestableToolView{Name: name, Role: toolpresentation.Role(name)})
	}
	return rows
}

func capabilityIDsOf(present map[string]bool) []string {
	var out []string
	for _, c := range capabilityLabels {
		if present[c.id] {
			out = append(out, c.id)
		}
	}
	return out
}

func TestRequestableCapabilitiesFollowLaunchersNotControls(t *testing.T) {
	controls := []string{"command_output", "command_stop", "terminal_send", "terminal_read", "terminal_snapshot", "terminal_close"}
	for _, tc := range []struct {
		name      string
		launchers []string
		want      []string
	}{
		{"both launchers", []string{"command", "terminal_open"}, []string{capabilityCommandAndTerminal}},
		{"command only", []string{"command"}, []string{capabilityCommandOnly}},
		{"terminal only", []string{"terminal_open"}, []string{capabilityTerminalOnly}},
		{"controls only", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := capabilityIDsOf(requestableCapabilityIDs(requestableRows(append(slices.Clone(tc.launchers), controls...)...)))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("capabilities = %v, want %v", got, tc.want)
			}
		})
	}
	pageControls := requestableCapabilityIDs(requestableRows("page_act", "page_snapshot", "page_close"))
	if len(pageControls) != 0 {
		t.Fatalf("page controls alone are not a capability: %v", pageControls)
	}
}

func TestInvestigateMoreToolsListsNothingAlreadyReachable(t *testing.T) {
	vars := map[string]any{}
	testutil.FailErr(t, "MergeCoordinatorSurfacePathVars", MergeCoordinatorSurfacePathVars(toolcontract.SurfaceImplementInvestigate, nil, vars, SurfaceTurn{}))
	labels, _ := vars["requestable_capabilities"].([]string)
	label := map[string]string{}
	for _, c := range capabilityLabels {
		label[c.id] = c.label
	}
	for _, id := range []string{capabilityCommandAndTerminal, capabilityCommandOnly, capabilityTerminalOnly} {
		if slices.Contains(labels, label[id]) {
			t.Fatalf("command and terminal_open are on the floor, so %s is not requestable: %v", id, labels)
		}
	}
	if !slices.Contains(labels, label["interface_test"]) {
		t.Fatalf("browser pages stay requestable: %v", labels)
	}
}
