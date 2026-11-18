package specsync

import (
	"go/constant"
	"go/types"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/lycaon/lycaon/internal/coordinator/loopwake"
	scancatalog "github.com/lycaon/lycaon/internal/scan/catalog"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
	"github.com/lycaon/lycaon/test/contract/invariants"
)

const (
	toolSchemasDir    = "lycaon/config/packs/painted-wolf/platform/tools/schemas"
	scannerSlotsPath  = "lycaon-den/src/settings/extensions/scanners-catalog-model.ts"
	waitLabelsPath    = "lycaon-den/src/chat/session/thinking-activity-label.ts"
	waitToolSchema    = toolSchemasDir + "/wait.yaml"
	redactionSourceIn = "internal/secretmatch"
)

var enumRules = []invariants.Invariant{
	{Name: "enum-registries-are-exhaustive", Anchors: []invariants.Anchor{apiPkg}, Check: enumRegistriesAreExhaustive},
	{Name: "string-enums-match-openapi-and-types", Anchors: []invariants.Anchor{openAPI, denTypes, apiPkg}, Check: stringEnumsMatch},
	{Name: "redaction-sources-match-the-wire", Anchors: []invariants.Anchor{invariants.Decl(redactionSourceIn, "RedactionSource"), invariants.Decl("pkg/api", "RedactionSource")}, Check: redactionSourcesMatchTheWire},
	{Name: "scanner-slots-match-go", Anchors: []invariants.Anchor{invariants.File(scannerSlotsPath)}, Check: scannerSlotsMatchGo},
	{Name: "wait-triggers-agree", Anchors: []invariants.Anchor{invariants.File(waitToolSchema), invariants.File(waitLabelsPath)}, Check: waitTriggersAgree},
	{Name: "verdict-coverage-matches-the-wire", Anchors: []invariants.Anchor{openAPI, invariants.File(toolSchemasDir + "/submit_verdict.yaml")}, Check: verdictCoverageMatchesTheWire},
}

// apiEnums are the string enums pkg/api declares, read once per corpus.
func apiEnums(r *invariants.Run) map[string][]string {
	return invariants.Part(r.Corpus, "pkg/api enums", func() (map[string][]string, error) {
		return wirespec.APIStringEnums(apiFiles(r)), nil
	})
}

// Every constant of a registered enum type appears in its All… registry.
func enumRegistriesAreExhaustive(r *invariants.Run) {
	declared := apiEnums(r)
	for name, registry := range map[string][]string{
		"CheckpointStatus":      wirespec.GoEnumValues(api.AllCheckpointStatuses()...),
		"CompletionReportScope": wirespec.GoEnumValues(api.AllCompletionReportScopes()...),
		"DraftStatus":           wirespec.GoEnumValues(api.AllDraftStatuses()...),
		"MessageKind":           wirespec.GoEnumValues(api.AllMessageKinds()...),
		"MessageLiveStatus":     wirespec.GoEnumValues(api.AllMessageLiveStatuses()...),
		"MessageVisibility":     wirespec.GoEnumValues(api.AllMessageVisibilities()...),
		"NavigationEntryKind":   wirespec.GoEnumValues(api.AllNavigationEntryKinds()...),
		"WorkerStatus":          wirespec.GoEnumValues(api.AllWorkerStatuses()...),
		"WorkerSummaryStatus":   wirespec.GoEnumValues(api.AllWorkerSummaryStatuses()...),
		"WorkflowBoundaryKind":  wirespec.GoEnumValues(api.AllWorkflowBoundaryKinds()...),
		"WorkflowRunStatus":     wirespec.GoEnumValues(api.AllWorkflowRunStatuses()...),
	} {
		if len(declared[name]) == 0 {
			r.Report("pkg/api declares no %s constants", name)
			continue
		}
		r.Diff("All"+name+" registry", declared[name], registry)
	}
}

// Every pkg/api string enum has the same values in its OpenAPI schema and
// its generated types.ts union.
func stringEnumsMatch(r *invariants.Run) {
	goEnums := apiEnums(r)
	r.Expect(len(goEnums), "pkg/api string enums")
	openAPIEnums, err := spec(r).Enums()
	must(r, "list OpenAPI enums", err)
	tsEnums := wirespec.TSEnumUnions(r.Text(denTypesPath))
	for _, name := range sortedNames(goEnums) {
		ts, ok := tsEnums[name]
		if !ok {
			r.Report("%s has no generated types.ts union", name)
		} else {
			r.Diff(name+" Go vs types.ts", goEnums[name], ts)
		}
		oapi, ok := openAPIEnums[name]
		if !ok {
			r.Report("%s has no OpenAPI enum schema", name)
		} else {
			r.Diff(name+" Go vs OpenAPI", goEnums[name], oapi)
		}
	}
}

// stringConsts returns the values of the constants of one named string type.
func stringConsts(r *invariants.Run, dir, typeName string) map[string]string {
	pkg := r.TypedPackage(dir).Types
	out := map[string]string{}
	for _, name := range pkg.Scope().Names() {
		c, ok := pkg.Scope().Lookup(name).(*types.Const)
		if !ok {
			continue
		}
		if named, ok := c.Type().(*types.Named); ok && named.Obj().Name() == typeName && c.Val().Kind() == constant.String {
			out[name] = constant.StringVal(c.Val())
		}
	}
	r.Expect(len(out), dir+" "+typeName+" constants")
	return out
}

