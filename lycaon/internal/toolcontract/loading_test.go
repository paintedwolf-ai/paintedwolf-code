package toolcontract_test

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestLoadFamiliesRespectsAvailability(t *testing.T) {
	cases := []struct {
		name                      string
		selected, available, want []string
	}{
		{"terminal", []string{"terminal_open"}, []string{"terminal_open", "terminal_read", "terminal_close"}, []string{"terminal_open", "terminal_read", "terminal_close"}},
		{"page", []string{"page_act"}, []string{"page_open", "page_act", "page_snapshot"}, []string{"page_act", "page_open", "page_snapshot"}},
		{"unavailable seed", []string{"page_open"}, []string{"page_act"}, nil},
		{"unrelated", []string{"command", "command", "mcp_example"}, []string{"command", "command_output", "mcp_example"}, []string{"command", "mcp_example"}},
		{"closed", []string{"terminal_open"}, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := slices.Clone(tc.selected)
			got := toolcontract.LoadFamilies(tc.selected, func(name string) bool { return slices.Contains(tc.available, name) })
			if !slices.Equal(got, tc.want) || !slices.Equal(tc.selected, before) {
				t.Fatalf("loaded=%v want=%v; input=%v want=%v", got, tc.want, tc.selected, before)
			}
		})
	}
}
