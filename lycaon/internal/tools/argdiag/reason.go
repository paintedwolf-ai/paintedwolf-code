package argdiag

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Reason states the primary finding for logs and the generic refusal copy.
func (diag Diagnosis) Reason(err error, args map[string]any) string {
	received := "; received keys: " + strings.Join(sortedArgKeys(args), ", ")
	switch {
	case diag.JSONText != nil && diag.JSONText.Defect != nil:
		return fmt.Sprintf("property %q is not valid JSON (%s at byte %d)%s",
			diag.JSONText.Path, diag.JSONText.Defect.Kind, diag.JSONText.Defect.Offset+1, received)
	case diag.JSONText != nil:
		return fmt.Sprintf("property %q received a JSON-encoded string; expected a native %s%s",
			diag.JSONText.Path, diag.JSONText.ExpectedType, received)
	case len(diag.Misplaced) > 0:
		m := diag.Misplaced[0]
		return fmt.Sprintf("properties %s under %s belong under %s%s",
			strings.Join(m.Fields, ", "), argPathLabel(m.FoundUnder), argPathLabel(m.BelongsUnder), received)
	case diag.DidYouMean != nil:
		return fmt.Sprintf("unknown property %q; did you mean %q?%s", diag.DidYouMean.Path, diag.DidYouMean.DidYouMean, received)
	case len(diag.ConflictKeys) > 0:
		return fmt.Sprintf("conflicting properties: provide only one of %s%s", strings.Join(diag.ConflictKeys, ", "), received)
	case err == nil:
		return "invalid arguments"
	}
	reason := strings.TrimSpace(err.Error())
	if len(args) == 0 {
		return reason
	}
	return reason + received
}

// SchemaIssue identifies a violated contract without interpreting error prose.
type SchemaIssue struct {
	Path    []string `json:"path"`
	Keyword []string `json:"keyword"`
}

// SchemaIssues lists each leaf violation's instance and keyword paths.
func SchemaIssues(err *jsonschema.ValidationError) []SchemaIssue {
	var out []SchemaIssue
	var visit func(*jsonschema.ValidationError)
	visit = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			out = append(out, SchemaIssue{Path: e.InstanceLocation, Keyword: e.ErrorKind.KeywordPath()})
			return
		}
		for _, cause := range e.Causes {
			visit(cause)
		}
	}
	visit(err)
	return out
}

// argPathLabel names a dotted argument path for diagnostics.
func argPathLabel(path string) string {
	if path == "" {
		return "the root"
	}
	return strconv.Quote(path)
}

func sortedArgKeys(args map[string]any) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for key := range args {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// mutationRecoveryTool suggests write when a span or structural edit fails
// schema validation and write is addressable this turn.
