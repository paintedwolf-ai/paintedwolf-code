package toolvocab_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolvocab"
)

// Grants and denies of retired tools are refused with the recorded successor;
// live tools, command operations and patterns are not retired uses.
func TestRetiredNamesRefusesProfileReferences(t *testing.T) {
	live := []sandbox.ToolProfile{{ID: "live", Tools: map[string]bool{"command": true, "state_start": true}, DenyTools: []string{"state_*", "git_commit", "read"}}}
	if err := toolvocab.RetiredNames(live, nil, nil); err != nil {
		t.Fatalf("live names refused: %v", err)
	}
	for _, profile := range []sandbox.ToolProfile{
		{ID: "grants", Tools: map[string]bool{"handoff_health": true}},
		{ID: "denies", DenyTools: []string{"handoff_health"}},
	} {
		var retired *toolcontract.RetiredToolError
		err := toolvocab.RetiredNames([]sandbox.ToolProfile{profile}, nil, nil)
		if !errors.As(err, &retired) || retired.Tool != "handoff_health" ||
			!slices.Equal(retired.Retirement.Successors, []string{"pack_board"}) {
			t.Fatalf("%s: refusal = %v", profile.ID, err)
		}
	}
}
