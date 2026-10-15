package toolcommand

import "testing"

func TestFileWriteReplacementPreservesConsumerSemantics(t *testing.T) {
	cases := []struct {
		name      string
		command   string
		stdin     string
		wantExact bool
	}{
		{"cat identity control", "cat > out.txt", "xy", true},
		{"dd preserves existing suffix", "dd of=out.txt conv=notrunc", "xy", false},
		{"pipeline changes case", "cat | tr a-z A-Z > out.txt", "xy", false},
		{"pipeline selects first line", "cat | head -n 1 > out.txt", "first\nsecond\n", false},
		{"pipeline numbers lines", "cat | cat -n > out.txt", "xy\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := InspectCommandFileWrite(t.Context(), "command", map[string]any{"command": tc.command, "stdin": tc.stdin}, t.TempDir())
			if !matched || got.Exact != tc.wantExact || (!tc.wantExact && len(got.Calls) != 0) {
				t.Fatalf("command=%q stdin=%q: matched=%t exact=%t replacement=%#v; want exact=%t without semantic changes", tc.command, tc.stdin, matched, got.Exact, got.Calls, tc.wantExact)
			}
		})
	}
}
