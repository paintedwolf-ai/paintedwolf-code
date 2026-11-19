package argdiag

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
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

// SchemaIssues returns leaf violations and exact paths of unsupported properties.
func SchemaIssues(err *jsonschema.ValidationError) ([]SchemaIssue, []string) {
	var issues []SchemaIssue
	var unsupported []string
	var visit func(*jsonschema.ValidationError, bool)
	visit = func(e *jsonschema.ValidationError, certain bool) {
		if len(e.Causes) == 0 {
			issues = append(issues, SchemaIssue{Path: e.InstanceLocation, Keyword: e.ErrorKind.KeywordPath()})
		}
		switch violation := e.ErrorKind.(type) {
		case *kind.AnyOf, *kind.OneOf, *kind.Not:
			certain = false
		case *kind.AdditionalProperties:
			if certain {
				unsupported = appendUnsupportedPaths(unsupported, e.InstanceLocation, violation.Properties)
			}
		}
		for _, cause := range e.Causes {
			visit(cause, certain)
		}
	}
	visit(err, true)
	slices.Sort(unsupported)
	return issues, unsupported
}

var pointerEscape = strings.NewReplacer("~", "~0", "/", "~1")

func appendUnsupportedPaths(paths, parent, names []string) []string {
	if len(paths) >= 8 {
		return paths
	}
	var prefix strings.Builder
	for _, segment := range parent {
		prefix.WriteByte('/')
		prefix.WriteString(pointerEscape.Replace(segment))
	}
	for _, name := range slices.Sorted(slices.Values(names)) {
		path := prefix.String() + "/" + pointerEscape.Replace(name)
		encoded := strconv.Quote(path)
		if len(encoded) <= 128 && !slices.Contains(paths, encoded) {
			paths = append(paths, encoded)
			if len(paths) == 8 {
				break
			}
		}
	}
	return paths
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
