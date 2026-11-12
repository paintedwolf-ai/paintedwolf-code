package gitcmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/toolcontract"
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

// A Git command reports its repository's object store as what it reads, and
// a mutation that fills the work tree from the store reports those files as
// derived from it; other mutations report none.
func TestGitCommandsReportTheirObjectStore(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{".git/objects", ".git/refs", "sub"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var flows []evidence.ContentFlow
	ctx := evidence.WithContentFlowObserver(context.Background(), func(f evidence.ContentFlow) { flows = append(flows, f) })
	ctx = reportRepositoryRead(ctx, filepath.Join(root, "sub"))
	store := filepath.Join(root, ".git")
	if len(flows) != 1 || flows[0].Source != store || len(flows[0].Files) != 0 {
		t.Fatalf("read flows = %+v, want the object store", flows)
	}
	merge := Prepared{command: &command{spec: toolcontract.CommandSpecification{Name: "git merge"}}}
	reportObjectFlow(ctx, merge, []string{"src/a.go"})
	if len(flows) != 2 || !slices.Equal(flows[1].Files, []string{filepath.Join(root, "src", "a.go")}) {
		t.Fatalf("merge flows = %+v", flows)
	}
	add := Prepared{command: &command{spec: toolcontract.CommandSpecification{Name: "git add"}}}
	reportObjectFlow(ctx, add, []string{"src/b.go"})
	if len(flows) != 2 {
		t.Fatalf("git add reported derived files: %+v", flows)
	}
}
