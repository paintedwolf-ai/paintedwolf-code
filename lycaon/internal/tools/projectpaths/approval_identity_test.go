package projectpaths_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type pathIdentityPolicy struct{ files []string }

func (p *pathIdentityPolicy) Evaluate(_ context.Context, eval platform.PolicyContext) (*platform.PolicyDecision, error) {
	p.files = eval.ResolvedFiles
	return &platform.PolicyDecision{Allowed: true}, nil
}

func TestApprovalIdentityMatchesNativeFileResolution(t *testing.T) {
	for _, count := range []int{1, 2} {
		for _, worker := range []bool{false, true} {
			t.Run(fmt.Sprintf("roots=%d/worker=%t", count, worker), func(t *testing.T) {
				tc, targets := approvalIdentityWorkspace(t, count, worker)
				for i, root := range tc.Roots {
					tc.ActiveRootID = root.ID
					paths := []string{"file[ab].txt", "./file[ab].txt", targets[i], "link.txt"}
					paths = append(paths, "@"+root.Label+"/file[ab].txt")
					for _, path := range paths {
						t.Run(root.Label+"/"+path, func(t *testing.T) {
							assertApprovalPathIdentity(t, tc, path, targets[i])
						})
					}
				}
			})
		}
	}
}

func approvalIdentityWorkspace(t *testing.T, count int, worker bool) (tools.ToolContext, []string) {
	t.Helper()
	tc := tools.ToolContext{ProjectID: "project", SessionID: "session"}
	if worker {
		tc.WorkerBranchRoot = t.TempDir()
		tc.BranchWorkspace = testutil.CompleteBranchWorkspace{}
	}
	var targets []string
	for i := range count {
		root := projectroot.RootRef{ID: fmt.Sprintf("id-%d", i), Label: fmt.Sprintf("folder-%d", i), Path: t.TempDir(), IsPrimary: i == 0}
		tc.Roots = append(tc.Roots, root)
		dir := root.Path
		if worker {
			dir = tc.WorkerBranchRoot
			if count > 1 {
				branchDir, err := projectroot.BranchDirForID(root.ID)
				testutil.FailErr(t, "resolve branch directory", err)
				dir = filepath.Join(dir, branchDir)
			}
		}
		testutil.FailErr(t, "create target directory", os.MkdirAll(dir, 0o755))
		target := filepath.Join(dir, "file[ab].txt")
		testutil.FailErr(t, "write target", os.WriteFile(target, []byte("fixture"), 0o600))
		testutil.FailErr(t, "create target alias", os.Symlink("file[ab].txt", filepath.Join(dir, "link.txt")))
		targets = append(targets, target)
	}
	return tc, targets
}

func assertApprovalPathIdentity(t *testing.T, tc tools.ToolContext, path, target string) {
	t.Helper()
	policy := &pathIdentityPolicy{}
	registry := tools.NewDefaultRegistry()
	const tool = "mcp_fixture_path_identity"
	testutil.FailErr(t, "register path probe", registry.RegisterDefinition(tools.Definition{
		Contract: toolcontract.External("fixture"),
		Meta:     tools.ToolMeta{Name: tool, Description: "Inspect path", ArgsSchema: map[string]any{"type": "object"}},
		Handler: func(ctx context.Context, args map[string]any, received tools.ToolContext) (string, error) {
			modelPath := args["path"].(string)
			read, err := projectpaths.ResolveRead(ctx, nil, received, modelPath)
			testutil.FailErr(t, "resolve read", err)
			write, err := projectpaths.ResolveWrite(ctx, nil, received, modelPath)
			testutil.FailErr(t, "resolve write", err)
			want := fspath.CanonicalPath(target)
			if len(policy.files) != 1 || policy.files[0] != want || fspath.CanonicalPath(read.Abs) != want || fspath.CanonicalPath(write.Abs) != want {
				t.Fatalf("path %q: approval=%v read=%q write=%q want=%q", path, policy.files, read.Abs, write.Abs, want)
			}
			return "ok", nil
		},
	}))
	executor := tools.NewDefaultToolExecutor(policy, registry, "implement")
	_, err := executor.Invoke(t.Context(), tool, map[string]any{"path": path}, tc)
	testutil.FailErr(t, "invoke path probe", err)
}

func TestWorkerCommandDirectoriesResolveRootLabels(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprintf("roots=%d", count), func(t *testing.T) {
			tc, targets := approvalIdentityWorkspace(t, count, true)
			for i, root := range tc.Roots {
				tc.ActiveRootID = root.ID
				for _, path := range []string{".", "@" + root.Label} {
					abs, _, err := projectpaths.CommandCwd(t.Context(), tc, path)
					testutil.FailErr(t, "resolve worker directory", err)
					if abs != filepath.Dir(targets[i]) {
						t.Fatalf("directory %q resolved to %q, want %q", path, abs, filepath.Dir(targets[i]))
					}
				}
			}
		})
	}
}
