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
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestProgramPolicyAdmissionPreservesEarlierOutputAndSkipsUntakenEffects(t *testing.T) {
	f := newLoopFixture(t)
	nested := filepath.Join(f.Dir, "nested")
	testutil.FailErr(t, "create reached directory", os.Mkdir(nested, 0o700))
	var mutationScopes []string
	ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
		for _, endpoint := range targets {
			path, mutation := endpoint.Path, endpoint.Mutation
			if !mutation || path == f.Dir {
				continue
			}
			mutationScopes = append(mutationScopes, path)
			return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED"}
		}
		return nil
	})
	raw, err := f.call(t, ctx, "pwd; false && external -- /bin/echo untaken; cd nested; external -- /usr/bin/touch sentinel")
	testutil.FailErr(t, "settle reached policy barrier", err)
	var result struct {
		Stdout string `json:"stdout"`
		Status int    `json:"status"`
	}
	testutil.FailErr(t, "decode program result", json.Unmarshal([]byte(raw), &result))
	if result.Stdout != f.Dir+"\n" || result.Status == 0 {
		t.Fatalf("program outcome: %s", raw)
	}
	if len(mutationScopes) != 1 || mutationScopes[0] != nested {
		t.Fatalf("reached mutation scopes = %v", mutationScopes)
	}
	if executions := f.executions(t); executions["command"] != "refused;" {
		t.Fatalf("untaken external branch received an execution: %v", executions)
	}
	if _, err := os.Stat(filepath.Join(nested, "sentinel")); !os.IsNotExist(err) {
		t.Fatalf("refused external effect reached the filesystem: %v", err)
	}
}

func TestProgramPolicyChecksReachedTypedOperandsForNativeAndHostedDispatch(t *testing.T) {
	for _, operation := range []string{"write", ""} {
		t.Run("dispatch="+map[string]string{"write": "native", "": "hosted"}[operation], func(t *testing.T) {
			invoked := 0
			f := policyOperandFixture(t, operation, &invoked)
			target := filepath.Join(f.Dir, "nested/target")
			var held []string
			ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
				for _, endpoint := range targets {
					path, mutation := endpoint.Path, endpoint.Mutation
					if mutation && path == target {
						held = append(held, path)
						return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED"}
					}
				}
				return nil
			})
			raw, err := f.Call(t, ctx, "echo kept; false && tee untaken; tee nested/target")
			testutil.FailErr(t, "run typed operand policy barrier", err)
			var result struct {
				Stdout string `json:"stdout"`
				Status int    `json:"status"`
			}
			testutil.FailErr(t, "decode typed operand outcome", json.Unmarshal([]byte(raw), &result))
			if result.Stdout != "kept\n" || result.Status == 0 || len(held) != 1 || invoked != 0 {
				t.Fatalf("operand policy outcome = %s, held=%v invoked=%d", raw, held, invoked)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatalf("held operand reached the filesystem: %v", err)
			}
		})
	}
}

func TestProgramPolicyRechecksOperandsImmediatelyBeforeDispatch(t *testing.T) {
	for _, operation := range []string{"write", ""} {
		t.Run("dispatch="+map[string]string{"write": "native", "": "hosted"}[operation], func(t *testing.T) {
			invoked, checked := 0, 0
			f := policyOperandFixture(t, operation, &invoked)
			target := filepath.Join(f.Dir, "nested/target")
			limit := 3
			if operation == "" {
				limit = 2
			}
			ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
				for _, endpoint := range targets {
					path, mutation := endpoint.Path, endpoint.Mutation
					if mutation && path == target {
						checked++
						if checked >= limit {
							return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED"}
						}
					}
				}
				return nil
			})
			raw, err := f.Call(t, ctx, "tee nested/target")
			testutil.FailErr(t, "run operand policy recheck", err)
			if checked != limit || invoked != 0 {
				t.Fatalf("operand recheck = %d, dispatch calls=%d, result=%s", checked, invoked, raw)
			}
		})
	}
}

func TestProgramPolicyUsesTheReachedNativeDirectoryFrame(t *testing.T) {
	for _, operation := range []string{"write", ""} {
		t.Run("dispatch="+map[string]string{"write": "native", "": "hosted"}[operation], func(t *testing.T) {
			invoked := 0
			f := policyOperandFixture(t, operation, &invoked)
			nested := filepath.Join(f.Dir, "nested")
			var held []string
			ctx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
				for _, endpoint := range targets {
					path, mutation := endpoint.Path, endpoint.Mutation
					if mutation && path == nested {
						held = append(held, path)
						return &toolrejection.ToolReject{Code: "POLICY_CONTEXT_CHANGED"}
					}
				}
				return nil
			})
			raw, err := f.Call(t, ctx, "cd nested; tee target")
			testutil.FailErr(t, "run native directory policy barrier", err)
			if len(held) != 1 || invoked != 0 {
				t.Fatalf("reached directory policy = %v dispatch calls=%d, result=%s", held, invoked, raw)
			}
		})
	}
}

func policyOperandFixture(t *testing.T, tool string, invoked *int) *programtest.Fixture {
	t.Helper()
	return programtest.New(t, programtest.Options{Natives: func(dir string) map[string]command.NativeOperation {
		testutil.FailErr(t, "create operand directory", os.Mkdir(filepath.Join(dir, "nested"), 0o700))
		operation := teeOperation(dir)
		operation.Tool = tool
		prepare := operation.Prepare
		operation.Prepare = func(ctx context.Context, paths *projectpaths.ProgramPaths, dispatch commandprogram.Dispatch, request commandprogram.OperationRequest) (command.NativePrepared, error) {
			prepared, err := prepare(ctx, paths, dispatch, request)
			if err != nil {
				return prepared, err
			}
			prepared.Review.PathTargets = []commandprogram.PathTarget{{Path: request.Argv[1].Value, Write: true}}
			invoke := prepared.Invoke
			prepared.Invoke = func(ctx context.Context, tc tools.ToolContext, streams commandprogram.Streams) (result toolcontract.ProducerResult, err error) {
				(*invoked)++
				return invoke(ctx, tc, streams)
			}
			return prepared, nil
		}
		return map[string]command.NativeOperation{"tee": operation}
	}})
}
