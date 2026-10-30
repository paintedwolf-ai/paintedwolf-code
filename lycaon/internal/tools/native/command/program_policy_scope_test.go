package command_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramPreservesResolvedCwdAcrossOverlappingRootsAndReview(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "current guidance", true: "changed after review"}[changed], func(t *testing.T) {
			preparations, invoked, cwdChecks := 0, 0, 0
			branch := sourcebranch.ForWorktree("outer-branch")
			f := programtest.New(t, programtest.Options{
				Context: func(dir string, tc *tools.ToolContext) {
					tc.Source.Roots = append(tc.Source.Roots, projectroot.RootRef{ID: "inner", Label: "inner", Path: filepath.Join(dir, "nested")})
					tc.Source.ProjectRootBranches = map[string]sourcebranch.ID{"r1": branch, "inner": sourcebranch.ForWorktree("inner-branch")}
				},
				Natives: func(dir string) map[string]command.NativeOperation {
					nested := filepath.Join(dir, "nested")
					testutil.FailErr(t, "create overlapping root", os.Mkdir(nested, 0o700))
					operation := teeOperation(nested)
					prepare := operation.Prepare
					operation.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
						preparations++
						if paths.Cwd().Root.ID != "r1" || request.Invocation.Scope.RootID != "r1" || request.Invocation.Scope.BranchID != branch.String() {
							t.Fatalf("resolved cwd identity changed: cwd=%+v scope=%+v", paths.Cwd(), request.Invocation.Scope)
						}
						prepared, err := prepare(ctx, paths, dispatch, request)
						prepared.Review.PathTargets = []commandprogram.PathTarget{{Path: request.Argv[1].Value, Write: true}}
						invoke := prepared.Invoke
						prepared.Invoke = func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
							invoked++
							return invoke(ctx, tc, streams)
						}
						return prepared, err
					}
					return map[string]command.NativeOperation{"tee": operation}
				},
			})
			nested := filepath.Join(f.Dir, "nested")
			ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
				for _, target := range targets {
					if target.Path == nested && target.Mutation {
						cwdChecks++
						if target.RootID != "r1" || changed && preparations >= 2 {
							return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED"}
						}
					}
					if target.Path == filepath.Join(nested, "target") && target.Mutation && target.RootID != "inner" {
						t.Fatalf("physical operand lost its canonical root: %+v", target)
					}
				}
				return nil
			})
			raw, err := f.CallArgs(t, ctx, map[string]any{"cwd": "@root/nested", "command": "pwd; tee target; external -- /usr/bin/touch external"})
			testutil.FailErr(t, "run overlapping-root program", err)
			var result struct {
				Status int `json:"status"`
			}
			testutil.FailErr(t, "decode overlapping-root result", json.Unmarshal([]byte(raw), &result))
			if changed {
				if result.Status == 0 || invoked != 0 || preparations != 2 {
					t.Fatalf("post-review policy rejection lost its cwd: %s", raw)
				}
			} else if result.Status != 0 || invoked != 1 || cwdChecks < 3 {
				t.Fatalf("resolved cwd was reinterpreted during admission: %s; checks=%d", raw, cwdChecks)
			}
			for _, name := range []string{"target", "external"} {
				_, err := os.Stat(filepath.Join(nested, name))
				if changed && !os.IsNotExist(err) || !changed && err != nil {
					t.Fatalf("unexpected publication for %s: %v", name, err)
				}
			}
		})
	}
}
