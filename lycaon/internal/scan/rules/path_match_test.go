package rules

import "testing"

func TestPathUnderExclude(t *testing.T) {
	patterns := []string{"node_modules", "target", ".yarn/cache", "bin/Debug"}
	cases := map[string]bool{
		"src/main.go":                       false,
		"node_modules/x/index.js":           true,
		"pkg/node_modules/x":                true,
		"lycaon-den/src-tauri/target/debug": true,
		"target/foo":                        true,
		".yarn/cache/foo":                   true,
		"proj/.yarn/cache/foo":              true,
		"bin/Debug/out.dll":                 true,
		"bin/helper.sh":                     false,
		"vendor/lib.go":                     false,
	}
	for rel, want := range cases {
		if got := PathUnderExclude(rel, patterns); got != want {
			t.Fatalf("PathUnderExclude(%q) = %v, want %v", rel, got, want)
		}
	}
	if PathUnderExclude("node_modules/x", nil) {
		t.Fatal("empty patterns must not exclude")
	}
}