// The wire republishes the matcher's evidence lens through a string cast, so
// each matcher value must be a wire value. The wire also carries policy,
// which no matcher detects.
func redactionSourcesMatchTheWire(r *invariants.Run) {
	wire := map[string]bool{}
	for _, value := range stringConsts(r, "pkg/api", "RedactionSource") {
		wire[value] = true
	}
	for name, value := range stringConsts(r, redactionSourceIn, "RedactionSource") {
		if !wire[value] {
			r.Report("secretmatch.%s = %q has no api.RedactionSource", name, value)
		}
	}
}

var scannerSlotsTS = regexp.MustCompile(`export const SCANNER_SLOTS = \[([^\]]*)\] as const;`)

// Den lists the scanner slots itself so empty slots keep their display order;
// the list and its order follow Go.
func scannerSlotsMatchGo(r *invariants.Run) {
	match := scannerSlotsTS.FindStringSubmatch(r.Text(scannerSlotsPath))
	if match == nil {
		r.Fatalf("SCANNER_SLOTS not found in %s", scannerSlotsPath)
	}
	var den []string
	for _, part := range strings.Split(match[1], ",") {
		if part = strings.Trim(strings.TrimSpace(part), `"`); part != "" {
			den = append(den, part)
		}
	}
	if goSlots := scancatalog.SlotCategories(); !slices.Equal(den, goSlots) {
		r.Report("Den SCANNER_SLOTS %v, Go SlotCategories %v; the order is display order", den, goSlots)
	}
}

var waitLabelsTS = regexp.MustCompile(`(?s)WAIT_CONDITION_LABELS[^=]*=\s*\{(.*?)\n\};`)

// The wait tool's condition kinds, the host's triggers and Den's labels are
// one set; the timer is not a condition.
func waitTriggersAgree(r *invariants.Run) {
	var unit struct {
		Schema struct {
			Properties map[string]struct {
				Items struct {
					Properties map[string]struct {
						Enum []string `yaml:"enum"`
					} `yaml:"properties"`
				} `yaml:"items"`
			} `yaml:"properties"`
		} `yaml:"schema"`
	}
	must(r, "decode "+waitToolSchema, yaml.Unmarshal(r.Bytes(waitToolSchema), &unit))
	schema := unit.Schema.Properties["conditions"].Items.Properties["kind"].Enum
	r.Expect(len(schema), "wait condition kinds")
	var host []string
	for _, trigger := range loopwake.AllWaitTriggers() {
		if trigger != loopwake.WaitTriggerTimer {
			host = append(host, string(trigger))
		}
	}
	r.Diff("wait condition kinds vs loopwake triggers", schema, host)
	block := waitLabelsTS.FindStringSubmatch(r.Text(waitLabelsPath))
	if block == nil {
		r.Fatalf("WAIT_CONDITION_LABELS not found in %s", waitLabelsPath)
	}
	var den []string
	for _, m := range regexp.MustCompile(`(?m)^\s*([a-z_]+):`).FindAllStringSubmatch(block[1], -1) {
		if m[1] != "timer" {
			den = append(den, m[1])
		}
	}
	r.Diff("wait condition kinds vs Den labels", schema, den)
}

// The coverage member a review phase offers decodes as the wire
// CoverageReview: the same members, required set and dispositions.
func verdictCoverageMatchesTheWire(r *invariants.Run) {
	cfg, err := toolschema.LoadSchemaDir(r.Path(toolSchemasDir))
	must(r, "load tool schemas", err)
	meta, ok := cfg.ToolMeta("submit_verdict")
	if !ok {
		r.Fatalf("submit_verdict has no schema")
	}
	defs, _ := meta.ArgsSchema["$defs"].(map[string]any)
	review, _ := defs["coverage_review"].(map[string]any)
	assessment := schemaChild(schemaChild(review, "assessments"), "items")
	wire := invariantDoc(r).Components.Schemas
	sameMembers(r, "coverage_review", review, wire["CoverageReview"])
	sameMembers(r, "coverage_review.assessments[]", assessment, wire["CoverageAssessment"])
	r.Diff("coverage dispositions", stringList(schemaChild(wire["CoverageAssessment"], "disposition")["enum"]), stringList(schemaChild(assessment, "disposition")["enum"]))
}

func sameMembers(r *invariants.Run, name string, fragment, wire map[string]any) {
	if fragment == nil || wire == nil {
		r.Report("%s: the fragment or the wire schema is missing", name)
		return
	}
	r.Diff(name+" members", sortedNames(asMap(wire["properties"])), sortedNames(asMap(fragment["properties"])))
	r.Diff(name+" required", stringList(wire["required"]), stringList(fragment["required"]))
	if fragment["additionalProperties"] != false {
		r.Report("%s must refuse undeclared members like the wire decoder", name)
	}
}

func schemaChild(schema map[string]any, name string) map[string]any {
	if name == "items" {
		return asMap(schema["items"])
	}
	return asMap(asMap(schema["properties"])[name])
}

func stringList(raw any) []string {
	var out []string
	for _, v := range asSlice(raw) {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
