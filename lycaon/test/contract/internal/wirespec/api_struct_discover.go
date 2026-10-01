package wirespec

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/testutil"
)

// wireField is one JSON field on a wire DTO with optionality.
type wireField struct {
	Name     string
	Optional bool
}

// wireStructSpec names a Go struct and its wire-export aliases.
type wireStructSpec struct {
	GoName        string
	OpenAPISchema string
	TSInterface   string
	Fields        []wireField
}

// wireStructGap is an exported pkg/api struct with JSON tags that reaches
// neither wire projection and claims no exemption.
type wireStructGap struct {
	GoName        string
	OpenAPISchema string
	TSInterface   string
	HasOpenAPI    bool
	HasTS         bool
}

// Reason renders the missing projections for a gap failure line.
func (g wireStructGap) Reason() string {
	switch {
	case !g.HasOpenAPI && !g.HasTS:
		return fmt.Sprintf("no OpenAPI schema %q and no types.ts schema %q", g.OpenAPISchema, g.TSInterface)
	case !g.HasOpenAPI:
		return fmt.Sprintf("no OpenAPI schema %q", g.OpenAPISchema)
	default:
		return fmt.Sprintf("no types.ts schema %q", g.TSInterface)
	}
}

// wireStructCatalogResult separates covered structs from missing wire projections.
type wireStructCatalogResult struct {
	Specs []wireStructSpec
	Gaps  []wireStructGap
}

func dtoSyncAliases() map[string]string {
	return map[string]string{
		"PromptReferencePathFilePart":   "PromptReferencePathFile",
		"PromptReferencePathFolderPart": "PromptReferencePathFolder",
		"PromptReferenceArtifactPart":   "PromptReferenceArtifact",
		"PromptReferenceSearchHitPart":  "PromptReferenceSearchHit",
		"EventEnvelope":                 "EventEnvelopeBase",
		"ErrorResponse":                 "Error",
		"ModelRefDTO":                   "ModelRef",
		"AgentPoolDTO":                  "AgentPool",
		"PresentedFact":                 "ApprovalCitedFact",
	}
}

// apiStructScan is the AST view of exported pkg/api struct types.
type apiStructScan struct {
	// Tagged holds structs with at least one JSON-tagged field, by Go name.
	Tagged map[string][]wireField
	// Untagged names structs whose fields carry no JSON tags at all.
	Untagged []string
	// Empty names field-less structs: published request bodies that carry {}.
	Empty []string
	// Embedded names structs with a promoted field the flat scan cannot resolve.
	Embedded []string
}

