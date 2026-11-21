package toolexecution

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/isolation"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolfeedback"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestPathDenialsPreserveUserDirection(t *testing.T) {
	decline := func(context.Context, string, map[string]any, tools.ToolContext, string) (PathPreflightResult, error) {
		return PathPreflightResult{
			Denied:       true,
			DenialCause:  isolation.PathDenialUserDeclined,
			UserGuidance: "Keep the existing location.",
		}, nil
	}
	e := func() *Executor {
		e := NewExecutor(nil, nil, "")
		e.Boundary.readPathPreflight = decline
		e.Boundary.writeRootPreflight = decline
		return e
	}()
	tc := tools.ToolContext{
		Invocation: tools.Invocation{Contract: toolcontract.Contract{Capabilities: toolcontract.CapabilityReadPath | toolcontract.CapabilityWriteRoot}},
	}
	for _, field := range []string{"read_path", "write_root"} {
		path := filepath.Join(t.TempDir(), "intended")
		args := map[string]any{"capability_request": map[string]any{field: path}}
		var err error
		if field == "read_path" {
			err = e.Boundary.preflightReadPath(t.Context(), "command", args, &tc)
		} else {
			err = e.Boundary.preflightWriteRoot(t.Context(), "command", args, &tc)
		}
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Data[toolrejection.UserGuidanceKey] != "Keep the existing location." || reject.Data["path"] != path {
			t.Fatalf("denial lost direction or path: %v", err)
		}
	}
}

func TestWriteRootAndReadPathDenialCauseAttribution(t *testing.T) {
	t.Parallel()
	denyRule := &settings.ApprovalRule{
		Category: settings.ApprovalCategoryPath,
		Pattern:  "/restricted/**",
		Effect:   settings.ApprovalEffectDeny,
		Source: settings.ApprovalRuleSource{
			Scope:  settings.ApprovalRuleScopeDevice,
			PackID: "acme/security",
			UnitID: "approvals/rules/deny-restricted-path",
		},
	}

	type testCase struct {
		name           string
		result         PathPreflightResult
		wantWriteCode  string
		wantReadCode   string
		wantSubstring  string
		forbidSubstr   string
		wantRuleFields bool
	}
	cases := []testCase{
		{
			name: "approval_rule_denied",
			result: PathPreflightResult{
				Denied:      true,
				DenialCause: isolation.PathDenialApprovalRule,
				DeniedRule:  denyRule,
			},
			wantWriteCode:  "APPROVAL_RULE_DENIED",
			wantReadCode:   "APPROVAL_RULE_DENIED",
			wantSubstring:  "approvals/rules/deny-restricted-path",
			forbidSubstr:   "Wait for new user direction",
			wantRuleFields: true,
		},
		{
			name: "control_plane_policy_denied",
			result: PathPreflightResult{
				Denied:      true,
				DenialCause: isolation.PathDenialControlPlanePolicy,
			},
			wantWriteCode: isolation.CodeControlPlaneDenied,
			wantReadCode:  isolation.CodeControlPlaneDenied,
			wantSubstring: "app's settings, credentials, tool definitions or policy",
			forbidSubstr:  "Wait for new user direction",
		},
		{
			name: "user_declined",
			result: PathPreflightResult{
				Denied:       true,
				DenialCause:  isolation.PathDenialUserDeclined,
				UserGuidance: "Keep writes inside the project workspace.",
			},
			wantWriteCode: isolation.CodeWriteRootDenied,
			wantReadCode:  isolation.CodeReadPathDenied,
			wantSubstring: "Keep writes inside the project workspace.",
		},
		{
			name: "prior_user_declined",
			result: PathPreflightResult{
				Denied:      true,
				DenialCause: isolation.PathDenialPriorUserDeclined,
			},
			wantWriteCode: isolation.CodeWriteRootDenied,
			wantReadCode:  isolation.CodeReadPathDenied,
			wantSubstring: "Wait for new user direction",
		},
		{
			name: "nonhuman_or_unattributed_rejected",
			result: PathPreflightResult{
				Denied:      true,
				DenialCause: isolation.PathDenialNonHumanRejected,
			},
			wantWriteCode: isolation.CodeApprovalUnavailable,
			wantReadCode:  isolation.CodeApprovalUnavailable,
			wantSubstring: "rejected without a confirmed user decision",
			forbidSubstr:  "Wait for new user direction",
		},
		{
			name: "approval_unavailable_and_unknown_cause",
			result: PathPreflightResult{
				Authorized:  false,
				Denied:      false,
				DenialCause: isolation.PathDenialCause("unexpected_cause"),
			},
			wantWriteCode: isolation.CodeApprovalUnavailable,
			wantReadCode:  isolation.CodeApprovalUnavailable,
			wantSubstring: "approval did not install authority",
			forbidSubstr:  "Wait for new user direction",
		},
	}

	bp := stockBlockPlane(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertPathDenialPreflights(t, bp, denyRule, tc.result, tc.wantWriteCode, tc.wantReadCode, tc.wantSubstring, tc.forbidSubstr, tc.wantRuleFields)
		})
	}
}

