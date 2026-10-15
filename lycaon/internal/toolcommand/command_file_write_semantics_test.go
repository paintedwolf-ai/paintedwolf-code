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
		{"dd append opening flags", "dd of=out.txt oflag=append", "xy", false},
		{"dd invalid conversion", "dd of=out.txt conv=notrunc,append", "xy", false},
		{"wrapper changes directory", "env --chdir=/tmp sh -c 'cat > out.txt'", "xy", false},
		{"pipeline changes case", "cat | tr a-z A-Z > out.txt", "xy", false},
		{"pipeline selects first line", "cat | head -n 1 > out.txt", "first\nsecond\n", false},
		{"pipeline numbers lines", "cat | cat -n > out.txt", "xy\n", false},
		{"producer discards pipeline stream", "cat > /dev/null | cat > out.txt", "xy", false},
		{"producer appends to null before pipe", "cat >> /dev/null | cat > out.txt", "xy", false},
		{"last redirect discards stream", "cat > out.txt > /dev/null", "xy", false},
		{"empty command truncates only", "> out.txt", "xy", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, matched := InspectCommandFileWrite(t.Context(), "command", map[string]any{"command": tc.command, "stdin": tc.stdin}, t.TempDir())
			if (tc.wantExact && !matched) || got.Exact != tc.wantExact || (!tc.wantExact && len(got.Calls) != 0) {
				t.Fatalf("command=%q stdin=%q: matched=%t exact=%t replacement=%#v; want exact=%t without semantic changes", tc.command, tc.stdin, matched, got.Exact, got.Calls, tc.wantExact)
			}
		})
	}
}
