package tools

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/syntaxhealth"
	"github.com/lycaon/lycaon/pkg/api"
)

const SyntaxCheckOverriddenCode = "SYNTAX_CHECK_OVERRIDDEN"

// WithSyntaxOverride scopes the invocation's reason to its mutation checks.
func WithSyntaxOverride(ctx context.Context, args map[string]any) (context.Context, error) {
	raw, present := args["syntax_override_reason"]
	if !present {
		return syntaxhealth.WithOverride(ctx, ""), nil
	}
	reason, ok := raw.(string)
	if !ok || strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > 1024 {
		return ctx, toolrejection.RejectInvalidArguments("TOOL_ARGS_INVALID", map[string]any{"field": "syntax_override_reason", "reason": "syntax_override_reason must contain 1 to 1024 nonblank characters"})
	}
	return syntaxhealth.WithOverride(ctx, strings.TrimSpace(reason)), nil
}

// ReportSyntaxOverride records the reason alongside the successful mutation.
func ReportSyntaxOverride(ctx context.Context, tc ToolContext, paths ...string) {
	reason := syntaxhealth.OverrideReason(ctx)
	if reason == "" || tc.Effects.Out == nil || len(paths) == 0 {
		return
	}
	tc.Effects.Out.Facts = tc.Effects.Out.Facts.WithFeedback(SyntaxCheckOverriddenCode, map[string]any{
		"path": paths[0], "paths": paths, "syntax_override_reason": reason,
	}, &api.FeedbackSubject{Kind: "path", ID: paths[0]})
}
