package gitexec

import (
	"errors"
	"slices"
	"testing"
)

func gitConfig(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, code, err := Run(t.Context(), dir, append([]string{"config"}, args...), Opts{}); err != nil || code != 0 {
		t.Fatalf("git config %v: code=%d err=%v out=%s", args, code, err, out)
	}
}

// Every URL a network command contacts resolves through Git's own selection
// and rewriting, so admission and credentials cover what Git will dial.
func TestResolveRemoteURLsFollowsGitSelectionAndRewrites(t *testing.T) {
	useBundledGit(t)
	dir := t.TempDir()
	initRepo(t, dir)
	if out, code, err := Run(t.Context(), dir, []string{"checkout", "-q", "-b", "main"}, Opts{}); err != nil || code != 0 {
		t.Fatalf("checkout: code=%d err=%v out=%s", code, err, out)
	}
	gitConfig(t, dir, "remote.origin.url", "https://fetch.example.test/repo.git")
	gitConfig(t, dir, "--add", "remote.origin.pushurl", "https://push-a.example.test/repo.git")
	gitConfig(t, dir, "--add", "remote.origin.pushurl", "short:repo.git")
	gitConfig(t, dir, "url.https://rewritten.example.test/.insteadOf", "short:")
	gitConfig(t, dir, "url.https://push-rewritten.example.test/.pushInsteadOf", "https://fetch.example.test/")
	gitConfig(t, dir, "remote.mirror.url", "https://mirror.example.test/repo.git")
	gitConfig(t, dir, "branch.main.remote", "origin")

	cases := []struct {
		name   string
		intent RemoteIntent
		want   []string
	}{
		{"fetch default", RemoteIntent{}, []string{"https://fetch.example.test/repo.git"}},
		{"push every pushurl, rewritten", RemoteIntent{Push: true}, []string{"https://push-a.example.test/repo.git", "https://rewritten.example.test/repo.git"}},
		{"named remote", RemoteIntent{Remote: "mirror"}, []string{"https://mirror.example.test/repo.git"}},
		{"push to a URL takes pushInsteadOf", RemoteIntent{Push: true, Remote: "https://fetch.example.test/other.git"}, []string{"https://push-rewritten.example.test/other.git"}},
		{"fetch from a URL takes insteadOf", RemoteIntent{Remote: "short:other.git"}, []string{"https://rewritten.example.test/other.git"}},
		{"fetch all", RemoteIntent{All: true}, []string{"https://mirror.example.test/repo.git", "https://fetch.example.test/repo.git"}},
	}
	for _, c := range cases {
		got, err := ResolveRemoteURLs(t.Context(), dir, c.intent)
		slices.Sort(got)
		want := slices.Clone(c.want)
		slices.Sort(want)
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("%s: got %v, %v; want %v", c.name, got, err, want)
		}
	}

	gitConfig(t, dir, "remote.pushDefault", "mirror")
	if got, err := ResolveRemoteURLs(t.Context(), dir, RemoteIntent{Push: true}); err != nil || !slices.Equal(got, []string{"https://mirror.example.test/repo.git"}) {
		t.Fatalf("remote.pushDefault: got %v, %v", got, err)
	}
	gitConfig(t, dir, "branch.main.pushRemote", "origin")
	if got, err := ResolveRemoteURLs(t.Context(), dir, RemoteIntent{Push: true}); err != nil || len(got) != 2 {
		t.Fatalf("branch pushRemote: got %v, %v", got, err)
	}
	if _, err := ResolveRemoteURLs(t.Context(), dir, RemoteIntent{Remote: "--upload-pack=touch"}); !errors.Is(err, ErrRemoteUnresolved) {
		t.Fatalf("option-shaped remote: %v", err)
	}
}
