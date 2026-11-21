package specsync

import (
	"go/ast"
	"strings"

	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

var structRules = []invariants.Invariant{
	{Name: "wire-structs-match-openapi-and-types", Anchors: []invariants.Anchor{openAPI, denTypes, apiPkg}, Check: wireStructsMatch},
	{Name: "tagged-structs-reach-the-wire", Anchors: []invariants.Anchor{openAPI, denTypes, apiPkg}, Check: taggedStructsReachTheWire},
	{Name: "untagged-structs-are-accounted", Anchors: []invariants.Anchor{apiPkg}, Check: untaggedStructsAccounted},
	{Name: "embedded-structs-are-accounted", Anchors: []invariants.Anchor{apiPkg}, Check: embeddedStructsAccounted},
	{Name: "struct-exemptions-are-live", Anchors: []invariants.Anchor{apiPkg}, Check: structExemptionsLive},
	{Name: "openapi-objects-have-go-types", Anchors: []invariants.Anchor{openAPI, denTypes, apiPkg}, Exempt: objectsWithoutGoTypes, Check: objectsHaveGoTypes},
	{Name: "exported-wire-fields-have-json-tags", Anchors: []invariants.Anchor{apiPkg}, Check: exportedFieldsHaveJSONTags},
	{Name: "json-tags-are-snake-case", Anchors: []invariants.Anchor{apiPkg}, Check: jsonTagsAreSnakeCase},
}

// objectsWithoutGoTypes are object schemas no pkg/api struct carries.
var objectsWithoutGoTypes = []invariants.Exemption{
	{Key: "ToolApprovalApproveBody", Reason: "a checkpoint resolve variant; it lands in the flat ResolveCheckpointRequest"},
	{Key: "ToolApprovalRejectBody", Reason: "a checkpoint resolve variant; it lands in the flat ResolveCheckpointRequest"},
	{Key: "ContentApplyResolveBody", Reason: "a checkpoint resolve variant; it lands in the flat ResolveCheckpointRequest"},
	{Key: "SummarizeAnchor", Reason: "summarize tool output"},
	{Key: "SummarizeNextAction", Reason: "summarize tool output"},
	{Key: "SummarizeResponse", Reason: "summarize tool output"},
	{Key: "SummarizePack", Reason: "summarize tool output"},
}

// structScan is the struct view of pkg/api, read once per corpus.
func structScan(r *invariants.Run) wirespec.APIStructScan {
	return invariants.Part(r.Corpus, "pkg/api structs", func() (wirespec.APIStructScan, error) {
		return wirespec.ScanAPIStructs(apiFiles(r)), nil
	})
}

func structCatalog(r *invariants.Run) wirespec.StructCatalogResult {
	return invariants.Part(r.Corpus, "wire struct catalog", func() (wirespec.StructCatalogResult, error) {
		return wirespec.StructCatalog(structScan(r), spec(r), r.Text(denTypesPath))
	})
}

// Each tagged pkg/api struct has the same fields, with the same optionality,
// in its OpenAPI schema and its generated types.ts schema.
func wireStructsMatch(r *invariants.Run) {
	catalog := structCatalog(r)
	r.Expect(len(catalog.Specs), "wire structs in pkg/api")
	for _, s := range catalog.Specs {
		if err := wirespec.SyncWireStructFields(spec(r), r.Text(denTypesPath), s); err != nil {
			r.Report("%v", err)
		}
	}
}

// A tagged pkg/api struct reaches both projections or is declared Go-internal
// or a union carrier.
func taggedStructsReachTheWire(r *invariants.Run) {
	for _, gap := range structCatalog(r).Gaps {
		r.Report("%s: %s; publish its schema and regenerate, or declare it in StructGoInternal or StructOneOfCarrier", gap.GoName, gap.Reason())
	}
}

// A struct without JSON tags would marshal PascalCase field names.
func untaggedStructsAccounted(r *invariants.Run) {
	for _, name := range structScan(r).Untagged {
		if !wirespec.StructExempt(name) {
			r.Report("%s: exported pkg/api struct with no JSON-tagged field", name)
		}
	}
}

// The flat scan cannot see promoted fields, so an embedding struct's shape
// would come out short.
func embeddedStructsAccounted(r *invariants.Run) {
	for _, name := range structScan(r).Embedded {
		if !wirespec.StructExempt(name) {
			r.Report("%s: embeds a field; spell the promoted fields out with JSON tags", name)
		}
	}
}

// An exemption names a live struct, gives a reason, and sits in one map.
func structExemptionsLive(r *invariants.Run) {
	scan := structScan(r)
	live := map[string]bool{}
	for name := range scan.Tagged {
		live[name] = true
	}
	for _, names := range [][]string{scan.Untagged, scan.Empty} {
		for _, name := range names {
			live[name] = true
		}
	}
	for name, reason := range wirespec.StructExemptions() {
		switch {
		case !live[name]:
			r.Report("%s: exempted, but pkg/api has no such exported struct", name)
		case reason == "":
			r.Report("%s: exempted without a reason", name)
		}
	}
	for name := range wirespec.StructGoInternal {
		if _, both := wirespec.StructOneOfCarrier[name]; both {
			r.Report("%s: declared both Go-internal and a union carrier", name)
		}
	}
}

func objectsHaveGoTypes(r *invariants.Run) {
	objects, err := spec(r).ObjectSchemas()
	must(r, "list object schemas", err)
	carried := map[string]bool{}
	for _, s := range structCatalog(r).Specs {
		carried[s.OpenAPISchema] = true
	}
	for _, name := range sortedNames(objects) {
		if !carried[name] && !r.Exempt(name) {
			r.Report("object schema %s has no Go wire type", name)
		}
	}
}

// structFields visits each named field of each struct type in pkg/api.
func structFields(r *invariants.Run, visit func(f *invariants.GoFile, typeName string, field *ast.Field, json string)) {
	for _, f := range r.GoFiles(invariants.Production, "pkg/api") {
		ast.Inspect(f.AST, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, field := range st.Fields.List {
				if len(field.Names) == 0 {
					continue // an embedded field marshals through its own type
				}
				tag := ""
				if field.Tag != nil {
					tag = strings.Trim(field.Tag.Value, "`")
				}
				visit(f, ts.Name.Name, field, jsonTag(tag))
			}
			return true
		})
	}
}

// jsonTag is the value of the json key of a struct tag, or "".
func jsonTag(tag string) string {
	_, rest, ok := strings.Cut(tag, `json:"`)
	if !ok {
		return ""
	}
	value, _, _ := strings.Cut(rest, `"`)
	return value
}

// An exported field without a JSON tag would serialize under its Go name.
func exportedFieldsHaveJSONTags(r *invariants.Run) {
	structFields(r, func(f *invariants.GoFile, typeName string, field *ast.Field, json string) {
		for _, name := range field.Names {
			if name.IsExported() && json == "" {
				r.ReportAt(name.Pos(), "%s.%s has no json tag", typeName, name.Name)
			}
		}
	})
}

func jsonTagsAreSnakeCase(r *invariants.Run) {
	structFields(r, func(f *invariants.GoFile, typeName string, field *ast.Field, json string) {
		key, _, _ := strings.Cut(json, ",")
		if key != "" && key != "-" && !rxSnakeCase.MatchString(key) {
			r.ReportAt(field.Pos(), "%s.%s has json key %q, which is not snake_case", typeName, field.Names[0].Name, key)
		}
	})
}
