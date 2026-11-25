package session

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzledger"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/session/approvalstate"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

func TestSandboxAskAnswerCarriesWhoEndedANonHumanRejection(t *testing.T) {
	for _, tc := range []struct {
		resolvedBy string
		cause      isolation.PathDenialCause
		recorded   string
	}{
		{authzledger.ResolvedByHuman, isolation.PathDenialUserDeclined, ""},
		{authzledger.ResolvedByPolicy, isolation.PathDenialNonHumanRejected, authzledger.ResolvedByPolicy},
		{authzledger.ResolvedByUserStop, isolation.PathDenialNonHumanRejected, authzledger.ResolvedByUserStop},
	} {
		got := sandboxAskAnswer(&hitl.CheckpointResponse{Status: hitl.DecisionStatusRejected, ResolvedBy: tc.resolvedBy})
		if !got.Denied || got.DenialCause != tc.cause || got.ResolvedBy != tc.recorded {
			t.Errorf("%s: resolution = %+v, want cause %s resolved by %q", tc.resolvedBy, got, tc.cause, tc.recorded)
		}
	}
}

// ruleAuthority answers Evaluate and fails the test on any other use.
type ruleAuthority struct {
	hitl.ApprovalGate
	result *hitl.ApprovalResult
	err    error
	calls  int
}

func (a *ruleAuthority) Evaluate(context.Context, hitl.ProposedAction) (*hitl.ApprovalResult, error) {
	a.calls++
	return a.result, a.err
}

type deniedRecorder struct {
	authzledger.Recorder
	denied []authzledger.ToolDeniedRecord
}

func (r *deniedRecorder) AppendToolDenied(_ context.Context, rec authzledger.ToolDeniedRecord) {
	r.denied = append(r.denied, rec)
}

func readBroker(t *testing.T, authority hitl.ApprovalGate, recorder authzledger.Recorder) *WriteRootCheckpointBroker {
	t.Helper()
	return &WriteRootCheckpointBroker{
		Checkpoints:       unusedWriteRootCheckpoints{t: t},
		Runtime:           approvalstate.NewSandboxPathGrantRuntime(),
		ReadRuntime:       approvalstate.NewSandboxPathGrantRuntime(),
		Authority:         authority,
		Authz:             recorder,
		ApprovalsDisabled: func(string) bool { return true },
	}
}

func TestAuthorizeReadAppliesDenyRulesToUnprotectedPathsAndRecordsThem(t *testing.T) {
	matches := []hitl.ApprovalRuleMatch{
		{Category: string(settings.ApprovalCategoryPath), Pattern: "*.txt", Effect: "deny", UnitID: "rules/a"},
		{Category: string(settings.ApprovalCategoryPath), Pattern: "notes/*", Effect: "deny", UnitID: "rules/b"},
	}
	authority := &ruleAuthority{result: &hitl.ApprovalResult{Denied: true, DenyCode: hitl.CodeApprovalRuleDenied, MatchedRules: matches}}
	recorder := &deniedRecorder{}
	path := filepath.Join(t.TempDir(), "notes", "a.txt")

	got, err := readBroker(t, authority, recorder).AuthorizeRead(t.Context(), command.SandboxReadPathAsk{SessionID: "sess", ProposedReadPath: path})
	testutil.FailErr(t, "authorize read", err)
	if got.Authorized || got.DenialCause != isolation.PathDenialApprovalRule || len(got.DeniedRules) != 2 {
		t.Fatalf("result = %+v, want both deny rules", got)
	}
	if len(recorder.denied) != 1 || recorder.denied[0].Tool != "read_path" || recorder.denied[0].RejectCode != hitl.CodeApprovalRuleDenied || len(recorder.denied[0].ApprovalRules) != 2 {
		t.Fatalf("recorded denials = %+v", recorder.denied)
	}
}

func TestAuthorizeReadFailsClosedWhenRulesCannotBeEvaluated(t *testing.T) {
	errRules := errors.New("rules unavailable")
	authority := &ruleAuthority{err: errRules}
	broker := readBroker(t, authority, nil)
	got, err := broker.AuthorizeRead(t.Context(), command.SandboxReadPathAsk{SessionID: "sess", ProposedReadPath: filepath.Join(t.TempDir(), "id_rsa")})
	if !errors.Is(err, errRules) || got.Authorized {
		t.Fatalf("result = %+v, err = %v; want the evaluation error and no grant", got, err)
	}
	if len(broker.ReadRuntime.SessionWriteRoots("sess")) != 0 {
		t.Fatal("an unevaluated read was granted to the chat")
	}
}
