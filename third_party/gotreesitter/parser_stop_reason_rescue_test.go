package gotreesitter

import "testing"

func stopReasonRescueTestTree(reason ParseStopReason) *Tree {
	tree := &Tree{}
	if reason != "" {
		tree.setParseStopReason(reason)
	}
	return tree
}

func TestRescueMaterializationStopReason(t *testing.T) {
	cases := []struct {
		name       string
		loopReason ParseStopReason
		treeReason ParseStopReason
		want       ParseStopReason
	}{
		{"accepted loop, budget-stamped sentinel tree", ParseStopAccepted, ParseStopMemoryBudget, ParseStopMemoryBudget},
		{"accepted loop, node-limit sentinel tree", ParseStopAccepted, ParseStopNodeLimit, ParseStopNodeLimit},
		{"accepted loop, ordinary tree without a reason", ParseStopAccepted, "", ParseStopAccepted},
		{"accepted loop, tree already accepted", ParseStopAccepted, ParseStopAccepted, ParseStopAccepted},
		{"loop stop wins over materialization reason", ParseStopNoStacksAlive, ParseStopMemoryBudget, ParseStopNoStacksAlive},
		{"loop budget stop passes through", ParseStopMemoryBudget, "", ParseStopMemoryBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tree := stopReasonRescueTestTree(tc.treeReason)
			if got := rescueMaterializationStopReason(tc.loopReason, tree); got != tc.want {
				t.Fatalf("rescueMaterializationStopReason(%q, tree=%q) = %q, want %q", tc.loopReason, tc.treeReason, got, tc.want)
			}
		})
	}
	if got := rescueMaterializationStopReason(ParseStopAccepted, nil); got != ParseStopAccepted {
		t.Fatalf("nil tree: got %q, want accepted", got)
	}
}