func assertPathDenialPreflights(
	t *testing.T,
	bp *toolfeedback.BlockPlane,
	denyRule *settings.ApprovalRule,
	res PathPreflightResult,
	wantWriteCode, wantReadCode, wantSubstring, forbidSubstr string,
	wantRuleFields bool,
) {
	t.Helper()
	exec := NewExecutor(nil, nil, "implement")
	exec.Rejections.SetBlockPlane(bp)
	preflight := func(context.Context, string, map[string]any, tools.ToolContext, string) (PathPreflightResult, error) {
		return res, nil
	}
	exec.Boundary.SetWriteRootPreflight(preflight)
	exec.Boundary.SetReadPathPreflight(preflight)

	toolCtx := tools.ToolContext{
		Identity: tools.InvocationIdentity{Agent: "implement", SessionID: "sess-attr"},
		Invocation: tools.Invocation{
			Contract: toolcontract.Contract{
				Capabilities: toolcontract.CapabilityWriteRoot | toolcontract.CapabilityReadPath,
			},
		},
	}
	target := filepath.Join(t.TempDir(), "restricted", "out")

	checkErr := func(label string, err error, wantCode string) {
		t.Helper()
		refusal, ok := guidance.RefusalFromError(err)
		if !ok || refusal == nil || refusal.Code() != wantCode {
			t.Fatalf("%s refusal = %v, want code %s", label, err, wantCode)
		}
		rendered := refusal.Error() + "\n" + refusal.Copy["what"] + "\n" + refusal.Copy["cause"] + "\n" + refusal.Copy["fix"]
		if wantSubstring != "" && !strings.Contains(rendered, wantSubstring) {
			t.Errorf("%s copy missing %q:\n%s", label, wantSubstring, rendered)
		}
		if forbidSubstr != "" && strings.Contains(rendered, forbidSubstr) {
			t.Errorf("%s copy falsely attributed %q:\n%s", label, forbidSubstr, rendered)
		}
		cause := toolrejection.AsToolReject(err)
		if cause == nil || cause.Code != wantCode {
			t.Fatalf("%s underlying ToolReject = %+v, want code %s", label, cause, wantCode)
		}
		if wantRuleFields {
			for k, want := range map[string]string{
				"rule_unit_id":  denyRule.Source.UnitID,
				"rule_pack_id":  denyRule.Source.PackID,
				"rule_scope":    string(denyRule.Source.Scope),
				"rule_category": string(denyRule.Category),
				"rule_pattern":  denyRule.Pattern,
				"path":          target,
			} {
				if got, _ := cause.Data[k].(string); got != want {
					t.Errorf("%s cause.Data[%q] = %q, want %q", label, k, got, want)
				}
			}
		}
	}

	writeArgs := map[string]any{
		"command":            "cp out " + target,
		"capability_request": map[string]any{"write_root": target},
	}
	tctxWrite := toolCtx
	checkErr("preflightWriteRoot", exec.Boundary.preflightWriteRoot(t.Context(), "command", writeArgs, &tctxWrite), wantWriteCode)

	_, redirectErr := exec.Boundary.ReviewRedirectRoot(t.Context(), toolCtx, ">", filepath.Join(target, "file.txt"), target)
	if !wantRuleFields {
		checkErr("ReviewRedirectRoot", redirectErr, wantWriteCode)
	} else if refusal, ok := guidance.RefusalFromError(redirectErr); !ok || refusal.Code() != wantWriteCode {
		t.Fatalf("ReviewRedirectRoot refusal = %v, want %s", redirectErr, wantWriteCode)
	}

	readArgs := map[string]any{
		"command":            "cat " + target,
		"capability_request": map[string]any{"read_path": target},
	}
	tctxRead := toolCtx
	checkErr("preflightReadPath", exec.Boundary.preflightReadPath(t.Context(), "command", readArgs, &tctxRead), wantReadCode)
}
