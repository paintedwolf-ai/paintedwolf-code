package settings_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/settings"
)

func TestMatchCommandPatternWildcards(t *testing.T) {
	tests := []struct {
		pattern string
		command string
		want    bool
	}{
		{"*", "anything goes", true},
		{"go test *", "go test ./...", true},
		{"go test *", "go test", true},
		{"go test*", "go test", true},
		{"git *", "git status", true},
		{"git *", "git commit -m x", true},
		{"find * -delete*", "find . -delete", true},
		{"find * -delete*", "find src -name foo -delete", true},
		{"find * -delete*", "find . -type f", false},
		{"sort -o*", "sort -o /tmp/out", true},
		{"sort -o*", "sort -n file.txt", false},
		{"sed -i*", "sed -i.bak file.txt", true},
		{"sed -i*", "sed file.txt", false},
		{"rm *", "rm -rf build", true},
		{"rm *", "gorm", false},
		{"./task *", "./task test:digest", true},
	}
	for _, tc := range tests {
		got := settings.MatchCommandPattern(tc.command, tc.pattern)
		if got != tc.want {
			t.Fatalf("MatchCommandPattern(%q, %q) = %v, want %v", tc.command, tc.pattern, got, tc.want)
		}
	}
}
