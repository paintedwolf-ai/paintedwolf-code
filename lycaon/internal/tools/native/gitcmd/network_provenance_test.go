package gitcmd

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

// A Git transfer reports its network remotes without credentials, and only
// those: local and loopback repositories are not network sources.
func TestNetworkReadsReportTheirRemotes(t *testing.T) {
	var got []evidence.ExchangeProvenance
	ctx := evidence.WithExchangeProvenanceObserver(context.Background(), func(p evidence.ExchangeProvenance) { got = append(got, p) })
	admitted := admittedRemotes{urls: []string{
		"https://user:token@git.example.test/org/repo.git?x=1#frag",
		"git@ssh.example.test:org/repo.git",
		"/srv/local/repo.git",
		"http://127.0.0.1:9418/repo.git",
	}}
	reportNetworkRead(ctx, admitted, networkRead{wroteStderr: true, trees: []string{"/work/repo"}})
	want := []string{"https://git.example.test/org/repo.git", "ssh://ssh.example.test/org/repo.git"}
	if len(got) != 1 || !got[0].External || !slices.Equal(got[0].ObservedURLs, want) || !got[0].WroteStderr || got[0].WroteStdout || !slices.Equal(got[0].WrittenTrees, []string{"/work/repo"}) {
		t.Fatalf("reported %+v, want remotes %v", got, want)
	}

	got = nil
	reportNetworkRead(ctx, admittedRemotes{urls: []string{"/srv/local/repo.git", "http://localhost:8080/r.git"}}, networkRead{wroteStdout: true})
	if len(got) != 0 {
		t.Fatalf("local transfer reported as a network read: %+v", got)
	}

	if paths := mergedPaths("/work/repo", []string{"src/a.go", " ", "README.md"}); !slices.Equal(paths, []string{filepath.Join("/work/repo", "src", "a.go"), filepath.Join("/work/repo", "README.md")}) {
		t.Fatalf("merged paths = %v", paths)
	}
}
