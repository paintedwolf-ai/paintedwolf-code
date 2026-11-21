// Package specsync holds the invariants that keep the wire projections in step:
// OpenAPI, Go, the generated TypeScript and the Rust mirrors, and the
// conventions the OpenAPI document follows.
package specsync

import (
	"slices"

	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

// Family is the spec-sync rule table.
var Family = invariants.Family{Name: "spec-sync", Invariants: slices.Concat(
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

var openAPI = invariants.File(openAPIPath)

// spec is docs/openapi.yaml, parsed once per corpus.
func spec(r *invariants.Run) *wirespec.Spec {
	return invariants.Part(r.Corpus, "openapi", func() (*wirespec.Spec, error) {
		return wirespec.ParseSpec(r.Bytes(openAPIPath))
	})
}

// must ends the check when err is not nil, naming what failed.
func must(r *invariants.Run, what string, err error) {
	if err != nil {
		r.Fatalf("%s: %v", what, err)
	}
}

// reportAll records each violation.
func reportAll(r *invariants.Run, violations []string) {
	for _, v := range violations {
		r.Report("%s", v)
	}
}
