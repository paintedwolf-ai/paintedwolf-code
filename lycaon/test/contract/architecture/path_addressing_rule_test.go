package contract

// AC-WR05-01: every path-shaped argument on a none-classified tool or
// operation is either addressed through the session-folder rule, proven by
// its owning tool's unit test, or deliberately not addressed (a key, filter,
// URL path, or reported metadata).

import (
	"sort"
	"testing"

	"github.com/lycaon/lycaon/internal/nativemanifest"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

// pathAddressingPathProps names every schema property that carries a path
// argument. Object-typed properties (page_act's drag `to` element selector)
// are not path arguments.
var pathAddressingPathProps = map[string]struct{}{
	"path": {}, "paths": {}, "src": {}, "dest": {}, "file": {},
	"source": {}, "destination": {}, "path_a": {}, "path_b": {},
	"from": {}, "to": {}, "body_path": {},
}

type pathAddressingArg struct {
	schema string // "tool" or "operation"
	name   string // schema stem
	prop   string // dotted property path inside the schema
}

// pathAddressingWalk collects every path-shaped property, including nested
// objects, arrays of objects, and oneOf/anyOf/allOf variants.
func pathAddressingWalk(schemaKind, name string, props map[string]any, prefix string, out *[]pathAddressingArg) {
	names := make([]string, 0, len(props))
	for n := range props {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		prop, ok := props[n].(map[string]any)
		if !ok {
			continue
		}
		dotted := n
		if prefix != "" {
			dotted = prefix + "." + n
		}
		typ, _ := prop["type"].(string)
		if _, isPath := pathAddressingPathProps[n]; isPath && typ != "object" {
			*out = append(*out, pathAddressingArg{schema: schemaKind, name: name, prop: dotted})
		}
		if sub, ok := prop["properties"].(map[string]any); ok {
			pathAddressingWalk(schemaKind, name, sub, dotted, out)
		}
		if items, ok := prop["items"].(map[string]any); ok {
			if sub, ok := items["properties"].(map[string]any); ok {
				pathAddressingWalk(schemaKind, name, sub, dotted, out)
			}
		}
		for _, key := range []string{"oneOf", "anyOf", "allOf"} {
			variants, ok := prop[key].([]any)
			if !ok {
				continue
			}
			for _, variant := range variants {
				vm, ok := variant.(map[string]any)
				if !ok {
					continue
				}
				if sub, ok := vm["properties"].(map[string]any); ok {
					pathAddressingWalk(schemaKind, name, sub, dotted, out)
				}
			}
		}
	}
}

// noneClassifiedPathArgs maps "schema/property" to whether the argument is
// addressed. Addressed arguments resolve @label paths in their owning tool:
// page routes in internal/tools/native/page (drive_fixtures_test.go),
// render_view dest in internal/tools/native (render_view_dest_test.go) and
// http_request bodies in internal/httpaction (request_body_paths_test.go).
// Every path-shaped argument on a MultiRootNone tool or operation must appear
// here, so a new one fails this test until it is classified.
var noneClassifiedPathArgs = map[string]bool{
	"capture_page/routes.body_path":     true,
	"page_act/actions.routes.body_path": true,
	"page_open/routes.body_path":        true,
	"render_view/dest":                  true,
	"http_request/body_path":            true,
	"http_request/form.path":            true,
	// Not addressed: URL paths, keys, filters, cited receipts, reported metadata.
	"capture_page/path":                false,
	"measure_page/path":                false,
	"page_open/path":                   false,
	"complete_leg/coverage_gaps.paths": false,
	"complete_leg/coverage_review.assessments.cited_evidence.path": false,
	"complete_leg/findings.path":                                   false,
	"fanout_plan/legs.scope.paths":                                 false,
	"handoff_release/paths":                                        false,
	"handoff_reserve/paths":                                        false,
	"preview_overlay/path":                                         false,
	"promote_overlay/resolutions.path":                             false,
	"scan_query/selector.paths":                                    false,
	"scan_query/path":                                              false,
	"state_query/path":                                             false,
	"state_update/path":                                            false,
	"submit_verdict/cited_evidence.path":                           false,
	"task/scope.paths":                                             false,
	"http_request/capture_tokens.from":                             false,
}

func TestNoneClassifiedPathArgsResolverCoverage(t *testing.T) {
	schemas := contractToolSchemas(t)
	found := map[string]bool{}
	manifest, err := nativemanifest.Load()
	contractcheck.FailErr(t, "load native manifest", err)
	for name := range manifest.MultiRootTools() {
		capability, ok := toolcontract.MultiRootOf(name)
		if !ok || capability != toolcontract.MultiRootNone {
			continue
		}
		entry, ok := schemas.Tools[name]
		if !ok {
			t.Errorf("none-classified %q has no schema", name)
			continue
		}
		props, ok := entry.Schema["properties"].(map[string]any)
		if !ok {
			continue
		}
		var args []pathAddressingArg
		pathAddressingWalk("none", name, props, "", &args)
		for _, arg := range args {
			found[arg.name+"/"+arg.prop] = true
		}
	}
	if len(found) == 0 {
		t.Fatal("no none-classified path arguments found; the walk is broken")
	}
	for key := range found {
		if _, ok := noneClassifiedPathArgs[key]; !ok {
			t.Errorf("none-classified path argument %s is not classified in noneClassifiedPathArgs", key)
		}
	}
	for key := range noneClassifiedPathArgs {
		if !found[key] {
			t.Errorf("noneClassifiedPathArgs entry %s matches no none-classified path argument", key)
		}
	}
}
