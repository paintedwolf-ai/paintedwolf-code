package toolcontract

import (
	"slices"
	"strings"
	"testing"
)

func TestNativeUtilitiesLineNamesCatalogUtilitiesOnly(t *testing.T) {
	line, err := NativeUtilitiesLine()
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Split(strings.TrimSuffix(strings.TrimPrefix(line, "Native utilities: "), "."), ", ")
	for _, want := range []string{"cat", "grep", "find", "ls", "jq", "sed", "awk", "git", "curl", "tar", "summarize", "outline"} {
		if !slices.Contains(names, want) {
			t.Errorf("utility line omits %s: %s", want, line)
		}
	}
	for _, absent := range []string{"external", "native_path", "[", "[[", ":", "git status", "true"} {
		if slices.Contains(names, absent) {
			t.Errorf("utility line advertises %q", absent)
		}
	}
	if !slices.IsSorted(names) || len(names) != len(slices.Compact(slices.Clone(names))) {
		t.Fatalf("utility line is not a sorted set: %s", line)
	}
	if len(line) > 1200 {
		t.Fatalf("utility line is %d bytes", len(line))
	}
}
