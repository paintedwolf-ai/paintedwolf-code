package projectadmin

import (
	"testing"
)

func TestRepoNameFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/acme/widgets.git": "widgets",
		"https://github.com/acme/widgets":     "widgets",
		"git@github.com:acme/widgets.git":     "widgets",
		"https://example.com/a/b/c/repo/":     "repo",
	}
	for in, want := range cases {
		if got := repoNameFromURL(in); got != want {
			t.Fatalf("repoNameFromURL(%q) = %q want %q", in, got, want)
		}
	}
}
