// Package specsync holds the laws that keep the wire projections in step:
// OpenAPI, Go, the generated TypeScript and the Rust mirrors, and the
// conventions the OpenAPI document follows.
package specsync

import (
	"slices"

	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/laws"
)

// Family is the spec-sync rule table.
var Family = laws.Family{Name: "spec-sync", Rules: slices.Concat(
	conventionRules,
	invariantRules,
	sourceRules,
	routeRules,
	queryRules,
	handlerErrorRules,
	structRules,
	enumRules,
	mirrorRules,
)}

const openAPIPath = "docs/openapi.yaml"

var openAPI = laws.File(openAPIPath)

// spec is docs/openapi.yaml, parsed once per corpus.
func spec(r *laws.Run) *wirespec.Spec {
	return laws.Part(r.Corpus, "openapi", func() (*wirespec.Spec, error) {
		return wirespec.ParseSpec(r.Bytes(openAPIPath))
	})
}

// must ends the check when err is not nil, naming what failed.
func must(r *laws.Run, what string, err error) {
	if err != nil {
		r.Fatalf("%s: %v", what, err)
	}
}

// reportAll records each violation.
func reportAll(r *laws.Run, violations []string) {
	for _, v := range violations {
		r.Report("%s", v)
	}
}
