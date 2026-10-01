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
		{"empty", "", "", false},
		{"unterminated quote", "tool '", "tool '", false},
		{"unquoted newline", "tool a\ntool b", "tool a\ntool b", false},
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
