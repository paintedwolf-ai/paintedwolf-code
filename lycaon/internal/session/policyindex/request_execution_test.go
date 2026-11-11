package policyindex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/session/history"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFittedReachedRootExecutesThroughNativeHostedAndExternalDispatch(t *testing.T) {
	for _, dispatch := range []string{"native", "hosted", "changed after review"} {
		t.Run(dispatch, func(t *testing.T) {
			m, sess, roots := policyFixture(t)
			first, second := roots.roots[0], roots.roots[1]
			for _, root := range roots.roots {
				writePolicy(t, root.Path, "AGENTS.md", strings.Repeat(root.ID+" root guidance. ", 130))
				writePolicy(t, root.Path, ".paintedwolf/AGENTS.md", strings.Repeat(root.ID+" overlay guidance. ", 130))
			}
			branch := sourcebranch.ForWorktree("reached-branch")
			invoked, preparations := 0, 0
			f := programtest.New(t, programtest.Options{
				Context: func(_ string, tc *tools.ToolContext) {
					tc.Source.Roots = roots.roots
					tc.Source.ActiveRootID = first.ID
					tc.Source.ProjectRootBranches = map[string]sourcebranch.ID{second.ID: branch}
				},
				Natives: func(string) map[string]command.NativeOperation {
					tool := "write"
					if dispatch == "hosted" {
						tool = ""
					}
					return map[string]command.NativeOperation{"tee": {Tool: tool, Prepare: func(ctx context.Context, paths *projectpaths.ProgramPaths, _ commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
						preparations++
						if request.Invocation.Scope.RootID != second.ID || request.Invocation.Scope.BranchID != branch.String() || request.Invocation.Scope.Directory != paths.Cwd().Abs {
							t.Fatalf("reached invocation retained the entry scope: %+v", request.Invocation.Scope)
						}
						spec, _, err := commandprogram.ResolveCommand(request.Argv)
						if err != nil {
							return command.NativePrepared{}, err
						}
						bound, err := (commandprogram.CatalogBinder{}).Bind(spec, request.Argv)
						if err != nil {
							return command.NativePrepared{}, err
						}
						review := bound.Review("write")
						target := filepath.Join(paths.Cwd().Abs, bound.Operands()[0].Value)
						review.PathTargets = []commandprogram.PathTarget{{Path: target, Write: true}}
						if dispatch == "changed after review" && preparations == 2 {
							writePolicy(t, second.Path, "AGENTS.md", "Policy changed during reviewed preparation")
						}
						return command.NativePrepared{Mode: "each-target", Review: review, Invoke: func(context.Context, tools.ToolContext, commandprogram.Streams) (toolcontract.ProducerResult, error) {
							invoked++
							err := os.WriteFile(target, []byte("native effect"), 0o600)
							return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, err
						}}, nil
					}}}
				},
			})
			sess.ID, sess.ProjectID = f.Session, testdbseed.DefaultProjectID
			rows := []api.Message{{ID: "same-intent", Role: api.MessageRoleUser, Origin: api.MessageOriginUser, Content: "Update the library", ContextPinned: true}}
			_, err := m.ResolveCurrent(t.Context(), sess, rows)
			testutil.FailErr(t, "initialize current intent", err)
			_, _, err = m.ObserveScopes(t.Context(), sess.ID, []agentpolicy.Scope{{RootID: second.ID, RootPath: second.Path, Path: "native.txt"}})
			testutil.FailErr(t, "observe reached root", err)
			delivery, err := m.ResolveCurrent(t.Context(), sess, rows)
			testutil.FailErr(t, "resolve reached guidance", err)
			request := modelcall.CompletionRequest{Messages: append(append([]api.Message(nil), rows...), delivery.Messages...)}
			binding, err := m.PrepareBinding(t.Context(), sess, request.Messages)
			testutil.FailErr(t, "prepare request binding", err)
			fitted, err := (&history.Service{}).FitRequest(t.Context(), sess, rows, request, &policyFitBudget{window: 9000}, m)
			testutil.FailErr(t, "fit reached root only", err)
			for _, file := range m.ManifestFor(fitted.Messages).Files {
				if file.RootID != second.ID {
					t.Fatal("fixture retained entry-root mutation coverage")
				}
			}
			check := binding(fitted.Messages)
			raw, err := f.Call(t, tools.WithAgentPolicy(t.Context(), check), "cd @library; pwd; tee native.txt; external -- /usr/bin/touch external.txt")
			testutil.FailErr(t, "execute fitted request", err)
			var result struct {
				Status int `json:"status"`
			}
			testutil.FailErr(t, "decode execution result", json.Unmarshal([]byte(raw), &result))
			if dispatch == "changed after review" {
				if result.Status == 0 || invoked != 0 || preparations != 2 {
					t.Fatalf("reviewed policy change reached publication: %s", raw)
				}
				for _, path := range []string{"native.txt", "external.txt"} {
					if _, err := os.Stat(filepath.Join(second.Path, path)); !os.IsNotExist(err) {
						t.Fatalf("effect reached changed-policy scope: %s, %v", path, err)
					}
				}
				return
			}
			if result.Status != 0 || invoked != 1 {
				t.Fatalf("fitted reached request did not execute: %s", raw)
			}
			for _, path := range []string{"native.txt", "external.txt"} {
				if _, err := os.Stat(filepath.Join(second.Path, path)); err != nil {
					t.Fatalf("missing reached effect %s: %v", path, err)
				}
				if _, err := os.Stat(filepath.Join(first.Path, path)); !os.IsNotExist(err) {
					t.Fatalf("effect leaked into entry root: %s, %v", path, err)
				}
			}
			receipts, err := f.Database.QueryContext(t.Context(), `SELECT native_command, scope_json FROM invocation_program_executions WHERE native_command != 'cd'`)
			testutil.FailErr(t, "read reached attribution", err)
			defer receipts.Close()
			count := 0
			for receipts.Next() {
				var name, encoded string
				testutil.FailErr(t, "scan reached attribution", receipts.Scan(&name, &encoded))
				var scope toolcontract.OperationScope
				testutil.FailErr(t, "decode reached scope", json.Unmarshal([]byte(encoded), &scope))
				if scope.RootID != second.ID || scope.BranchID != branch.String() || scope.Directory != second.Path {
					t.Fatalf("%s receipt retained entry scope: %+v", name, scope)
				}
				count++
			}
			testutil.FailErr(t, "finish reached attribution", receipts.Err())
			if count != 3 {
				t.Fatalf("expected pwd, mutation and external receipts; got %d", count)
			}
		})
	}
}
