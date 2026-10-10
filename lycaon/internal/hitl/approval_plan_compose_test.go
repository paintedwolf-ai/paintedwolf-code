package hitl

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func capabilityPathReview(t *testing.T, read bool) *PreparedApproval {
	return capabilityPathReviewWithRung(t, read, ApprovalRungChat)
}

func capabilityPathReviewWithRung(t *testing.T, read bool, rung ApprovalOptionRung) *PreparedApproval {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state")
	kind, target, subject := AuthorityWriteRootChat, "write_root", ApprovalSubjectWriteRootSet
	category := ApprovalGrantCategoryWriteRoot
	scope := ApprovalGrantScopeChat
	title := TitleAllowForThisChat
	expires := ExpiresWhenChatDeleted
	if rung == ApprovalRungDay {
		title = TitleAllowFor1Day
		expires = ExpiresIn1DayOrChatDeleted
	}
	if read {
		kind, target, subject, category = AuthorityReadPathChat, "read_path", ApprovalSubjectReadPathSet, ApprovalGrantCategoryReadPath
	}
	action := ProposedAction{
Invocation: ActionInvocation{
Tool: target,
Args: map[string]any{"path": path},
ActionID: "call",
},
Scope: ActionScope{
SessionID: "session",
ProjectID: "project",
},
}
	grant := ApprovalGrant{
		ID: "grant_" + target, Scope: scope, ChatSessionID: "session", ProjectID: "project",
		Predicate: ApprovalGrantPredicate{Category: category, Pattern: path},
		Title:     title, Coverage: path, GrantedAt: time.Now().UTC(),
		ExpiresWhen: expires, ReaskWhen: "a different path is requested",
	}
	delta := ApprovalAuthorityDelta{Kind: kind, Grant: &grant, ChatSessionID: "session"}
	if read {
		delta.ReadPaths = []string{path}
	} else {
		delta.WriteRoots = []string{path}
	}
	decision := &gate.Decision{Primary: api.GateCapabilityWidening, Posture: gate.PostureBalanced,
		Cited: []gate.Fact{{Gate: api.GateCapabilityWidening, Key: "path", Value: path, Source: "host"}}}
	primary, cited, reasons := PresentDecision(decision)
	plan, err := NewApprovalPlan(action, ApprovalStagePreSpawn, ApprovalSubject{Kind: subject, Title: "Allow path access", Targets: []ApprovalTarget{{Kind: target, Label: path}}}, ApprovalPresentation{
		Action: "Access path", Impact: path, Gate: primary, Cited: cited,
	}, reasons, []ApprovalOption{GrantOption(ApprovalGrantOffer{
		ID: grant.ID, Rung: rung, Scope: grant.Scope, Title: grant.Title, Coverage: grant.Coverage,
		ExpiresWhen: grant.ExpiresWhen, ReaskWhen: grant.ReaskWhen, Grant: grant, Authority: []ApprovalAuthorityDelta{delta},
	})}, FaceContext{})
	testutil.FailErr(t, "build path review", err)
	return &PreparedApproval{Request: CheckpointRequest{SessionID: "session", ToolCallID: "call", ProjectID: "project", ProposedAction: &action, ApprovalPlan: plan, Decision: decision}}
}

func TestCapabilityCompositionPreservesIndependentPathAuthority(t *testing.T) {
	write, read := capabilityPathReview(t, false), capabilityPathReview(t, true)
	action := ProposedAction{
Invocation: ActionInvocation{
Tool: "command",
Args: map[string]any{"command": "true"},
ActionID: "call",
},
Scope: ActionScope{
SessionID: "session",
ProjectID: "project",
},
}
	plan, _, err := ComposeCapabilityApprovals(action, []*PreparedApproval{write, read})
	testutil.FailErr(t, "compose path approvals", err)
	option, ok := plan.Option(plan.RecommendedOptionID)
	if !ok || option.Rung != ApprovalRungChat || len(option.Authority) != 2 || len(plan.Subject.Targets) != 2 {
		t.Fatalf("incomplete combined plan: %+v", plan)
	}
	for i, review := range []*PreparedApproval{write, read} {
		want := review.Request.ApprovalPlan.Options[0].Authority[0]
		got := option.Authority[i]
		if got.Kind != want.Kind || !slices.Equal(got.ReadPaths, want.ReadPaths) || !slices.Equal(got.WriteRoots, want.WriteRoots) || got.Grant.ID != want.Grant.ID || got.Grant.Scope != want.Grant.Scope {
			t.Fatalf("composition changed path authority: %+v versus %+v", got, want)
		}
	}
	partial := option
	partial.Authority = partial.Authority[:1]
	if plan.OptionContinues(partial) {
		t.Fatal("write authority alone released the protected read")
	}
}

func TestCapabilityCompositionRefusesForeignInvocation(t *testing.T) {
	review := capabilityPathReview(t, true)
	action := *review.Request.ProposedAction
	action.Invocation.ActionID = "another-call"
	if _, _, err := ComposeCapabilityApprovals(action, []*PreparedApproval{review}); err == nil {
		t.Fatal("composition accepted another invocation")
	}
}

func TestCapabilityCompositionKeepsStrictestContributingPosture(t *testing.T) {
	write, read := capabilityPathReview(t, false), capabilityPathReview(t, true)
	write.Request.Decision.Posture, read.Request.Decision.Posture = gate.PostureLight, gate.PostureLight
	_, decision, err := ComposeCapabilityApprovals(*write.Request.ProposedAction, []*PreparedApproval{write, read})
	testutil.FailErr(t, "compose light reviews", err)
	if decision.Posture != gate.PostureLight {
		t.Fatalf("composition changed light posture to %s", decision.Posture)
	}
	read.Request.Decision.Posture = gate.PostureStrict
	_, decision, err = ComposeCapabilityApprovals(*write.Request.ProposedAction, []*PreparedApproval{write, read})
	testutil.FailErr(t, "compose strict review", err)
	if decision.Posture != gate.PostureStrict {
		t.Fatalf("composition weakened strict posture to %s", decision.Posture)
	}
}

