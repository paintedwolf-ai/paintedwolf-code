package tools

import "github.com/santhosh-tekuri/jsonschema/v6"

// SchemaIssue identifies a violated contract without interpreting error prose.
type SchemaIssue struct {
	Path    []string `json:"path"`
	Keyword []string `json:"keyword"`
}

func schemaIssues(err *jsonschema.ValidationError) []SchemaIssue {
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
