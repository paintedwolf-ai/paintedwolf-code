package sandbox

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/pathglob"
)

func TestEntryGlobDiscoveryGrammar(t *testing.T) {
	for _, tc := range []struct {
		pattern, path string
		want          bool
	}{
		{"**/*.{yml,yaml,sh,json,toml,go,rs,ts}", "build/generated/config.json", true},
		{"**/*.{yml,yaml,sh,json,toml,go,rs,ts}", "release.yml", true},
		{"**/*.{go,ts}", "build/binary.o", false},
		{"*.{go,ts}", "node_modules/generated.ts", true},
		{"{src,build}/**/*.{go,{ts,tsx}}", "build/a/b.tsx", true},
		{"{src,build}/**/*.{go,{ts,tsx}}", "other/a.go", false},
		{"[!a]*.go", "beta.go", true},
		{`\{name\}.go`, "{name}.go", true},
		{"[{]name[}].go", "{name}.go", true},
		{"", "any/path", true},
	} {
		t.Run(tc.pattern+"/"+tc.path, func(t *testing.T) {
			glob, err := CompileEntryGlob(tc.pattern)
			testutil.FailErr(t, "compile glob", err)
			if got := glob.Match(tc.path); got != tc.want {
				t.Fatalf("match = %v, want %v", got, tc.want)
			}
		})
	}
	if pathglob.Match("*.{go,ts}", "main.go") {
		t.Fatal("discovery grammar must not broaden permission globs")
	}
}

func TestEntryGlobRejectsInvalidAndExcessivePatterns(t *testing.T) {
	for _, pattern := range []string{"[", "*.{go,ts", "*.go}", "{go}", "**/foo[", strings.Repeat("{a,b}", 9), strings.Repeat("a", 4097)} {
		if _, err := CompileEntryGlob(pattern); err == nil {
			t.Errorf("accepted invalid pattern %q", pattern)
		}
	}
}