func TestCapabilityCompositionKeepsCheckpointConsequence(t *testing.T) {
	write, read := capabilityPathReview(t, false), capabilityPathReview(t, true)
	write.Request.ConsequenceBand, write.Request.ConsequenceCode = api.ConsequenceBandHighRisk, api.ConsequenceCodeDetection
	plan, _, err := ComposeCapabilityApprovals(*write.Request.ProposedAction, []*PreparedApproval{write, read})
	testutil.FailErr(t, "compose detection review", err)
	if plan.Presentation.ConsequenceBand != string(api.ConsequenceBandHighRisk) || plan.Presentation.ConsequenceCode != string(api.ConsequenceCodeDetection) {
		t.Fatalf("composition lowered checkpoint consequence: %+v", plan.Presentation)
	}
}

func TestCapabilityCompositionRefusesIncompatibleDurations(t *testing.T) {
	write, read := capabilityPathReview(t, false), capabilityPathReview(t, true)
	plan := read.Request.ApprovalPlan
	option := plan.Options[0]
	option.Rung = ApprovalRungDay
	var err error
	read.Request.ApprovalPlan, err = NewApprovalPlan(*read.Request.ProposedAction, plan.Stage, plan.Subject, plan.Presentation, plan.Reasons, []ApprovalOption{option}, FaceContext{})
	testutil.FailErr(t, "build incompatible review", err)
	if _, _, err := ComposeCapabilityApprovals(*write.Request.ProposedAction, []*PreparedApproval{write, read}); !errors.Is(err, ErrNoCommonApprovalDuration) {
		t.Fatalf("incompatible durations were combined: %v", err)
	}
}

func TestCapabilityCompositionRetainsBoundedQuietChoice(t *testing.T) {
	write, read := capabilityPathReviewWithRung(t, false, ApprovalRungDay), capabilityPathReviewWithRung(t, true, ApprovalRungDay)
	plan := write.Request.ApprovalPlan
	quiet := quietOption("session", []QuietSubject{{Key: "outside_roots:write", Label: "writing outside roots"}})
	var err error
	write.Request.ApprovalPlan, err = NewApprovalPlan(*write.Request.ProposedAction, plan.Stage, plan.Subject, plan.Presentation, plan.Reasons,
		append(plan.Options, quiet), FaceContext{})
	testutil.FailErr(t, "build quietable path review", err)
	combined, _, err := ComposeCapabilityApprovals(*write.Request.ProposedAction, []*PreparedApproval{write, read})
	testutil.FailErr(t, "compose quietable reviews", err)
	for _, option := range combined.Options {
		if option.Kind == ApprovalOptionQuiet {
			if !combined.OptionContinues(option) || len(option.Authority) != 3 || option.Authority[2].AskQuiet.Key != "outside_roots:write" {
				t.Fatalf("quiet choice lost its bounded authority or subject: %+v", option)
			}
			return
		}
	}
	t.Fatal("composition removed the quiet choice")
}

func TestCapabilityCompositionSkipsQuietWhenTaskLeasePresent(t *testing.T) {
	write, read := capabilityPathReview(t, false), capabilityPathReview(t, true)
	plan := write.Request.ApprovalPlan
	quiet := quietOption("session", []QuietSubject{{Key: "outside_roots:write", Label: "writing outside roots"}})
	var err error
	write.Request.ApprovalPlan, err = NewApprovalPlan(*write.Request.ProposedAction, plan.Stage, plan.Subject, plan.Presentation, plan.Reasons,
		append(plan.Options, quiet), FaceContext{})
	testutil.FailErr(t, "build path review with quiet candidate", err)
	combined, _, err := ComposeCapabilityApprovals(*write.Request.ProposedAction, []*PreparedApproval{write, read})
	testutil.FailErr(t, "compose path reviews", err)
	for _, option := range combined.Options {
		if option.Kind == ApprovalOptionQuiet {
			t.Fatalf("quiet choice was retained despite enabled chat lease: %+v", option)
		}
	}
}

func TestPreparedApprovalAnswerBindsExactSubject(t *testing.T) {
	review := capabilityPathReview(t, true)
	ctx := WithPreparedApprovalAnswer(t.Context(), []*PreparedApproval{review}, &CheckpointResponse{Status: DecisionStatusApproved, Result: &DecisionResult{Approved: true}})
	if _, ok := PreparedApprovalAnswer(ctx, review.Request.ProposedAction, review.Request.ApprovalPlan); !ok {
		t.Fatal("exact held subject lost its answer")
	}
	changed := *review.Request.ApprovalPlan
	changed.Subject.Targets = []ApprovalTarget{{Kind: "read_path", Label: filepath.Dir(changed.Subject.Targets[0].Label)}}
	if _, ok := PreparedApprovalAnswer(ctx, review.Request.ProposedAction, &changed); ok {
		t.Fatal("a broader read inherited the answer")
	}
	err := PrepareApproval(WithApprovalPreparation(t.Context()), review.Request, nil)
	var prepared *PreparedApproval
	if !errors.As(err, &prepared) || prepared.Request.ApprovalPlan.ID != review.Request.ApprovalPlan.ID {
		t.Fatalf("preparation did not suspend the review: %v", err)
	}
}
