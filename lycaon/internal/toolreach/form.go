package toolreach

// FormCommand is the program a form tool runs, quoted as the host submits it.
func FormCommand(tool string, args map[string]any) (string, bool) {
	argv, ok := formArgv[tool]
	if !ok {
		return "", false
	}
	return QuoteProgram(argv(args)), true
}

// ProgramBacked reports whether a callable tool evaluates a portable program.
func ProgramBacked(tool string) bool {
	return tool == ProgramTool || tool == "verify" || formArgv[tool] != nil
}

var formArgv = map[string]func(map[string]any) []string{
	"summarize":    SummarizeArgv,
	"survey_repo":  SurveyRepoArgv,
	"code_rewrite": CodeRewriteArgv,
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
