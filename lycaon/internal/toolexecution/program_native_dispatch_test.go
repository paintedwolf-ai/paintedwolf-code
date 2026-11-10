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
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/platform"
	"github.com/lycaon/lycaon/internal/progress"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

type nativeProgressFixture struct{ store *progress.MemoryStore }

func (p nativeProgressFixture) ProgramProgressState(ctx context.Context, id string) (progress.PrerequisiteState, error) {
	return progress.PrerequisiteState{SessionID: id, RootSessionID: id, Content: p.store.Get(ctx, id)}, nil
}

type nativeStartFunc func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error

func (f nativeStartFunc) BeforeNativeOwner(ctx context.Context, b toolcontract.BoundOperation, call string, review toolcontract.ReviewedOperationInputs) error {
	return f(ctx, b, call, review)
}

func TestNativeEntryFreezesInputsUsesActualReadDoorAndRecordsInvokedNoMutation(t *testing.T) {
	for _, tool := range []string{"command", "verify"} {
		t.Run(tool, func(t *testing.T) { nativeReadAdmissionFixture(t, "input", tool) })
	}
}

func nativeReadAdmissionFixture(t *testing.T, inputPath, modelTool string) {
	t.Helper()
	parent, tc := frameFixture(t)
	root := tc.ActiveRootPath()
	testutil.FailErr(t, "seed original", os.WriteFile(filepath.Join(root, inputPath), []byte("original bytes"), 0600))
	testutil.FailErr(t, "seed changed", os.WriteFile(filepath.Join(root, "changed"), []byte("wrong bytes"), 0600))
	classes, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	executor := NewExecutor(toolprofiles.NewProfilePolicyEngine(boundary), tools.NewDefaultRegistry(), "fixture")
	// Both real preinvoke anchors must see the submitted command envelope,
	// independently of the raw native path handed to the existing read door.
	var observationRules []*oar.Rule
	for _, anchor := range []string{oar.AnchorToolPreInvoke, oar.AnchorToolHandler} {
		observationRules = append(observationRules, &oar.Rule{ID: anchor, Kind: oar.KindPolicy, Anchor: anchor, Effect: oar.EffectBlock, Enforcement: "enforce", When: `tool != "` + modelTool + `" || paintedwolf.native_operation != "read" || tool_arg_string("command") != "fixture" || tool_arg_string("path") != ""`})
	}
	pipeline := oar.NewGuardPipeline(oar.NewRuleSet(observationRules), nil, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	pipeline.EnableAnchor(oar.AnchorToolHandler)
	executor.Rejections.SetBlockPlane(&toolfeedback.BlockPlane{Pipeline: pipeline})
	definition := nativeDefinition(t, "read")
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
	testutil.FailErr(t, "freeze actual profile", err)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: modelTool, Arguments: map[string]any{"command": "fixture"}}, stageMemory(t, 64<<20))
	testutil.FailErr(t, "freeze frame", err)
	t.Cleanup(frame.Close)
	child := parent
	child.Identity.ExecutionID = "child"
	child.Identity.ParentExecutionID = "parent"
	bound, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
	testutil.FailErr(t, "bind native read", err)
	owner := progress.NewMemoryStore()
	testutil.FailErr(t, "set actual progress", owner.Set(parent.SessionID, "- [ ] Inspect"))
	args := map[string]any{"path": inputPath}
	prepared, invoked := false, false
	var stdout bytes.Buffer
	effect := NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{ExecutionID: "child", OperationID: "read", Argv: []string{"cat", inputPath}}, Arguments: args, Streams: commandprogram.Streams{Output: &stdout}}
	effect.PrepareReviewed = func(ctx context.Context, resolved map[string]any, actual tools.ToolContext) (PreparedNativeEffect, error) {
		args["path"] = "changed"
		path := resolved["path"].(string)
		paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, ".")
		if err != nil {
			return PreparedNativeEffect{}, err
		}
		nativePath, err := paths.Bind(ctx, path, false)
		if err != nil {
			return PreparedNativeEffect{}, err
		}
		prepared = true
		return PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, actual tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
			invoked = true
			file, err := nativePath.Open()
			if err != nil {
				return toolcontract.ProducerResult{}, err
			}
			defer file.Close()
			data, err := io.ReadAll(file)
			if err != nil || string(data) != "original bytes" {
				t.Fatalf("executed unreviewed input %q: %v", data, err)
			}
			if _, err := streams.Output.Write(data); err != nil {
				return toolcontract.ProducerResult{}, err
			}
			return toolcontract.ProducerResult{Status: 0, Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, nil
		}}, nil
	}
	starts := 0
	observer := nativeStartFunc(func(_ context.Context, actual toolcontract.BoundOperation, call string, review toolcontract.ReviewedOperationInputs) error {
		decoded, release, decodeErr := review.Arguments(t.Context(), stageMemory(t, 64<<20))
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		defer release()
		if decoded["path"] != inputPath || len(review.ArgumentsDigest()) != 64 {
			t.Fatalf("unfrozen review: %+v", review)
		}
		if !prepared || invoked || actual.Invocation().Identity.ExecutionID != "child" || call != "actual-model-call" {
			t.Fatal("start marker ordering/identity changed")
		}
		starts++
		return nil
	})
	admission := stageMemory(t, 64<<20)
	result, err := executor.InvokeNative(t.Context(), frame, surface, admission, nativeProgressFixture{owner}, observer, effect, tc)
	testutil.FailErr(t, "invoke actual native read binding", err)
	if !result.OwnerInvoked || !result.OutputPublishable || result.Producer.EffectCertainty != toolcontract.NoEffect || starts != 1 || admission.InUse().State != 0 || stdout.String() != "original bytes" {
		t.Fatalf("lost invoked/read distinction or reservation: %+v held%d", result, admission.InUse().State)
	}
	invoked = false
	denial := errors.New("start record refused")
	result, err = executor.InvokeNative(t.Context(), frame, surface, stageMemory(t, 64<<20), nativeProgressFixture{owner}, nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
		return denial
	}), effect, tc)
	if !errors.Is(err, denial) || result.OwnerInvoked || invoked {
		t.Fatalf("owner dispatched without start record: %+v %v", result, err)
	}
	result, err = executor.InvokeNative(t.Context(), frame, surface, stageMemory(t, 64<<20), nativeProgressFixture{owner}, nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
		frame.Close()
		return nil
	}), effect, tc)
	if err == nil || result.OwnerInvoked || invoked {
		t.Fatalf("owner dispatched after frame closed at marker: %+v %v", result, err)
	}
}

