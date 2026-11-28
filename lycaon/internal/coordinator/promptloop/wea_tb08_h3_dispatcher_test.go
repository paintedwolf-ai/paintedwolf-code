package promptloop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/invocation"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/toolstest"
	"github.com/lycaon/lycaon/pkg/api"
)

// tb08NoopMemory satisfies toolcontract.ReviewStateAdmission for isolated fixture inputs.
type tb08NoopMemory struct{}

func (tb08NoopMemory) ReserveProgramState(context.Context, int64) (func(), error) {
	return func() {}, nil
}

// TestWEATB08_H3_RetainedIdentityLookupDispatcherOutcome reproduces H3's retained-identity
// lookup failure (`internal/invocation/program_start.go:249`: `child program identity: sql: no rows in result set`)
// through the real `promptloop` tool dispatcher (`executeToolCall` + `settleInvocation`) backed by
// the real `invocation.SQLRecorder`, and records the exact typed dispatcher outcome.
//
// Owning contract:
//   - `docs/tools.md#tool-lifecycle`: admitted calls cross one subsystem-owner boundary and settle
//     their receipt once; outcomes are Completed, Rejected (structured policy/contract refusal),
//     Failed (owner could not complete), or Interrupted.
//   - `internal/invocation/program_start.go:241-250` (`validateChildParent`): verifies that the
//     referenced `invocation_programs` + `invocation_program_nodes` row exists before preparing
//     a child execution. A missing retained-identity row is an internal ledger/program-retention
//     integrity fault (owner: `internal/invocation` / command program lifecycle), NOT a caller-fixable
//     argument rejection.
//   - `internal/coordinator/promptloop/tools.go:145-167` (`ownerFailureFromError`): any non-`ToolReject`
//     error returned by a tool owner logs `component=tool_owner` and settles as `TOOL_OWNER_FAILED`
//     with `Class: "owner_error"`, `Retryable: false`, `Outcome: "error"`, `Isolation: nil`.
func TestWEATB08_H3_RetainedIdentityLookupDispatcherOutcome(t *testing.T) {
	ctx := t.Context()
	database := testdbfixture.Open(t, "tb08-h3-dispatcher.db")
	const sessionID = "sess-tb08-h3-identity"
	testdbseed.InsertSession(t, database, sessionID, testdbseed.DefaultProjectID)

	recorder := invocation.NewSQLRecorder(database)
	readContract, ok := toolcontract.Lookup("read")
	if !ok {
		t.Fatal("read contract missing")
	}
	readDef, err := toolcontract.ToolOperation("read", json.RawMessage(`{"type":"object"}`), readContract)
	testutil.FailErr(t, "freeze read operation definition", err)

	var prepareErr error
	reg := toolstest.NewRegistry()
	if err := reg.Register("command", func(runCtx context.Context, _ map[string]any, tc tools.ToolContext) (string, error) {
		if tc.Effects.Out != nil {
			tc.Effects.Out.OwnerInvoked = true
		}
		expanded, release, err := toolcontract.NewReviewedOperationInputs(runCtx, tb08NoopMemory{}, map[string]any{"path": "fixture.txt"})
		if err != nil {
			return "", err
		}
		defer release()
		// Structurally valid ProgramChildStart referencing a program/node identity that was
		// never retained (or was evicted/missing) in `invocation_programs` / `invocation_program_nodes`.
		child := toolcontract.ProgramChildStart{
			ParentReceiptID: tc.Invocation.ID,
			Invocation: toolcontract.OperationInvocation{
				Kind: toolcontract.ProgramChildInvocation,
				Identity: toolcontract.ExecutionIdentity{
					ProgramID:         uuid.NewString(),
					NodeID:            "missing-retained-node",
					ExecutionID:       uuid.NewString(),
					ParentExecutionID: uuid.NewString(),
				},
				ActorID:       "tb08-fixture-actor",
				SessionID:     sessionID,
				Scope:         toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope},
				SourceUnitID:  "submitted",
				Span:          toolcontract.SourceSpan{StartByte: 0, EndByte: 9, Line: 1, Column: 1},
				CatalogDigest: toolcontract.LanguageCatalogDigest(),
			},
			Definition:         readDef,
			ArgumentsReference: "output:tb08-h3-expanded",
			ExpandedInputs:     expanded,
			Canonical:          "read fixture.txt",
		}
		prepareErr = recorder.PrepareChild(runCtx, child)
		return "", prepareErr
	}); err != nil {
		t.Fatalf("register command fixture tool: %v", err)
	}

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{Tools: reg},
		Tools:   ToolsDeps{Invocations: recorder},
	})
	sess := &api.Session{
		ID:        sessionID,
		ProjectID: testdbseed.DefaultProjectID,
		Mode:      api.SessionModeAgent,
		Posture:   api.SessionPostureBuild,
	}
	tc := api.ToolCall{
		ID:   "call-tb08-h3-identity",
		Name: "command",
		Args: map[string]any{"command": "cat fixture.txt"},
	}

	run := loop.Tools.executeToolCall(
		ctx, sess, sessionID, "TB08 H3 retained-identity fixture", nil,
		tc, tools.ToolContext{}, nil, 0, "", api.CoordinatorRunContext{},
	)

	// 1. Verify the underlying error came directly from validateChildParent (program_start.go:249).
	if !errors.Is(prepareErr, sql.ErrNoRows) || !strings.Contains(prepareErr.Error(), "child program identity:") {
		t.Fatalf("PrepareChild error = %v, want wrapped child program identity: sql.ErrNoRows", prepareErr)
	}
	if tr := toolrejection.AsToolReject(prepareErr); tr != nil {
		t.Fatalf("expected raw sql.ErrNoRows error without ToolReject wrapper, got %+v", tr)
	}

	// 2. Verify exact typed dispatcher outcome returned by promptloop.
	if run.reject != nil {
		t.Fatalf("expected nil guidance.Refusal for unstructured owner failure, got %+v", run.reject)
	}
	if run.facts.Outcome != api.ToolResultOutcomeError {
		t.Fatalf("dispatcher outcome = %q, want %q", run.facts.Outcome, api.ToolResultOutcomeError)
	}
	if run.failure == nil {
		t.Fatal("dispatcher returned nil InvocationFailure")
	}
	if run.failure.Code != toolrejection.ToolOwnerFailedCode {
		t.Fatalf("failure.Code = %q, want %q", run.failure.Code, toolrejection.ToolOwnerFailedCode)
	}
	if run.failure.Class != api.FailureClassOwnerError {
		t.Fatalf("failure.Class = %q, want %q", run.failure.Class, api.FailureClassOwnerError)
	}
	if run.failure.Retryable {
		t.Fatalf("failure.Retryable = true, want false")
	}
	if !run.facts.HasCode(toolrejection.ToolOwnerFailedCode) {
		t.Fatalf("facts.Codes = %v, want %q", run.facts.Codes, toolrejection.ToolOwnerFailedCode)
	}

	// 3. Settle the invocation receipt and verify persisted ledger state.
	settled, err := loop.Tools.settleInvocation(ctx, run, api.InvocationStatusError, "error", "", run.failure.OwnerRef)
	testutil.FailErr(t, "settleInvocation", err)
	if settled.receipt == nil {
		t.Fatal("settled receipt is nil")
	}
	if settled.receipt.Status != api.InvocationStatusError ||
		settled.receipt.Failure == nil ||
		settled.receipt.Failure.Code != toolrejection.ToolOwnerFailedCode ||
		settled.receipt.Failure.Class != api.FailureClassOwnerError ||
		settled.receipt.Failure.Retryable ||
		settled.receipt.Isolation != nil {
		t.Fatalf("unexpected settled receipt: %+v", settled.receipt)
	}

	listed, err := recorder.ListSession(ctx, sessionID)
	testutil.FailErr(t, "ListSession", err)
	if len(listed) != 1 || listed[0].Failure == nil || listed[0].Failure.Code != toolrejection.ToolOwnerFailedCode {
		t.Fatalf("persisted ledger receipts = %+v", listed)
	}

	receiptJSON, _ := json.Marshal(map[string]any{
		"schema":              "wea-tb08-h3-dispatcher-v1",
		"symptom":             "missing_retained_identity",
		"source_location":     "lycaon/internal/invocation/program_start.go:249",
		"owning_contract":     "docs/tools.md#tool-lifecycle + internal/invocation (retained program identity)",
		"underlying_error":    prepareErr.Error(),
		"has_tool_reject":     false,
		"dispatcher_outcome":  run.facts.Outcome,
		"receipt_status":      settled.receipt.Status,
		"failure_code":        settled.receipt.Failure.Code,
		"failure_class":       settled.receipt.Failure.Class,
		"failure_retryable":   settled.receipt.Failure.Retryable,
		"isolation_present":   settled.receipt.Isolation != nil,
		"owner_ref":           settled.receipt.Failure.OwnerRef,
		"user_impact":         "Unstructured SQL no-rows error surfaces as generic non-retryable TOOL_OWNER_FAILED (owner_error) and logs component=tool_owner; distinguishes a host/ledger integrity fault from a caller-retryable rejection.",
	})
	t.Logf("WEA_TB08_H3_DISPATCHER_RECEIPT:%s", receiptJSON)
}