func discoverAPIStructFields(repoRoot string) (apiStructScan, error) {
	apiDir := filepath.Join(repoRoot, "lycaon", "pkg", "api")
	entries, err := os.ReadDir(apiDir)
	if err != nil {
		return apiStructScan{}, err
	}
	fset := token.NewFileSet()
	scan := apiStructScan{Tagged: make(map[string][]wireField)}
	for _, ent := range entries {
		name := ent.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(apiDir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return apiStructScan{}, fmt.Errorf("parse %s: %w", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || !ts.Name.IsExported() {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			var fields []wireField
			embedded := false
			for _, f := range st.Fields.List {
				if len(f.Names) == 0 {
					embedded = true
					continue
				}
				tag := ""
				if f.Tag != nil {
					tag = strings.Trim(f.Tag.Value, "`")
				}
				jsonName, optional, keep := parseJSONTag(tag)
				if !keep {
					continue
				}
				fields = append(fields, wireField{Name: jsonName, Optional: optional})
			}
			if embedded {
				scan.Embedded = append(scan.Embedded, ts.Name.Name)
			}
			switch {
			case len(fields) > 0:
				scan.Tagged[ts.Name.Name] = fields
			case len(st.Fields.List) == 0:
				// An empty object marshals as {}: no field name can leak.
				scan.Empty = append(scan.Empty, ts.Name.Name)
			default:
				scan.Untagged = append(scan.Untagged, ts.Name.Name)
			}
			return true
		})
	}
	for k := range scan.Tagged {
		sort.Slice(scan.Tagged[k], func(i, j int) bool { return scan.Tagged[k][i].Name < scan.Tagged[k][j].Name })
	}
	sort.Strings(scan.Untagged)
	sort.Strings(scan.Empty)
	sort.Strings(scan.Embedded)
	return scan, nil
}

func parseJSONTag(tag string) (name string, optional bool, keep bool) {
	if tag == "" {
		return "", false, false
	}
	const key = `json:"`
	idx := strings.Index(tag, key)
	if idx < 0 {
		return "", false, false
	}
	rest := tag[idx+len(key):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return "", false, false
	}
	raw := rest[:end]
	if raw == "-" {
		return "", false, false
	}
	name, opts, _ := strings.Cut(raw, ",")
	if strings.TrimSpace(name) == "" {
		return "", false, false
	}
	optional = strings.Contains(opts, "omitempty")
	return name, optional, true
}

func WireStructCatalog(repoRoot string) (wireStructCatalogResult, error) {
	scan, err := CachedDiscoverAPIStructFields(repoRoot)
	if err != nil {
		return wireStructCatalogResult{}, err
	}
	openAPIName := dtoSyncAliases()
	openAPISchemas, err := LoadOpenAPIObjectSchemas(repoRoot)
	if err != nil {
		return wireStructCatalogResult{}, err
	}
	tsContent, err := cachedTSContent(repoRoot)
	if err != nil {
		return wireStructCatalogResult{}, err
	}

	// A field-less published request is tri-synced like any other: its schema
	// must stay empty on all three sides.
	discovered := make(map[string][]wireField, len(scan.Tagged)+len(scan.Empty))
	for goName, fields := range scan.Tagged {
		discovered[goName] = fields
	}
	for _, goName := range scan.Empty {
		discovered[goName] = nil
	}

	var out wireStructCatalogResult
	for goName, fields := range discovered {
		if StructExempt(goName) {
			continue
		}
		oapiName := goName
		if alt, ok := openAPIName[goName]; ok {
			oapiName = alt
		}
		ifName := oapiName

		_, hasOpenAPI := openAPISchemas[oapiName]
		hasTS := tsInterfaceExists(tsContent, ifName)
		if !hasOpenAPI || !hasTS {
			out.Gaps = append(out.Gaps, wireStructGap{
				GoName:        goName,
				OpenAPISchema: oapiName,
				TSInterface:   ifName,
				HasOpenAPI:    hasOpenAPI,
				HasTS:         hasTS,
			})
			continue
		}
		out.Specs = append(out.Specs, wireStructSpec{
			GoName:        goName,
			OpenAPISchema: oapiName,
			TSInterface:   ifName,
			Fields:        fields,
		})
	}
	sort.Slice(out.Specs, func(i, j int) bool { return out.Specs[i].GoName < out.Specs[j].GoName })
	sort.Slice(out.Gaps, func(i, j int) bool { return out.Gaps[i].GoName < out.Gaps[j].GoName })
	return out, nil
}

func tsInterfaceExists(content, name string) bool {
	return tsGeneratedSchemaExists(content, name)
}

func SyncWireStructFields(root string, spec wireStructSpec) error {
	goFields := spec.Fields

	openAPIFields, err := loadOpenAPISchemaFieldSpecs(root, spec.OpenAPISchema)
	if err != nil {
		return fmt.Errorf("%s: %w", spec.GoName, err)
	}
	if diff := wireFieldDiff(spec.GoName+" Go vs OpenAPI "+spec.OpenAPISchema, goFields, openAPIFields); diff != "" {
		return fmt.Errorf("%s\nfix: align pkg/api %s JSON tags with docs/openapi.yaml schema %q",
			diff, spec.GoName, spec.OpenAPISchema)
	}

	tsFields, err := parseTSInterfaceFieldSpecs(root, spec.TSInterface)
	if err != nil {
		return fmt.Errorf("%s: %w", spec.GoName, err)
	}
	if diff := wireFieldDiff(spec.GoName+" Go vs TS "+spec.TSInterface, goFields, tsFields); diff != "" {
		return fmt.Errorf("%s\nfix: align pkg/api %s JSON tags with generated lycaon-den/src/api/types.ts schema %q",
			diff, spec.GoName, spec.TSInterface)
	}
	return nil
}

func loadOpenAPISchemaFieldSpecs(repoRoot, schema string) ([]wireField, error) {
	props, err := loadOpenAPISchemaPropertySpecs(repoRoot, schema)
	if err != nil {
		return nil, err
	}
	var out []wireField
	for name, optional := range props {
		out = append(out, wireField{Name: name, Optional: optional})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func loadOpenAPISchemaPropertySpecs(repoRoot, schema string) (map[string]bool, error) {
	return CachedOpenAPIPropertySpecs(repoRoot, schema)
}

func parseTSInterfaceFieldSpecs(repoRoot, name string) ([]wireField, error) {
	content, err := cachedTSContent(repoRoot)
	if err != nil {
		return nil, err
	}
	fields, err := parseGeneratedSchemaFieldSpecs(content, name)
	if err != nil {
		return nil, err
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields, nil
}

func wireFieldDiff(label string, want, got []wireField) string {
	wantByName := make(map[string]wireField, len(want))
	for _, f := range want {
		wantByName[f.Name] = f
	}
	var onlyWant, onlyGot, optMismatches []string
	for _, f := range got {
		w, ok := wantByName[f.Name]
		if !ok {
			onlyGot = append(onlyGot, f.Name)
			continue
		}
		if w.Optional != f.Optional {
			optMismatches = append(optMismatches, fmt.Sprintf("%s (want optional=%v got optional=%v)", f.Name, w.Optional, f.Optional))
		}
		delete(wantByName, f.Name)
	}
	for name := range wantByName {
		onlyWant = append(onlyWant, name)
	}
	if len(onlyWant) == 0 && len(onlyGot) == 0 && len(optMismatches) == 0 {
		return ""
	}
	sort.Strings(onlyWant)
	sort.Strings(onlyGot)
	sort.Strings(optMismatches)
	var b strings.Builder
	if len(onlyWant) > 0 || len(onlyGot) > 0 {
		b.WriteString(testutil.FormatSetDiff(label, wireFieldNames(want), wireFieldNames(got)))
	} else {
		b.WriteString(label + ": optionality mismatch")
	}
	if len(onlyWant) > 0 {
		b.WriteString("\n  only in expected: ")
		b.WriteString(strings.Join(onlyWant, ", "))
	}
	if len(onlyGot) > 0 {
		b.WriteString("\n  only in actual: ")
		b.WriteString(strings.Join(onlyGot, ", "))
	}
	if len(optMismatches) > 0 {
		b.WriteString("\n  optionality mismatches: ")
		b.WriteString(strings.Join(optMismatches, "; "))
	}
	return b.String()
}

func wireFieldNames(fields []wireField) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f.Name
	}
	return out
}

// StructGoInternal records why exported structs have no wire projection.
var StructGoInternal = map[string]string{
	"BoardSnapshot":         "board assembly shape; BoardView is what serializes",
	"BoardHostSlice":        "BoardSnapshot member; not carried by BoardView",
	"BoardOrientationRoot":  "BoardSnapshot member; not carried by BoardView",
	"BoardReservationEntry": "BoardSnapshot member; rendered into pack_board text, never serialized",

	"ParseResult":              "LLM structured-output landing shape",
	"ExtractResult":            "LLM structured-output landing shape",
	"DelegationPlan":           "LLM structured-output landing shape",
	"DelegationLegPlan":        "DelegationPlan member",
	"DecompositionConstraints": "DelegationPlan member",

	"SpawnChildRequest":         "session store CreateChild argument; tools spawn child sessions, not an endpoint",
	"TranscriptPageQuery":       "query-string carrier; parameters are declared in docs/openapi/components/parameters.yaml",
	"ModelReasoning":            "Message carries it as json:\"-\"",
	"DetectionPackRejectedRule": "pack loader diagnostic; DetectionPack carries the counts",

	"PromoteOverlayInput":     "promote_overlay tool arguments, not an HTTP body",
	"WorkerPromoteResolution": "promote_overlay tool argument member",
	"WorkerTaskCharter":       "task tool argument member",
	"WorkerCancelResult":      "cancel tool response; Den renders worker events instead",
	"OverlayRejectOutcome":    "reject_overlay tool response; Den renders worker events instead",
}

// Union carriers use variant-level checks because their fields span multiple schemas.
var StructOneOfCarrier = map[string]string{
	"SourceComparisonSelector": "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",
	"SourceTreeCommand":        "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",
	"SourceView":               "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",
	"SourceViewCreate":         "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",
	"SourceViewFrame":          "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",
	"SourceViewLocation":       "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",
	"SourceViewSearchPage":     "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",
	"SourceViewUpdate":         "source presentation union; generated variants are checked individually and custom JSON enforces the discriminator",

	"PromptReferencePart":      "prompt reference union; PromptReference* variants are tri-synced individually",
	"ResolveCheckpointRequest": "checkpoint resolve union; the ResolveCheckpointRequest oneOf variants are the discriminated schemas",
	"EventScope":               "event scope union; variants are inline oneOf members keyed by EventScopeKind",
}

func StructExempt(goName string) bool {
	if _, ok := StructGoInternal[goName]; ok {
		return true
	}
	_, ok := StructOneOfCarrier[goName]
	return ok
}

func StructExemptions() map[string]string {
	out := make(map[string]string, len(StructGoInternal)+len(StructOneOfCarrier))
	for name, reason := range StructGoInternal {
		out[name] = reason
	}
	for name, reason := range StructOneOfCarrier {
		out[name] = reason
	}
	return out
}