func TestNativeOwnerRevalidatesLiveDenyAfterStartRecorder(t *testing.T) {
	testNativeOwnerPolicyAfterRecorder(t, []string{"allowed", "late-deny", "recovery"})
}

func TestNativeOwnerReviewsNewRequirementAfterStartRecorder(t *testing.T) {
	testNativeOwnerPolicyAfterRecorder(t, []string{"late-ask", "deny-during-review", "recovery"})
}

func TestNativeOwnerRechecksFrameAfterNewRecorderReview(t *testing.T) {
	testNativeOwnerPolicyAfterRecorder(t, []string{"frame-close-during-review"})
}

func testNativeOwnerPolicyAfterRecorder(t *testing.T, phases []string) {
	t.Helper()
	parent, tc := frameFixture(t)
	root := tc.ActiveRootPath()
	testutil.FailErr(t, "seed read target", os.WriteFile(filepath.Join(root, "input"), []byte("authorized bytes"), 0600))
	store, err := settings.NewApprovalStoreAt(filepath.Join(t.TempDir(), "approvals.yaml"))
	testutil.FailErr(t, "create live policy store", err)
	// Quiet discretionary policy must still enforce an explicit user deny.
	neverAsk := true
	testutil.FailErr(t, "configure quiet policy", store.PutGlobal(settings.ApprovalConfig{NeverAsk: &neverAsk}))
	classes, _ := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: "fixture", CommandEffects: classes}})
	gate := settings.NewRuleApprovalGate(store, settings.NoSources())
	executor := NewExecutor(NewApprovalPolicyEngine(toolprofiles.NewProfilePolicyEngine(boundary), gate), tools.NewDefaultRegistry(), "fixture")
	definition := nativeDefinition(t, "read")
	surface, err := executor.Metadata.FreezeProgramSurface(t.Context(), platform.ToolFilter{ProfileID: "fixture"}, []toolcontract.OperationDefinition{definition})
	testutil.FailErr(t, "freeze native surface", err)
	frame, err := FreezeProgramFrame(t.Context(), parent, tc, ProgramModelInvocation{ToolName: "command", Arguments: map[string]any{"command": "cat input"}}, stageMemory(t, 64<<20))
	testutil.FailErr(t, "capture native frame", err)
	t.Cleanup(frame.Close)
	progressStore := progress.NewMemoryStore()
	testutil.FailErr(t, "set current progress", progressStore.Set(parent.SessionID, "- [ ] Inspect"))
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			testutil.FailErr(t, "clear only fixture policy", store.PutProject(root, settings.ApprovalConfig{}))
			testutil.FailErr(t, "reset quiet policy", store.PutGlobal(settings.ApprovalConfig{NeverAsk: &neverAsk}))
			child := parent
			child.Identity.ExecutionID, child.Identity.ParentExecutionID = phase, parent.Identity.ExecutionID
			bound, err := toolcontract.BindOperation(definition, child, "cat", definition.Facts().Commands[0].Effects[0].Mode, nil)
			testutil.FailErr(t, "bind fresh native read", err)
			var output bytes.Buffer
			entered, recorded := false, 0
			asks := 0
			needsReview := phase == "late-ask" || phase == "deny-during-review" || phase == "frame-close-during-review"
			manager := &nativeRecorderReviewManager{invariantCheckpointMgr: invariantCheckpointMgr{status: hitl.DecisionStatusApproved, result: &hitl.DecisionResult{Approved: true}}}
			manager.onRequest = func(req hitl.CheckpointRequest) {
				asks++
				if !needsReview || asks != 1 || recorded != 1 || entered || output.Len() != 0 {
					t.Fatalf("review was repeated, misplaced, or followed an effect: phase=%s asks=%d recorded=%d entered=%t", phase, asks, recorded, entered)
				}
				matched := false
				for _, rule := range req.ApprovalMatches {
					matched = matched || (rule.Category == "tool" && rule.Pattern == "read")
				}
				if !matched {
					t.Fatal("new read requirement was absent from actual review")
				}
				if phase == "frame-close-during-review" {
					frame.Close()
				}
				if phase == "deny-during-review" {
					testutil.FailErr(t, "install deny during approval", store.PutProject(root, settings.ApprovalConfig{Rules: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "read", Effect: settings.ApprovalEffectDeny}}}))
				}
			}
			executor.Approvals.SetCheckpointManager(t.Context(), manager, gate)
			t.Cleanup(func() {
				testutil.FailErr(t, "release fixture egress resolver", executor.Approvals.ReleaseEgressResolver(context.Background()))
			})
			effect := NativeEffect{Operation: bound, Stage: toolcontract.ExecutionStage{ExecutionID: phase, OperationID: "read", Argv: []string{"cat", "input"}}, Arguments: map[string]any{"path": "input"}, Streams: commandprogram.Streams{Output: &output}}
			effect.PrepareReviewed = func(ctx context.Context, args map[string]any, actual tools.ToolContext) (PreparedNativeEffect, error) {
				paths, err := projectpaths.NewProgramPaths(ctx, boundary, actual, ".")
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				path, err := paths.Bind(ctx, args["path"].(string), false)
				if err != nil {
					return PreparedNativeEffect{}, err
				}
				return PreparedNativeEffect{Operation: bound, InvokeOwner: func(ctx context.Context, actual tools.ToolContext, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
					entered = true
					file, err := path.Open()
					if err != nil {
						return toolcontract.ProducerResult{}, err
					}
					defer file.Close()
					_, err = io.Copy(streams.Output, file)
					return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.NoEffect}, err
				}}, nil
			}
			observer := nativeStartFunc(func(context.Context, toolcontract.BoundOperation, string, toolcontract.ReviewedOperationInputs) error {
				recorded++
				if phase == "late-deny" {
					return store.PutProject(root, settings.ApprovalConfig{Rules: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "read", Effect: settings.ApprovalEffectDeny}}})
				}
				if needsReview {
					askEnabled := false
					return store.PutProject(root, settings.ApprovalConfig{NeverAsk: &askEnabled, Rules: []settings.ApprovalRule{{Category: settings.ApprovalCategoryTool, Pattern: "read", Effect: settings.ApprovalEffectAsk}}})
				}
				return nil
			})
			memory := stageMemory(t, 64<<20)
			result, err := executor.InvokeNative(t.Context(), frame, surface, memory, nativeProgressFixture{progressStore}, observer, effect, tc)
			if recorded != 1 || memory.InUse().State != 0 {
				t.Fatalf("fixture missed recorder or retained invocation state: recorded=%d state=%d", recorded, memory.InUse().State)
			}
			if needsReview && asks != 1 {
				t.Fatalf("new requirement did not receive exactly one review: %d", asks)
			}
			if phase == "frame-close-during-review" {
				if err == nil || entered || result.OwnerInvoked || result.OutputPublishable || output.Len() != 0 {
					t.Fatalf("closed frame resumed through approved card: err=%v entered=%t result=%+v output=%q", err, entered, result, output.String())
				}
				return
			}
			if phase == "late-deny" || phase == "deny-during-review" {
				reject := toolrejection.AsToolReject(err)
				if reject == nil || reject.Code != "APPROVAL_RULE_DENIED" || entered || result.OwnerInvoked || result.OutputPublishable || output.Len() != 0 {
					t.Fatalf("late deny reached native owner: err=%v entered=%t result=%+v output=%q", err, entered, result, output.String())
				}
				return
			}
			testutil.FailErr(t, "invoke authorized native read", err)
			if !entered || !result.OwnerInvoked || !result.OutputPublishable || output.String() != "authorized bytes" {
				t.Fatalf("authorized workflow failed: entered=%t result=%+v output=%q", entered, result, output.String())
			}
		})
	}
}