// TestWEATB08_H3_BoundedWorkVsMissingIdentityDistinctContracts drives three isolated fixture cases
// through the same promptloop dispatcher + SQLRecorder to keep bounded-work symptoms and
// missing-identity symptoms strictly distinct by owner, contract, and recovery class.
func TestWEATB08_H3_BoundedWorkVsMissingIdentityDistinctContracts(t *testing.T) {
	ctx := t.Context()

	type caseSpec struct {
		name              string
		symptomClass      string
		owner             string
		returnErr         func(runCtx context.Context, recorder *invocation.SQLRecorder, tc tools.ToolContext) error
		wantOutcome       api.ToolResultOutcome
		wantReceiptStatus api.InvocationStatus
		wantCode          string
		wantClass         string
		wantRetryable     bool
		wantTypedReject   bool
	}

	readContract, _ := toolcontract.Lookup("read")
	readDef, err := toolcontract.ToolOperation("read", json.RawMessage(`{"type":"object"}`), readContract)
	testutil.FailErr(t, "freeze read definition", err)

	cases := []caseSpec{
		{
			name:         "missing_identity_host_ledger_fault",
			symptomClass: "missing_identity",
			owner:        "internal/invocation (program_start.go:249)",
			returnErr: func(runCtx context.Context, recorder *invocation.SQLRecorder, tc tools.ToolContext) error {
				expanded, release, err := toolcontract.NewReviewedOperationInputs(runCtx, tb08NoopMemory{}, map[string]any{"path": "a.txt"})
				if err != nil {
					return err
				}
				defer release()
				return recorder.PrepareChild(runCtx, toolcontract.ProgramChildStart{
					ParentReceiptID: tc.Invocation.ID,
					Invocation: toolcontract.OperationInvocation{
						Kind: toolcontract.ProgramChildInvocation,
						Identity: toolcontract.ExecutionIdentity{
							ProgramID:         uuid.NewString(),
							NodeID:            "absent-node",
							ExecutionID:       uuid.NewString(),
							ParentExecutionID: uuid.NewString(),
						},
						ActorID:       "tb08-actor",
						SessionID:     tc.Identity.SessionID,
						Scope:         toolcontract.OperationScope{Kind: toolcontract.SessionOperationScope},
						SourceUnitID:  "submitted",
						Span:          toolcontract.SourceSpan{StartByte: 0, EndByte: 5, Line: 1, Column: 1},
						CatalogDigest: toolcontract.LanguageCatalogDigest(),
					},
					Definition:         readDef,
					ArgumentsReference: "output:tb08-missing",
					ExpandedInputs:     expanded,
					Canonical:          "read a.txt",
				})
			},
			wantOutcome:       api.ToolResultOutcomeError,
			wantReceiptStatus: api.InvocationStatusError,
			wantCode:          toolrejection.ToolOwnerFailedCode,
			wantClass:         api.FailureClassOwnerError,
			wantRetryable:     false,
			wantTypedReject:   false,
		},
		{
			name:         "bounded_work_raw_untranslated_stream_limit",
			symptomClass: "bounded_work_unclassified",
			owner:        "internal/secretmatch (stream_flush.go:43) / captureprojection / command",
			returnErr: func(_ context.Context, _ *invocation.SQLRecorder, _ tools.ToolContext) error {
				return &secretmatch.StreamLimitError{Resource: "screen_work_units", Maximum: 1 << 30, Observed: (1 << 30) + 1}
			},
			wantOutcome:       api.ToolResultOutcomeError,
			wantReceiptStatus: api.InvocationStatusError,
			wantCode:          toolrejection.ToolOwnerFailedCode,
			wantClass:         api.FailureClassOwnerError,
			wantRetryable:     false,
			wantTypedReject:   false,
		},
		{
			name:         "bounded_work_typed_command_limit_exceeded",
			symptomClass: "bounded_work_classified",
			owner:        "docs/tools.md command-language-v1 + internal/tools/native/command",
			returnErr: func(_ context.Context, _ *invocation.SQLRecorder, _ tools.ToolContext) error {
				return toolrejection.RejectInvalidArguments("COMMAND_LIMIT_EXCEEDED", map[string]any{
					"limit_id":      "screen_work_units",
					"resource_unit": "work_units",
					"maximum":       int64(1 << 30),
					"observed":      int64(1<<30) + 1,
				})
			},
			wantOutcome:       api.ToolResultOutcomeError,
			wantReceiptStatus: api.InvocationStatusRejected,
			wantCode:          "COMMAND_LIMIT_EXCEEDED",
			wantClass:         "host_rejection",
			wantRetryable:     false,
			wantTypedReject:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			database := testdbfixture.Open(t, tc.name+".db")
			sessionID := "sess-" + tc.name
			testdbseed.InsertSession(t, database, sessionID, testdbseed.DefaultProjectID)
			recorder := invocation.NewSQLRecorder(database)

			reg := toolstest.NewRegistry()
			if err := reg.Register("command", func(runCtx context.Context, _ map[string]any, toolCtx tools.ToolContext) (string, error) {
				if toolCtx.Effects.Out != nil {
					toolCtx.Effects.Out.OwnerInvoked = true
				}
				return "", tc.returnErr(runCtx, recorder, toolCtx)
			}); err != nil {
				t.Fatalf("register command tool: %v", err)
			}

			loop := NewPromptLoopForTest(PromptLoopDeps{
				Context: ContextDeps{Tools: reg},
				Tools:   ToolsDeps{Invocations: recorder},
			})
			sess := &api.Session{
				ID:        sessionID,
				ProjectID: testdbseed.DefaultProjectID,
				Mode:      api.SessionModeAgent,
				Posture:   api.SessionPostureBuild,
			}
			call := api.ToolCall{
				ID:   "call-" + tc.name,
				Name: "command",
				Args: map[string]any{"command": "cat a.txt"},
			}
			run := loop.Tools.executeToolCall(
				ctx, sess, sessionID, "TB08 H3 distinction fixture", nil,
				call, tools.ToolContext{Identity: tools.InvocationIdentity{SessionID: sessionID}}, nil, 0, "", api.CoordinatorRunContext{},
			)

			if run.facts.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q", run.facts.Outcome, tc.wantOutcome)
			}
			if (run.reject != nil) != tc.wantTypedReject {
				t.Fatalf("typed reject present = %v, want %v", run.reject != nil, tc.wantTypedReject)
			}
			if run.failure == nil {
				t.Fatal("expected non-nil InvocationFailure")
			}
			if run.failure.Code != tc.wantCode {
				t.Fatalf("failure.Code = %q, want %q", run.failure.Code, tc.wantCode)
			}
			if run.failure.Class != tc.wantClass {
				t.Fatalf("failure.Class = %q, want %q", run.failure.Class, tc.wantClass)
			}
			if run.failure.Retryable != tc.wantRetryable {
				t.Fatalf("failure.Retryable = %v, want %v", run.failure.Retryable, tc.wantRetryable)
			}

			settled, err := loop.Tools.settleInvocation(ctx, run, tc.wantReceiptStatus, "error", "", run.failure.OwnerRef)
			testutil.FailErr(t, "settleInvocation", err)
			if settled.receipt.Status != tc.wantReceiptStatus {
				t.Fatalf("receipt.Status = %q, want %q", settled.receipt.Status, tc.wantReceiptStatus)
			}
		})
	}
}
