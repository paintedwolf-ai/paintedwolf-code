package tools

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSyntaxOverrideIsExplicitAndInvocationScoped(t *testing.T) {
	ctx, err := WithSyntaxOverride(context.Background(), map[string]any{"syntax_override_reason": "  verified with the language compiler  "})
	testutil.FailErr(t, "decode override", err)
	if syntaxhealth.OverrideReason(ctx) != "verified with the language compiler" {
		t.Fatalf("reason was not preserved: %q", syntaxhealth.OverrideReason(ctx))
	}
	next, err := WithSyntaxOverride(ctx, nil)
	testutil.FailErr(t, "decode next invocation", err)
	if syntaxhealth.OverrideReason(next) != "" {
		t.Fatal("override leaked into another invocation")
	}
	for _, reason := range []any{nil, true, "", " \n\t", strings.Repeat("x", 1025), strings.Repeat(" ", 1024) + "x"} {
		_, err := WithSyntaxOverride(context.Background(), map[string]any{"syntax_override_reason": reason})
		var reject *toolrejection.ToolReject
		if !errors.As(err, &reject) || reject.Code != "TOOL_ARGS_INVALID" {
			t.Fatalf("invalid reason %T accepted: %v", reason, err)
		}
	}
}

func TestSyntaxOverrideReportsReasonAndPaths(t *testing.T) {
	ctx := syntaxhealth.WithOverride(context.Background(), "compiler accepts this source")
	tc := ToolContext{
		Effects: InvocationEffects{Out: &ToolInvocationOut{}},
	}
	ReportSyntaxOverride(ctx, tc, "a.swift", "b.swift")
	if !tc.Effects.Out.Facts.HasCode(SyntaxCheckOverriddenCode) {
		t.Fatal("successful override was not recorded")
	}
	details := tc.Effects.Out.Facts.FeedbackFor(SyntaxCheckOverriddenCode).Details
	if details["syntax_override_reason"] != "compiler accepts this source" || details["path"] != "a.swift" {
		t.Fatalf("override facts incomplete: %+v", details)
	}
}