// The callback models a policy change while a human card is outstanding. The
// existing fixture supplies its explicit reply; every card is observed separately.
type nativeRecorderReviewManager struct {
	invariantCheckpointMgr
	onRequest func(hitl.CheckpointRequest)
}

func (m *nativeRecorderReviewManager) RequestCheckpoint(ctx context.Context, req hitl.CheckpointRequest) (*hitl.CheckpointResponse, error) {
	m.onRequest(req)
	return m.invariantCheckpointMgr.RequestCheckpoint(ctx, req)
}

// Preparation can read source bytes before the final producer is invoked.
// A human reply cannot preserve authority withdrawn during that wait.
func TestNativePreparationRechecksAuthorityAfterApproval(t *testing.T) {
	for _, name := range []string{"unchanged", "profile-withdrawn", "branch-unchanged", "branch-withdrawn", "frame-closed", "disclosure-unchanged", "disclosure-frame-closed"} {
		t.Run(name, func(t *testing.T) {
			parent, tc := frameFixture(t)
			disclose := name == "disclosure-unchanged" || name == "disclosure-frame-closed"
			withdraw := name == "profile-withdrawn" || name == "branch-withdrawn" || name == "frame-closed" || name == "disclosure-frame-closed"
			wantAsks := 1
			if disclose {
				wantAsks = 2
			}
			readRoot := tc.ActiveRootPath()
			branch := &preparationBranch{}
			retired := errors.New("fixture branch retired")
			if name == "branch-unchanged" || name == "branch-withdrawn" {
				tc.Source.WorkerBranchRoot = t.TempDir()
				readRoot = tc.Source.WorkerBranchRoot
				tc.Source.BranchWorkspace = branch
			}
			ctx := sandbox.WithSessionID(t.Context(), parent.SessionID)
			testutil.FailErr(t, "seed preparation source", os.WriteFile(filepath.Join(readRoot, "input"), []byte("source bytes"), 0600))
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
			asks, records, preparations := 0, 0, 0
			manager := &nativeRecorderReviewManager{invariantCheckpointMgr: invariantCheckpointMgr{status: hitl.DecisionStatusApproved, result: &hitl.DecisionResult{Approved: true}}}
			manager.onRequest = func(req hitl.CheckpointRequest) {
				asks++
				if asks > wantAsks || preparedBytes.Len() != 0 || records != 0 {
					t.Fatalf("review repeated or followed preparation: asks=%d want=%d preparations=%d bytes=%q records=%d", asks, wantAsks, preparations, preparedBytes.String(), records)
				}
				matched := false
				for _, rule := range req.ApprovalMatches {
					matched = matched || rule.Category == "tool" && rule.Pattern == "read"
				}
				if !matched {
					t.Fatal("read requirement absent from review")
				}
				if name == "frame-closed" || name == "disclosure-frame-closed" && asks == 2 {
					frame.Close()
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
				preparations++
				if disclose && preparations == 1 {
					return PreparedNativeEffect{Operation: bound, DisclosedArgv: &commandprogram.ArgvReview{OperationID: "read", Command: "cat", Operands: []string{"input"}, PathTargets: []commandprogram.PathTarget{{Path: "input"}}}}, nil
				}
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
			if asks != wantAsks || memory.InUse().State != 0 || frameMemory.InUse().State != 0 {
				t.Fatal("review or retention contract failed")
			}
			// The cwd door projects binding errors into its own repair code.
			// Observe the live refusal at its owner rather than requiring a Go
			// error identity to survive that presentation boundary.
			if name == "branch-withdrawn" && branch.refused == 0 {
				t.Fatal("branch authority did not refuse after withdrawal")
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
				if preparations != wantAsks {
					t.Fatalf("preparation passes=%d want=%d", preparations, wantAsks)
				}
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
	refused int
}

func (b *preparationBranch) ValidateMeta(context.Context) error {
	b.checked++
	if b.err != nil {
		b.refused++
	}
	return b.err
}
func (*preparationBranch) EnsureParents(context.Context, string) error { return nil }
