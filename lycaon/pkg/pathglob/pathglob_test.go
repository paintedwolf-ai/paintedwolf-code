package pathglob_test

import (
	"sync"
	"testing"

	"github.com/lycaon/lycaon/pkg/pathglob"
)

func TestMatchRecursiveAndWholeRoot(t *testing.T) {
	cases := []struct {
		pattern string
		path    string
		want    bool
	}{
		{"internal/**", "internal/ntp/client.go", true},
		{"internal/**", "internal/ntp/testdata/reply.bin", true},
		{"src/**/*.go", "src/a.go", true},
		{"src/**/*.go", "src/deep/a.go", true},
		{"src/*.go", "src/deep/a.go", false},
		{"**", "", true},
		{"**", "any/path.txt", true},
		{"", "any/path.txt", false},
		{"src/[!a]*.go", "src/b_test.go", true},
		{"src/[!a]*.go", "src/a_test.go", false},
	}
	for _, tc := range cases {
		if got := pathglob.Match(tc.pattern, tc.path); got != tc.want {
			t.Fatalf("Match(%q, %q) = %v want %v", tc.pattern, tc.path, got, tc.want)
		}
	}
}

func TestCoversDirectoryAndGlob(t *testing.T) {
	if !pathglob.Covers("internal", "internal/ntp/client.go") {
		t.Fatal("directory scope must cover descendants")
	}
	if !pathglob.Covers("internal/**", "internal/ntp/client.go") {
		t.Fatal("recursive glob must cover descendants")
	}
	if pathglob.Covers("internal/*.go", "internal/ntp/client.go") {
		t.Fatal("single-star glob must not cross a path segment")
	}
	if !pathglob.Covers(".", "src/deep/a.go") {
		t.Fatal("whole-root scope must cover every relative path")
	}
}

func TestCompiledRuleSetMatchesScalarAcrossCandidatesAndConcurrency(t *testing.T) {
	patterns := []string{
		"",
		"**/.env",
		"**/.env.*",
		"**/.config/gcloud/**",
		"**/.ssh/*",
		"keys/[!t]*.pem",
	}
	set := pathglob.Compile(patterns)
	if set.Len() != 5 {
		t.Fatalf("compiled rule count = %d, want 5", set.Len())
	}
	candidates := []string{
		"",
		".env",
		"services/api/.env.local",
		".config/gcloud/credentials.db",
		".config/gcloud",
		"ops/.ssh/id_ed25519",
		"ops/.ssh/nested/config",
		"keys/prod.pem",
		"keys/test.pem",
		"src/internal/main.go",
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, p := range candidates {
				scalar := false
				for _, pat := range patterns {
					if pathglob.Match(pat, p) {
						scalar = true
						break
					}
				}
				cand := pathglob.PrepareCandidate(p)
				if got := set.MatchCandidate(cand); got != scalar {
					t.Errorf("MatchCandidate(%q) = %v, scalar = %v", p, got, scalar)
				}
				if got := set.Match(p); got != scalar {
					t.Errorf("RuleSet.Match(%q) = %v, scalar = %v", p, got, scalar)
				}
			}
		}()
	}
	wg.Wait()
}
