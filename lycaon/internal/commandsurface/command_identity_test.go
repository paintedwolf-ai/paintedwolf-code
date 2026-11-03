package commandsurface_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/commandsurface"
)

func TestSameCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name, left, right string
		equal             bool
	}{
		{"spacing", "./task check", "  ./task   check  ", true},
		{"quote syntax", `tool 'two words'`, `tool "two words"`, true},
		{"escaped space", `tool two\ words`, `tool 'two words'`, true},
		{"literal backslash", `tool two\words`, `tool twowords`, false},
		{"sequence spacing", "tool a&&tool b", "tool a && tool b", true},
		{"quoted spacing", `tool 'two  words'`, `tool 'two words'`, false},
		{"quoted newline", "tool 'two\nwords'", "tool 'two words'", false},
		{"argument boundary", `tool 'two words'`, "tool two words", false},
		{"empty argument", `tool ''`, "tool", false},
		{"operator", "tool a && tool b", "tool a || tool b", false},
		{"literal versus expansion", `tool '$value'`, `tool "$value"`, false},
		{"substitution", `tool "$(printf a)"`, `tool "$(printf a)"`, true},
		{"substitution changed", `tool "$(printf a)"`, `tool "$(printf b)"`, false},
		{"literal substitution", `tool '$(printf a)'`, `tool "$(printf a)"`, false},
		{"redirect changed", `tool > first`, `tool > second`, false},
		{"redirect append", `tool > first`, `tool >> first`, false},
		{"background versus sequence", `tool & next`, `tool; next`, false},
		{"glob versus literal", `tool *.go`, `tool '*.go'`, false},
		{"literal versus brace", `tool '{a,b}'`, `tool {a,b}`, false},
		{"assignment", `VALUE=a tool`, `VALUE=b tool`, false},
		{"empty", "", "", false},
		{"unterminated quote", "tool '", "tool '", false},
		{"unquoted newline", "tool a\ntool b", "tool a\ntool b", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := commandsurface.SameCommandLine(tc.left, tc.right); got != tc.equal {
				t.Fatalf("SameCommandLine(%q, %q) = %v, want %v", tc.left, tc.right, got, tc.equal)
			}
		})
	}
}

func TestCheckKeySeparatesRenderedPipelines(t *testing.T) {
	if commandsurface.CheckKey("check a | report", ".") == commandsurface.CheckKey("check b | report", ".") {
		t.Fatal("different pipelines collapsed into one check")
	}
	if commandsurface.CheckKey("check a", ".") == commandsurface.CheckKey("check a", "nested") {
		t.Fatal("different working directories collapsed into one check")
	}
}
