package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestOpenAPIObjectsHaveGoTypes(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	schemas, err := wirespec.LoadOpenAPIObjectSchemas(root)
	contractcheck.FailErr(t, "load OpenAPI object schemas", err)
	catalog, err := wirespec.WireStructCatalog(root)
	contractcheck.FailErr(t, "discover wire types", err)
	registered := make(map[string]struct{})
	for _, spec := range catalog.Specs {
		registered[spec.OpenAPISchema] = struct{}{}
	}
	// Schemas with no matching Go DTO: checkpoint resolve variants land in the
	// flat ResolveCheckpointRequest, summarize shapes are tool output.
	skip := map[string]struct{}{
		"ToolApprovalApproveBody": {},
		"ToolApprovalRejectBody":  {},
		"ContentApplyResolveBody": {},
		"SummarizeAnchor":         {},
		"SummarizeNextAction":     {},
		"SummarizeResponse":       {},
		"SummarizePack":           {},
	}
	var violations []string
	for name := range skip {
		if _, ok := schemas[name]; !ok {
			violations = append(violations, name+": skip entry names no OpenAPI object schema")
		}
	}
	for name := range schemas {
		if _, ok := skip[name]; ok {
			continue
		}
		if _, ok := registered[name]; ok {
			continue
		}
		violations = append(violations, name+": no Go wire type")
	}
	contractcheck.FailViolations(t, "OpenAPI object schemas vs discovered Go wire types", violations)
}
