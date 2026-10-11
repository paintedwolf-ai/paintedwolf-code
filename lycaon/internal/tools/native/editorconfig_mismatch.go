package native

import (
	"context"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/editorconfig"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// editorConfigMismatch is one file's declared rules broken by landed text.
type editorConfigMismatch struct {
	path       string
	violations []editorconfig.Violation
}

// checkEditorConfig compares landed text with its .editorconfig chain. An
// unreadable chain reports nothing.
func checkEditorConfig(ctx context.Context, resolved projectpaths.Resolved, st sourceview.Text, written string) (editorConfigMismatch, bool) {
	if resolved.External || strings.TrimSpace(resolved.Root.Path) == "" {
		return editorConfigMismatch{}, false
	}
	props, err := editorconfig.Load(resolved.Root.Path, resolved.ScopeRel)
	if err != nil {
		slog.DebugContext(ctx, "editorconfig unavailable for write check", "path", resolved.DisplayPath, "err", err)
		return editorConfigMismatch{}, false
	}
	change := editorconfig.Change{After: written}
	if st.Disk != nil || st.Editor != nil {
		before := st.Content
		change.Before = &before
	}
	if st.Editor != nil {
		// The open document owns its line endings and holds the text as LF.
		props.EndOfLine = ""
		change.After = strings.ReplaceAll(written, "\r\n", "\n")
	}
	violations, err := editorconfig.Check(props, change)
	if err != nil {
		slog.DebugContext(ctx, "editorconfig write check failed", "path", resolved.DisplayPath, "err", err)
		return editorConfigMismatch{}, false
	}
	if len(violations) == 0 {
		return editorConfigMismatch{}, false
	}
	return editorConfigMismatch{path: resolved.DisplayPath, violations: violations}, true
}

// reportLandedEditorConfig checks and states one landed file.
func reportLandedEditorConfig(ctx context.Context, tctx tools.ToolContext, resolved projectpaths.Resolved, st sourceview.Text, written string) {
	if tctx.Effects.Out == nil {
		return
	}
	if m, ok := checkEditorConfig(ctx, resolved, st, written); ok {
		reportEditorConfigMismatch(tctx, m)
	}
}

// reportEditorConfigMismatch states the mismatch on the invocation, joining
// files already reported by the same call.
func reportEditorConfigMismatch(tctx tools.ToolContext, m editorConfigMismatch) {
	if tctx.Effects.Out == nil || len(m.violations) == 0 {
		return
	}
	code := toolrejection.EditorConfigMismatchCode
	previous := tctx.Effects.Out.Facts.FeedbackFor(code)
	paths, _ := previous.Details["path"].(string)
	rules, _ := previous.Details["editorconfig_rules"].([]string)
	lines, _ := previous.Details["editorconfig_lines"].(string)
	details := map[string]any{
		"path":               joinDetail(paths, ", ", m.path),
		"editorconfig_rules": mergeRules(rules, m.violations),
		"editorconfig_lines": joinDetail(lines, "; ", editorConfigLines(m)),
	}
	subject := previous.Subject
	if subject == nil {
		subject = &api.FeedbackSubject{Kind: "file", ID: m.path}
	}
	tctx.Effects.Out.Facts = replaceFeedback(tctx.Effects.Out.Facts, code, details, subject)
}

func joinDetail(previous, sep, next string) string {
	if previous == "" {
		return next
	}
	return previous + sep + next
}

func mergeRules(rules []string, violations []editorconfig.Violation) []string {
	out := append([]string(nil), rules...)
	for _, v := range violations {
		if rule := string(v.Rule); !slices.Contains(out, rule) {
			out = append(out, rule)
		}
	}
	return out
}

// editorConfigLines renders `path:3,4 rule` entries; `(+N)` counts lines past the cap.
func editorConfigLines(m editorConfigMismatch) string {
	entries := make([]string, 0, len(m.violations))
	for _, v := range m.violations {
		numbers := make([]string, len(v.Lines))
		for i, line := range v.Lines {
			numbers[i] = strconv.Itoa(line)
		}
		entry := m.path + ":" + strings.Join(numbers, ",")
		if extra := v.LineCount - len(v.Lines); extra > 0 {
			entry += " (+" + strconv.Itoa(extra) + ")"
		}
		entries = append(entries, entry+" "+string(v.Rule))
	}
	return strings.Join(entries, "; ")
}

// replaceFeedback keeps raise order and every other code's observation.
func replaceFeedback(facts guidance.ToolResultFacts, code string, details map[string]any, subject *api.FeedbackSubject) guidance.ToolResultFacts {
	if !facts.HasCode(code) {
		return facts.WithFeedback(code, details, subject)
	}
	next := guidance.ToolResultFacts{Outcome: facts.Outcome, Confine: facts.Confine}
	for _, existing := range facts.Codes {
		feedback := facts.FeedbackFor(existing)
		if existing == code {
			next = next.WithFeedback(code, details, subject)
			continue
		}
		next = next.WithFeedback(existing, feedback.Details, feedback.Subject)
	}
	return next
}
