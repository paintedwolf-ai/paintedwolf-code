package toolexecution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// Preparation can read source bytes before the final producer is invoked.
// A human reply cannot preserve an operation or branch removed during that wait.
func TestNativePreparationRechecksAuthorityAfterApproval(t *testing.T) {
	for _, name := range []string{"unchanged", "profile-withdrawn", "branch-unchanged", "branch-withdrawn"} {
		t.Run(name, func(t *testing.T) {
			parent, tc := frameFixture(t)
			withdraw := name == "profile-withdrawn" || name == "branch-withdrawn"
			branch := &preparationBranch{}
			retired := errors.New("fixture branch retired")
			if name == "branch-unchanged" || name == "branch-withdrawn" {
				tc.Source.WorkerBranchRoot = tc.ActiveRootPath()
				tc.Source.BranchWorkspace = branch
			}
			ctx := sandbox.WithSessionID(t.Context(), parent.SessionID)
			testutil.FailErr(t, "seed preparation source", os.WriteFile(filepath.Join(tc.ActiveRootPath(), "input"), []byte("source bytes"), 0600))
			classes, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
			testutil.FailErr(t, "read effect class", err)
			profile := sandbox.ToolProfile{ID: "fixture", CommandEffects: classes}
			boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{profile})
			boundary.SetProfileSource(func(context.Context, string) []sandbox.ToolProfile { return []sandbox.ToolProfile{profile} })
			store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
			testutil.FailErr(t, "create policy store", err)
			testutil.FailErr(t, "require read review", store.PutProject(tc.ActiveRootPath(), settings.ApprovalConfig{Rules: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "read", Effect: settings.ApprovalEffectAsk}}}))
			gate := settings.NewRuleApprovalGate(store, settings.NoSources())
			executor := NewExecutor(NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), tools.NewDefaultRegistry(), "fixture")
			definition := nativeDefinition(t, "read")
			surface, err := executor.Metadata.FreezeProgramSurface(ctx, platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
			testutil.FailErr(t, "freeze initial profile", err)
			frameMemory := stageMemory(t, 64<<20)
			frame, err := FreezeProgramFrame(ctx, parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat input"}}, frameMemory)
			testutil.FailErr(t, "capture program", err)
			t.Cleanup(frame.Close)
			child := parent
			child.Identity.ExecutionID, child.Identity.ParentExecutionID = "child", parent.Identity.ExecutionID
			bound, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
			testutil.FailErr(t, "bind read", err)
			var preparedBytes, output bytes.Buffer
			asks, records := 0, 0
			manager := &nativeRecorderReviewManager{invariantCheckpointMgr: invariantCheckpointMgr{status: hitl.DecisionStatusApproved, result: &hitl.DecisionResult{Approved: true}}}
			manager.onRequest = func(req hitl.CheckpointRequest) {
				asks++
				if asks != 1 || preparedBytes.Len() != 0 || records != 0 {
					t.Fatal("review repeated or followed preparation")
				}
				matched := false
				for _, rule := range req.ApprovalMatches {
					matched = matched || rule.Category == "tool" && rule.Pattern == "read"
				}
				if !matched {
					t.Fatal("read requirement absent from review")
				}
				if name == "profile-withdrawn" {
					profile.DenyTools = []string{"read"}
				}
				if name == "branch-withdrawn" {
					branch.err = retired
				}
			}
			executor.Approvals.SetCheckpointManager(ctx, manager, gate)
			t.Cleanup(func() {
				testutil.FailErr(t, "release egress resolver", executor.Approvals.ReleaseEgressResolver(context.Background()))
			})
			effect := NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{ExecutionID: "child", OperationID: "read", Argv: []string{"cat", "input"}}, Arguments: map[string]any{"path": "input"}, Streams: commandprogram.Streams{Output: &output}}
			effect.PrepareReviewed = func(ctx context.Context, args map[string]any, actual tools.ToolContext) (PreparedNativeEffect, error) {
				paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, ".")
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				path, err := paths.Bind(ctx, args["path"].(string), false)
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				file, err := path.Open()
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				defer file.Close()
				if _, err := io.Copy(&preparedBytes, file); err != nil {
					return PreparedNativeEffect{}, err
				}
				return PreparedNativeEffect{Operation: bound, InvokeOwner: func(_ context.Context, _ tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					_, err := io.Copy(streams.Output, bytes.NewReader(preparedBytes.Bytes()))
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, err
				}}, nil
			}
			owner := progress.NewMemoryStore()
			testutil.FailErr(t, "seed progress", owner.Set(parent.SessionID, "- [ ] Inspect"))
			memory := stageMemory(t, 64<<20)
			result, err := executor.InvokeNative(ctx, frame, surface, memory, nativeProgressFixture{owner}, nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
				records++
				return nil
			}), effect, tc)
			frame.Close()
			if asks != 1 || memory.InUse().State != 0 || frameMemory.InUse().State != 0 {
				t.Fatal("review or retention contract failed")
			}
			if name == "branch-withdrawn" && !errors.Is(err, retired) {
				t.Fatalf("lost branch retirement refusal: %v", err)
			}
			if (name == "branch-unchanged" || name == "branch-withdrawn") && branch.checked == 0 {
				t.Fatal("worker branch authority was not consulted")
			}
			if withdraw {
				if err == nil || preparedBytes.Len() != 0 || output.Len() != 0 || records != 0 || result.OwnerInvoked {
					t.Fatalf("withdrawn operation read source: err=%v prepared=%q output=%q records=%d owner=%t", err, preparedBytes.String(), output.String(), records, result.OwnerInvoked)
				}
			} else {
				testutil.FailErr(t, "resume approved preparation", err)
				if preparedBytes.String() != "source bytes" || output.String() != "source bytes" || records != 1 || !result.OwnerInvoked || !result.OutputPublishable {
					t.Fatalf("authorized preparation failed: prepared=%q output=%q records=%d result=%+v", preparedBytes.String(), output.String(), records, result)
				}
			}
		})
	}
}

// The branch owner supplies live metadata validation; the dispatch and file
// doors remain real, so this port changes authority without deleting the file.
type preparationBranch struct {
	err     error
	checked int
}

func (b *preparationBranch) ValidateMeta(context.Context) error        { b.checked++; return b.err }
func (*preparationBranch) EnsureParents(context.Context, string) error { return nil }
