package workercloseout

import "testing"

func TestJoinProvenance(t *testing.T) {
	cases := []struct {
		name, base, extra, want string
	}{
		{"both set", "agent_json", "grounding_retry+attempt_1", "agent_json+grounding_retry+attempt_1"},
		{"empty base", "", "grounding_retry+attempt_1", "grounding_retry+attempt_1"},
		{"empty extra", "agent_json", "", "agent_json"},
		{"both empty", "", "", ""},
		{"whitespace base", "  ", "trimmed", "trimmed"},
		{"whitespace extra", "agent_json", "  ", "agent_json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinProvenance(tc.base, tc.extra); got != tc.want {
				t.Fatalf("joinProvenance(%q, %q) = %q want %q", tc.base, tc.extra, got, tc.want)
			}
		})
	}
}
