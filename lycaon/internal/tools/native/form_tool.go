package native

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
)

// FormTool is a callable tool that runs through its native command form, so both
// routes share one operation, result, receipt and review. The host writes the
// program from typed arguments, which keeps them out of the model's shell quoting.
type FormTool struct {
	Programs *command.ProgramRuntime
	Argv     func(map[string]any) []string
}

// Run executes the form the arguments select.
func (t *FormTool) Run(ctx context.Context, args map[string]any, tc tools.ToolContext) (string, error) {
	if tc.Effects.RequestedArgs == nil {
		tc.Effects.RequestedArgs = command.CanonicalToolArgs(tc, args)
	}
	return t.Programs.Run(ctx, "command", map[string]any{"command": quoteProgram(t.Argv(args))}, tc)
}

func quoteProgram(words []string) string {
	quoted := make([]string, len(words))
	for i, word := range words {
		quoted[i] = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

func stringArg(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func appendValue(words []string, flag, value string) []string {
	if value == "" {
		return words
	}
	return append(words, flag, value)
}

func appendPaths(words []string, args map[string]any) []string {
	words = appendValue(words, "--path", stringArg(args, "path"))
	list, _ := args["paths"].([]any)
	for _, item := range list {
		if path, ok := item.(string); ok {
			words = appendValue(words, "--path", path)
		}
	}
	return words
}

// SummarizeArgv maps the summarize tool's arguments onto its form.
func SummarizeArgv(args map[string]any) []string {
	words := appendPaths([]string{"summarize"}, args)
	words = appendValue(words, "--content", stringArg(args, "content"))
	words = appendValue(words, "--focus", stringArg(args, "task"))
	words = appendValue(words, "--pattern", stringArg(args, "pattern"))
	return appendValue(words, "--cursor", stringArg(args, "cursor"))
}

// SurveyRepoArgv maps the survey_repo tool's arguments onto its form.
func SurveyRepoArgv(args map[string]any) []string {
	words := appendValue([]string{"repo-map"}, "--path", stringArg(args, "path"))
	return appendValue(words, "--bundle", stringArg(args, "bundle"))
}

// CodeRewriteArgv maps the code_rewrite tool's arguments onto its form.
func CodeRewriteArgv(args map[string]any) []string {
	words := []string{"code-rewrite", "--pattern", stringArg(args, "pattern"), "--rewrite", stringArg(args, "rewrite")}
	words = appendPaths(words, args)
	if recursive, _ := args["recursive"].(bool); recursive {
		words = append(words, "--recursive")
	}
	if dryRun, _ := args["dry_run"].(bool); dryRun {
		words = append(words, "--dry-run")
	}
	words = appendValue(words, "--lang", stringArg(args, "lang"))
	return appendValue(words, "--syntax-override-reason", stringArg(args, "syntax_override_reason"))
}

