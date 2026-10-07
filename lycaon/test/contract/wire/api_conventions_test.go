package contract

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"gopkg.in/yaml.v3"
)

// The API conventions in docs/host-contract.md, checked over docs/openapi.yaml.
// Every violation starts with the operationId, or with the component schema
// name for rules that belong to a schema, followed by the rule name.

const (
	schemaRefPrefix       = "#/components/schemas/"
	responseRefPrefix     = "#/components/responses/"
	positionalWindowKey   = "x-positional-window"
	multiStreamListKey    = "x-multi-stream-list"
	projectRoutePrefix    = "/v1/projects/{id}"
	patchRevisionProperty = "expected_revision"
	idempotencyProperty   = "operation_id"
)

var conventionMethods = map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}

type conventionOp struct {
	ID     string
	Method string
	Path   string
	Op     map[string]any
	Params []map[string]any
}

type conventionSpec struct {
	*invariantOpenAPIDoc
	ops []conventionOp
}

func loadConventionSpec(t *testing.T) *conventionSpec {
	t.Helper()
	doc := loadInvariantDoc(t)
	spec := &conventionSpec{invariantOpenAPIDoc: doc}
	for path, item := range doc.Paths {
		pathParams := asSlice(item["parameters"])
		for method, raw := range item {
			op := asMap(raw)
			if !conventionMethods[method] || op == nil {
				continue
			}
			entry := conventionOp{ID: asString(op["operationId"]), Method: method, Path: path, Op: op}
			if entry.ID == "" {
				entry.ID = strings.ToUpper(method) + " " + path
			}
			for _, p := range append(asSlice(op["parameters"]), pathParams...) {
				if pm := asMap(p); pm != nil {
					entry.Params = append(entry.Params, resolveParamMap(pm, doc.Components.Parameters))
				}
			}
			spec.ops = append(spec.ops, entry)
		}
	}
	sort.Slice(spec.ops, func(i, j int) bool { return spec.ops[i].ID < spec.ops[j].ID })
	return spec
}

// reportConventionViolations prints every violation, sorted, without stopping early.
func reportConventionViolations(t *testing.T, violations []string) {
	t.Helper()
	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("%s", v)
	}
}

// resolve follows component schema references.
func (s *conventionSpec) resolve(schema map[string]any) map[string]any {
	for range 32 {
		ref := asString(schema["$ref"])
		if !strings.HasPrefix(ref, schemaRefPrefix) {
			return schema
		}
		next := s.Components.Schemas[strings.TrimPrefix(ref, schemaRefPrefix)]
		if next == nil {
			return schema
		}
		schema = next
	}
	return schema
}

// effective resolves references and unwraps a nullable union or a single allOf member.
func (s *conventionSpec) effective(schema map[string]any) map[string]any {
	schema = s.resolve(schema)
	for _, key := range []string{"oneOf", "anyOf", "allOf"} {
		var branches []map[string]any
		for _, raw := range asSlice(schema[key]) {
			branch := s.resolve(asMap(raw))
			if branch != nil && !schemaIsNull(branch) {
				branches = append(branches, branch)
			}
		}
		if len(branches) == 1 && len(asMap(schema["properties"])) == 0 {
			return s.effective(branches[0])
		}
	}
	return schema
}

func schemaIsNull(schema map[string]any) bool {
	types := schemaTypes(schema)
	return len(types) == 1 && types[0] == "null"
}

func schemaTypes(schema map[string]any) []string {
	switch v := schema["type"].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			out = append(out, asString(item))
		}
		return out
	}
	return nil
}

func schemaHasType(schema map[string]any, want string) bool {
	for _, got := range schemaTypes(schema) {
		if got == want {
			return true
		}
	}
	return false
}

// objectShape merges properties and required names across allOf members.
func (s *conventionSpec) objectShape(schema map[string]any) (map[string]map[string]any, map[string]bool) {
	props := map[string]map[string]any{}
	required := map[string]bool{}
	var visit func(map[string]any, int)
	visit = func(node map[string]any, depth int) {
		node = s.resolve(node)
		if node == nil || depth > 16 {
			return
		}
		for name, raw := range asMap(node["properties"]) {
			props[name] = asMap(raw)
		}
		for _, name := range asSlice(node["required"]) {
			required[asString(name)] = true
		}
		for _, member := range asSlice(node["allOf"]) {
			visit(asMap(member), depth+1)
		}
		for _, member := range asSlice(node["oneOf"]) {
			visit(asMap(member), depth+1)
		}
	}
	visit(schema, 0)
	return props, required
}

func sortedNames[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// requestSchemas returns each media type's request body schema.
func requestSchemas(op map[string]any) []map[string]any {
	var out []map[string]any
	content := asMap(asMap(op["requestBody"])["content"])
	for _, mediaType := range sortedNames(content) {
		if schema := asMap(asMap(content[mediaType])["schema"]); schema != nil {
			out = append(out, schema)
		}
	}
	return out
}

func jsonRequestSchema(op map[string]any) map[string]any {
	return asMap(asMap(asMap(asMap(op["requestBody"])["content"])["application/json"])["schema"])
}

func (s *conventionSpec) response(raw any) map[string]any {
	resp := asMap(raw)
	if ref := asString(resp["$ref"]); strings.HasPrefix(ref, responseRefPrefix) {
		return s.Components.Responses[strings.TrimPrefix(ref, responseRefPrefix)]
	}
	return resp
}

// successJSONSchemas returns the JSON body schema of each 2xx response by status.
func (s *conventionSpec) successJSONSchemas(op map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for code, raw := range asMap(op["responses"]) {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		schema := asMap(asMap(asMap(s.response(raw)["content"])["application/json"])["schema"])
		if schema != nil {
			out[code] = schema
		}
	}
	return out
}

func queryParams(op conventionOp) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, p := range op.Params {
		if asString(p["in"]) == "query" {
			out[asString(p["name"])] = p
		}
	}
	return out
}

// schemaProperty is one declared property, attributed to the component schema
// or operation whose source declares it.
type schemaProperty struct {
	Owner  string
	Name   string
	Schema map[string]any
}

// properties visits every property declared in a component schema, a shared
// response, or an operation's inline schemas. A $ref is not entered: the
// referenced component reports its own properties.
func (s *conventionSpec) properties() []schemaProperty {
	var out []schemaProperty
	var walk func(owner string, node map[string]any)
	walk = func(owner string, node map[string]any) {
		if node == nil || node["$ref"] != nil {
			return
		}
		props := asMap(node["properties"])
		for _, name := range sortedNames(props) {
			out = append(out, schemaProperty{Owner: owner, Name: name, Schema: asMap(props[name])})
			walk(owner, asMap(props[name]))
		}
		walk(owner, asMap(node["items"]))
		walk(owner, asMap(node["additionalProperties"]))
		walk(owner, asMap(node["not"]))
		for _, key := range []string{"allOf", "oneOf", "anyOf", "prefixItems"} {
			for _, member := range asSlice(node[key]) {
				walk(owner, asMap(member))
			}
		}
	}
	for _, name := range sortedNames(s.Components.Schemas) {
		walk(name, s.Components.Schemas[name])
	}
	walkContent := func(owner string, resp map[string]any) {
		content := asMap(resp["content"])
		for _, mediaType := range sortedNames(content) {
			walk(owner, asMap(asMap(content[mediaType])["schema"]))
		}
	}
	for _, name := range sortedNames(s.Components.Responses) {
		walkContent(name, s.Components.Responses[name])
	}
	for _, op := range s.ops {
		for _, schema := range requestSchemas(op.Op) {
			walk(op.ID, schema)
		}
		responses := asMap(op.Op["responses"])
		for _, code := range sortedNames(responses) {
			if asString(asMap(responses[code])["$ref"]) == "" {
				walkContent(op.ID, asMap(responses[code]))
			}
		}
	}
	return out
}

// TestAPIConventionListEnvelope: a 2xx JSON body is never a bare array and never
// carries `items` or `count`; a list operation (verb list, or a cursor, before,
// or after parameter) answers an object with exactly one plural array.
func TestAPIConventionListEnvelope(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		params := queryParams(op)
		isList := operationVerb(op.ID) == "list" || params["cursor"] != nil || params["before"] != nil || params["after"] != nil
		bodies := spec.successJSONSchemas(op.Op)
		for _, code := range sortedNames(bodies) {
			body := spec.effective(bodies[code])
			if schemaHasType(body, "array") {
				violations = append(violations, fmt.Sprintf("%s: list-envelope: %s body is a bare array; wrap it in an object with one named plural array", op.ID, code))
				continue
			}
			props, _ := spec.objectShape(body)
			for _, banned := range []string{"items", "count"} {
				if props[banned] != nil {
					violations = append(violations, fmt.Sprintf("%s: list-envelope: %s body declares %q", op.ID, code, banned))
				}
			}
			if !isList || asBool(op.Op[multiStreamListKey]) {
				continue
			}
			var arrays []string
			for _, name := range sortedNames(props) {
				if schemaHasType(spec.effective(props[name]), "array") {
					arrays = append(arrays, name)
				}
			}
			switch {
			case len(arrays) != 1:
				violations = append(violations, fmt.Sprintf("%s: list-envelope: %s body has %d array properties %v; a list answers exactly one", op.ID, code, len(arrays), arrays))
			case !strings.HasSuffix(arrays[0], "s"):
				violations = append(violations, fmt.Sprintf("%s: list-envelope: %s array %q is not a plural resource name", op.ID, code, arrays[0]))
			}
		}
	}
	reportConventionViolations(t, violations)
}

var forbiddenPageParameters = map[string]bool{
	"page": true, "page_size": true, "per_page": true, "page_token": true, "skip": true,
	"next_cursor": true, "before_cursor": true, "after_cursor": true, "offset_cursor": true, "next_offset": true,
	"git_skip": true, "after_path": true, "before_ordinal": true, "after_ordinal": true, "around_ordinal": true,
	"before_revision": true, "after_revision": true,
}

var forbiddenContinuationProperties = map[string]bool{
	"has_more": true, "has_more_before": true, "has_more_after": true, "more": true,
	"next_offset": true, "next_path": true, "next_before_ordinal": true, "next_before_revision": true,
	"next_git_skip": true, "before_ordinal": true, "git_skip": true, "after_path": true, "before_revision": true,
	"next_after": true, "next_token": true, "page_token": true, "continuation_token": true,
}

var continuationProperties = map[string]bool{"next_cursor": true, "before_cursor": true, "after_cursor": true}

// TestAPIConventionPaginationParameters: forward pages take `cursor` + `limit`,
// bidirectional windows take `before`/`after` + `limit`, and `offset` appears
// only on operations marked `x-positional-window: true`. Every `limit`
// declares a minimum and maximum. Query parameters and request body fields
// follow the same vocabulary.
func TestAPIConventionPaginationParameters(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		inputs := map[string]map[string]any{}
		for name, p := range queryParams(op) {
			inputs[name] = asMap(p["schema"])
		}
		if body := jsonRequestSchema(op.Op); body != nil {
			props, _ := spec.objectShape(spec.effective(body))
			for name, schema := range props {
				inputs[name] = schema
			}
		}
		violations = append(violations, paginationInputViolations(spec, op, inputs)...)
	}
	reportConventionViolations(t, violations)
}

func paginationInputViolations(spec *conventionSpec, op conventionOp, inputs map[string]map[string]any) []string {
	var out []string
	add := func(format string, args ...any) {
		out = append(out, fmt.Sprintf("%s: pagination-parameters: ", op.ID)+fmt.Sprintf(format, args...))
	}
	for _, name := range sortedNames(inputs) {
		if forbiddenPageParameters[name] {
			add("input %q is not pagination vocabulary; use cursor, before/after, or a positional offset", name)
		}
	}
	positional := asBool(op.Op[positionalWindowKey])
	_, hasOffset := inputs["offset"]
	_, hasCursor := inputs["cursor"]
	_, hasBefore := inputs["before"]
	_, hasAfter := inputs["after"]
	limit, hasLimit := inputs["limit"]
	switch {
	case hasOffset && !positional:
		add("input \"offset\" requires %s: true (a positional window into one document); lists page with cursor", positionalWindowKey)
	case positional && !hasOffset:
		add("%s operation declares no \"offset\"", positionalWindowKey)
	}
	if positional && (hasCursor || hasBefore || hasAfter) {
		add("%s operation also declares cursor or before/after", positionalWindowKey)
	}
	if hasCursor && (hasBefore || hasAfter) {
		add("mixes forward \"cursor\" with bidirectional before/after")
	}
	// A stream resumes from its cursor; only a JSON answer is a page.
	pages := len(spec.successJSONSchemas(op.Op)) > 0
	if pages && (hasCursor || hasBefore || hasAfter || hasOffset) && !hasLimit {
		add("pages without a \"limit\" input")
	}
	if hasLimit {
		resolved := spec.effective(limit)
		if resolved["minimum"] == nil || resolved["maximum"] == nil {
			add("\"limit\" must declare minimum and maximum")
		}
	}
	return out
}

// TestAPIConventionContinuationFields: responses continue with `next_cursor`,
// `before_cursor`, or `after_cursor` (opaque strings) and no other
// continuation or has-more vocabulary.
func TestAPIConventionContinuationFields(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, prop := range spec.properties() {
		if forbiddenContinuationProperties[prop.Name] {
			violations = append(violations, fmt.Sprintf("%s: continuation-fields: property %q; continue with next_cursor or before_cursor/after_cursor", prop.Owner, prop.Name))
		}
		if continuationProperties[prop.Name] && !schemaHasType(spec.effective(prop.Schema), "string") {
			violations = append(violations, fmt.Sprintf("%s: continuation-fields: %q must be an opaque string cursor", prop.Owner, prop.Name))
		}
	}
	reportConventionViolations(t, violations)
}

// instantNames are the names an instant takes instead of `*_at`.
var instantNames = map[string]bool{
	"ts": true, "mtime": true, "since": true, "now": true,
	"first_seen": true, "last_seen": true, "last_used": true, "last_updated": true,
}

// TestAPIConventionInstantFields: `*_at` properties are date-time strings,
// every date-time property is `*_at`, every date property is `*_on`, and no
// instant goes by another name (`ts`, `*_ts`, `mtime`, `since`, …).
func TestAPIConventionInstantFields(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, prop := range spec.properties() {
		schema := spec.effective(prop.Schema)
		format := asString(schema["format"])
		add := func(msg string) {
			violations = append(violations, fmt.Sprintf("%s: instant-fields: property %q %s", prop.Owner, prop.Name, msg))
		}
		switch {
		case instantNames[prop.Name] || strings.HasSuffix(prop.Name, "_ts"):
			add("names an instant; name it *_at")
		case strings.HasSuffix(prop.Name, "_at") && (format != "date-time" || !schemaHasType(schema, "string")):
			add("must be a string with format date-time")
		case format == "date-time" && !strings.HasSuffix(prop.Name, "_at"):
			add("has format date-time; name it *_at")
		case format == "date" && !strings.HasSuffix(prop.Name, "_on"):
			add("has format date; name it *_on")
		}
	}
	for _, op := range spec.ops {
		for _, p := range op.Params {
			if name := asString(p["name"]); instantNames[name] || strings.HasSuffix(name, "_ts") {
				violations = append(violations, fmt.Sprintf("%s: instant-fields: parameter %q names an instant; name it *_at", op.ID, name))
			}
		}
	}
	reportConventionViolations(t, violations)
}

// identityNames maps a name to the one name its identity takes in the API:
// a worker job is worker_id, a delegation is delegation_id, a client-chosen
// idempotency key is operation_id, and a blueprint is a blueprint.
var identityNames = map[string]string{
	"job_id":        "worker_id",
	"task_id":       "worker_id",
	"worker_job_id": "worker_id",
	"departure_id":  "delegation_id",
	"request_id":    "operation_id",
	"submission_id": "operation_id",
	"plan_id":       "blueprint_id",
	"plan_path":     "blueprint_path",
	"plan_name":     "blueprint_title",
}

// TestAPIConventionIdentityNames: one identity has one name on every
// operation and event. Tool results follow their tool's vocabulary.
func TestAPIConventionIdentityNames(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	api := spec.reachable(apiRoots(t, spec))
	var violations []string
	for _, prop := range spec.properties() {
		if _, component := spec.Components.Schemas[prop.Owner]; component && !api["schemas/"+prop.Owner] {
			continue
		}
		if name, ok := identityNames[prop.Name]; ok {
			violations = append(violations, fmt.Sprintf("%s: identity-names: property %q is %q", prop.Owner, prop.Name, name))
		}
	}
	for _, op := range spec.ops {
		for _, p := range op.Params {
			if name, ok := identityNames[asString(p["name"])]; ok {
				violations = append(violations, fmt.Sprintf("%s: identity-names: parameter %q is %q", op.ID, asString(p["name"]), name))
			}
		}
	}
	reportConventionViolations(t, violations)
}

// canceledSpellings finds the doubled-l spellings; the API spells canceled,
// canceling, and cancelable with one l.
var canceledSpellings = regexp.MustCompile(`cancell(ed|ing|able)`)

// TestAPIConventionCanceledSpelling: no operationId, parameter, property, or
// enum value spells canceled with two l's.
func TestAPIConventionCanceledSpelling(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	add := func(owner, what, value string) {
		if canceledSpellings.MatchString(strings.ToLower(value)) {
			violations = append(violations, fmt.Sprintf("%s: canceled-spelling: %s %q", owner, what, value))
		}
	}
	for _, op := range spec.ops {
		add(op.ID, "operationId", op.ID)
		for _, p := range op.Params {
			add(op.ID, "parameter", asString(p["name"]))
		}
	}
	for _, prop := range spec.properties() {
		add(prop.Owner, "property", prop.Name)
		for _, value := range asSlice(prop.Schema["enum"]) {
			add(prop.Owner+"."+prop.Name, "enum value", asString(value))
		}
	}
	for _, name := range sortedNames(spec.Components.Schemas) {
		for _, value := range asSlice(spec.Components.Schemas[name]["enum"]) {
			add(name, "enum value", asString(value))
		}
	}
	reportConventionViolations(t, violations)
}

func isIdentifierName(name string) bool {
	return name == "id" || strings.HasSuffix(name, "_id")
}

// identifierIsDeclared accepts a string id with format uuid, a pattern, or a
// closed enum/const. Non-string ids are not bare strings.
func identifierIsDeclared(schema map[string]any) bool {
	if types := schemaTypes(schema); len(types) > 0 && !schemaHasType(schema, "string") {
		return true
	}
	return asString(schema["format"]) == "uuid" || schema["pattern"] != nil || schema["enum"] != nil || schema["const"] != nil
}

// TestAPIConventionIdentifierFormats: every `id` / `*_id` property and
// path/query parameter declares format uuid or a pattern.
func TestAPIConventionIdentifierFormats(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, prop := range spec.properties() {
		if isIdentifierName(prop.Name) && !identifierIsDeclared(spec.effective(prop.Schema)) {
			violations = append(violations, fmt.Sprintf("%s: identifier-formats: property %q declares neither format uuid nor a pattern", prop.Owner, prop.Name))
		}
	}
	for _, op := range spec.ops {
		for _, p := range op.Params {
			name, in := asString(p["name"]), asString(p["in"])
			if (in == "path" || in == "query") && isIdentifierName(name) && !identifierIsDeclared(spec.effective(asMap(p["schema"]))) {
				violations = append(violations, fmt.Sprintf("%s: identifier-formats: %s parameter %q declares neither format uuid nor a pattern", op.ID, in, name))
			}
		}
	}
	reportConventionViolations(t, violations)
}

func durationSuffixViolation(name string) bool {
	for _, suffix := range []string{"_sec", "_secs", "_seconds"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

// TestAPIConventionDurationFields: durations are integer `*_ms`; no seconds suffixes.
func TestAPIConventionDurationFields(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, prop := range spec.properties() {
		if durationSuffixViolation(prop.Name) {
			violations = append(violations, fmt.Sprintf("%s: duration-fields: property %q; durations are integer *_ms", prop.Owner, prop.Name))
		}
		if strings.HasSuffix(prop.Name, "_ms") && !schemaHasType(spec.effective(prop.Schema), "integer") {
			violations = append(violations, fmt.Sprintf("%s: duration-fields: property %q must be an integer", prop.Owner, prop.Name))
		}
	}
	for _, op := range spec.ops {
		for name, p := range queryParams(op) {
			if durationSuffixViolation(name) || (strings.HasSuffix(name, "_ms") && !schemaHasType(spec.effective(asMap(p["schema"])), "integer")) {
				violations = append(violations, fmt.Sprintf("%s: duration-fields: query parameter %q; durations are integer *_ms", op.ID, name))
			}
		}
	}
	reportConventionViolations(t, violations)
}

// TestAPIConventionMoneyFields: money is integer `*_nano_usd`; no other `*_usd`.
func TestAPIConventionMoneyFields(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, prop := range spec.properties() {
		switch {
		case strings.HasSuffix(prop.Name, "_nano_usd"):
			if !schemaHasType(spec.effective(prop.Schema), "integer") {
				violations = append(violations, fmt.Sprintf("%s: money-fields: property %q must be an integer", prop.Owner, prop.Name))
			}
		case strings.HasSuffix(prop.Name, "_usd"):
			violations = append(violations, fmt.Sprintf("%s: money-fields: property %q; money is integer *_nano_usd", prop.Owner, prop.Name))
		}
	}
	reportConventionViolations(t, violations)
}

// TestAPIConventionPutReplacesWholeDocument: a PUT body requires every property it declares.
func TestAPIConventionPutReplacesWholeDocument(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		body := jsonRequestSchema(op.Op)
		if op.Method != "put" || body == nil {
			continue
		}
		props, required := spec.objectShape(spec.effective(body))
		var optional []string
		for _, name := range sortedNames(props) {
			if !required[name] {
				optional = append(optional, name)
			}
		}
		if len(optional) > 0 {
			violations = append(violations, fmt.Sprintf("%s: put-full-body: PUT replaces the whole document but %v are optional", op.ID, optional))
		}
	}
	reportConventionViolations(t, violations)
}

// TestAPIConventionPatchMerges: a PATCH body requires nothing but the
// optimistic-concurrency `expected_revision` and the idempotency `operation_id`.
func TestAPIConventionPatchMerges(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		body := jsonRequestSchema(op.Op)
		if op.Method != "patch" || body == nil {
			continue
		}
		_, required := spec.objectShape(spec.effective(body))
		var fields []string
		for _, name := range sortedNames(required) {
			if name != patchRevisionProperty && name != idempotencyProperty {
				fields = append(fields, name)
			}
		}
		if len(fields) > 0 {
			violations = append(violations, fmt.Sprintf("%s: patch-merges: PATCH merges but requires %v", op.ID, fields))
		}
	}
	reportConventionViolations(t, violations)
}

// TestAPIConventionNoToggleSubresources: on/off state is PATCH {enabled}, never
// an `enabled` or `disabled` path segment.
func TestAPIConventionNoToggleSubresources(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		for _, segment := range strings.Split(op.Path, "/") {
			if segment == "enabled" || segment == "disabled" {
				violations = append(violations, fmt.Sprintf("%s: toggle-subresources: %s %s; toggle with PATCH {enabled}", op.ID, strings.ToUpper(op.Method), op.Path))
			}
		}
	}
	reportConventionViolations(t, violations)
}

// operationVerb is the leading lowercase word of a lowerCamel operationId.
func operationVerb(id string) string {
	for i, r := range id {
		if unicode.IsUpper(r) {
			return id[:i]
		}
	}
	return id
}

// contradictingVerbs lists, per method, the CRUD verbs that name another method.
var contradictingVerbs = map[string]map[string]bool{
	"get":    {"create": true, "update": true, "replace": true, "delete": true, "set": true, "add": true, "remove": true},
	"post":   {"get": true, "list": true, "update": true, "replace": true},
	"put":    {"get": true, "list": true, "create": true, "update": true, "delete": true},
	"patch":  {"get": true, "list": true, "create": true, "replace": true, "delete": true},
	"delete": {"get": true, "list": true, "create": true, "update": true, "replace": true, "set": true, "add": true},
}

// crudSynonyms maps a verb to the CRUD verb the API names that action with.
var crudSynonyms = map[string]string{
	"add": "create", "init": "create", "new": "create",
	"remove": "delete",
	"set":    "replace",
	"mutate": "update", "modify": "update", "edit": "update",
	"fetch": "get", "retrieve": "get",
}

// TestAPIConventionOperationVerbs: operationIds never use an HTTP method name
// or a CRUD synonym as the verb, and never a CRUD verb belonging to another
// method.
func TestAPIConventionOperationVerbs(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		verb := operationVerb(op.ID)
		switch {
		case verb == "post" || verb == "put" || verb == "patch":
			violations = append(violations, fmt.Sprintf("%s: operation-verbs: verb %q is an HTTP method name", op.ID, verb))
		case crudSynonyms[verb] != "":
			violations = append(violations, fmt.Sprintf("%s: operation-verbs: verb %q is spelled %q", op.ID, verb, crudSynonyms[verb]))
		case contradictingVerbs[op.Method][verb]:
			violations = append(violations, fmt.Sprintf("%s: operation-verbs: verb %q contradicts %s", op.ID, verb, strings.ToUpper(op.Method)))
		}
	}
	reportConventionViolations(t, violations)
}

// TestAPIConventionDeclaredErrors: authenticated operations declare 401 and
// operations with a request body declare 415.
func TestAPIConventionDeclaredErrors(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		responses := asMap(op.Op["responses"])
		security, overridden := op.Op["security"]
		authenticated := len(spec.Security) > 0
		if overridden {
			authenticated = len(asSlice(security)) > 0
		}
		if authenticated && responses["401"] == nil {
			violations = append(violations, fmt.Sprintf("%s: declared-errors: authenticated operation does not declare 401", op.ID))
		}
		if op.Op["requestBody"] != nil && responses["415"] == nil {
			violations = append(violations, fmt.Sprintf("%s: declared-errors: operation with a request body does not declare 415", op.ID))
		}
	}
	reportConventionViolations(t, violations)
}

// TestAPIConventionClosedRequestSchemas: a request body schema and every object
// schema inside it set `additionalProperties: false` (a composite may use
// `unevaluatedProperties: false`). Map-typed objects declare their value schema
// in additionalProperties and are closed by construction.
func TestAPIConventionClosedRequestSchemas(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		seen := map[string]bool{}
		for _, schema := range requestSchemas(op.Op) {
			spec.walkRequestObjects(schema, "body", false, seen, func(label string) {
				violations = append(violations, fmt.Sprintf("%s: closed-request-schemas: %s does not set additionalProperties: false", op.ID, label))
			})
		}
	}
	reportConventionViolations(t, violations)
}

func (s *conventionSpec) walkRequestObjects(node map[string]any, label string, member bool, seen map[string]bool, open func(string)) {
	if node == nil {
		return
	}
	if ref := asString(node["$ref"]); strings.HasPrefix(ref, schemaRefPrefix) {
		name := strings.TrimPrefix(ref, schemaRefPrefix)
		key := name
		if member {
			key += "#member"
		}
		if seen[key] {
			return
		}
		seen[key] = true
		s.walkRequestObjects(s.Components.Schemas[name], name, member, seen, open)
		return
	}
	members := asSlice(node["allOf"])
	// A one-member allOf beside no properties only decorates a reference.
	decorates := len(members) == 1 && node["properties"] == nil
	isObject := schemaHasType(node, "object") || node["properties"] != nil || (len(members) > 1 && s.composesObject(members))
	_, isMap := node["additionalProperties"].(map[string]any)
	closed := node["additionalProperties"] == false || node["unevaluatedProperties"] == false
	if isObject && !isMap && !member && !closed {
		open(label)
	}
	props := asMap(node["properties"])
	for _, name := range sortedNames(props) {
		s.walkRequestObjects(asMap(props[name]), label+"."+name, false, seen, open)
	}
	s.walkRequestObjects(asMap(node["items"]), label+"[]", false, seen, open)
	if isMap {
		s.walkRequestObjects(asMap(node["additionalProperties"]), label+"{}", false, seen, open)
	}
	for _, raw := range members {
		s.walkRequestObjects(asMap(raw), label, !decorates, seen, open)
	}
	for _, key := range []string{"oneOf", "anyOf"} {
		for _, raw := range asSlice(node[key]) {
			s.walkRequestObjects(asMap(raw), label, false, seen, open)
		}
	}
}

// composesObject reports whether an allOf composes an object: a member,
// through its reference, declares object type or properties.
func (s *conventionSpec) composesObject(members []any) bool {
	for _, raw := range members {
		member := asMap(raw)
		if ref := asString(member["$ref"]); strings.HasPrefix(ref, schemaRefPrefix) {
			member = s.Components.Schemas[strings.TrimPrefix(ref, schemaRefPrefix)]
		}
		if schemaHasType(member, "object") || member["properties"] != nil {
			return true
		}
	}
	return false
}

// TestAPIConventionStatusCodes: 201 answers only POST, and DELETE answers 204,
// or 202 for a durable operation that outlives the response.
func TestAPIConventionStatusCodes(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		responses := asMap(op.Op["responses"])
		if responses["201"] != nil && op.Method != "post" {
			violations = append(violations, fmt.Sprintf("%s: status-codes: 201 on %s; only POST creates", op.ID, strings.ToUpper(op.Method)))
		}
		if op.Method != "delete" {
			continue
		}
		for _, code := range sortedNames(responses) {
			if strings.HasPrefix(code, "2") && code != "204" && code != "202" {
				violations = append(violations, fmt.Sprintf("%s: status-codes: DELETE answers %s; DELETE answers 204 with no body or 202 for a durable operation", op.ID, code))
			}
		}
	}
	reportConventionViolations(t, violations)
}

// TestAPIConventionReachableComponents: every component schema is reachable
// from an operation, an event topic payload, the event envelope, or a tool
// result schema that declares x-tool-output.
func TestAPIConventionReachableComponents(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	roots, violations := toolOutputRoots(t, spec.Components.Schemas)
	reached := spec.reachable(append(apiRoots(t, spec), schemaRefs(roots)...))
	for _, name := range sortedNames(spec.Components.Schemas) {
		if !reached["schemas/"+name] {
			violations = append(violations, fmt.Sprintf("%s: reachable-components: schema is not reachable from any operation or event", name))
		}
	}
	reportConventionViolations(t, violations)
}

// apiRoots are the operations, every EventTopic payload, and the event
// envelope: the surface the API conventions govern.
func apiRoots(t *testing.T, spec *conventionSpec) []any {
	t.Helper()
	roots := make([]any, 0, len(spec.Paths))
	for _, item := range spec.Paths {
		roots = append(roots, item)
	}
	return append(roots, schemaRefs(eventSchemaRoots(t))...)
}

func schemaRefs(names []string) []any {
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"$ref": schemaRefPrefix + name})
	}
	return out
}

// reachable is every component ("schemas/<name>", "responses/<name>",
// "parameters/<name>") the seeds reference, transitively.
func (s *conventionSpec) reachable(seeds []any) map[string]bool {
	reached := map[string]bool{}
	var visit func(any)
	visit = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			if ref := asString(v["$ref"]); ref != "" {
				switch {
				case strings.HasPrefix(ref, schemaRefPrefix):
					visitComponent(reached, "schemas/"+strings.TrimPrefix(ref, schemaRefPrefix), s.Components.Schemas[strings.TrimPrefix(ref, schemaRefPrefix)], visit)
				case strings.HasPrefix(ref, responseRefPrefix):
					visitComponent(reached, "responses/"+strings.TrimPrefix(ref, responseRefPrefix), s.Components.Responses[strings.TrimPrefix(ref, responseRefPrefix)], visit)
				case strings.HasPrefix(ref, "#/components/parameters/"):
					name := strings.TrimPrefix(ref, "#/components/parameters/")
					visitComponent(reached, "parameters/"+name, s.Components.Parameters[name], visit)
				}
			}
			for _, child := range v {
				visit(child)
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	for _, seed := range seeds {
		visit(seed)
	}
	return reached
}

func visitComponent[V any](reached map[string]bool, key string, component V, visit func(any)) {
	if reached[key] {
		return
	}
	reached[key] = true
	visit(any(component))
}

// toolOutputRoots names the schemas that declare `x-tool-output`: the JSON a
// named agent tool returns, which Den parses from tool results. Each named tool
// must have a tool schema under the platform pack.
func toolOutputRoots(t *testing.T, schemas map[string]map[string]any) ([]string, []string) {
	t.Helper()
	toolDir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "config", "packs", "painted-wolf", "platform", "tools", "schemas")
	var roots, violations []string
	for _, name := range sortedNames(schemas) {
		raw, ok := schemas[name]["x-tool-output"]
		if !ok {
			continue
		}
		tools := asSlice(raw)
		if len(tools) == 0 {
			violations = append(violations, fmt.Sprintf("%s: reachable-components: x-tool-output must list the tools that return it", name))
			continue
		}
		for _, tool := range tools {
			tool := asString(tool)
			if _, err := os.Stat(filepath.Join(toolDir, tool+".yaml")); tool == "" || err != nil {
				violations = append(violations, fmt.Sprintf("%s: reachable-components: x-tool-output names %q, which is not a platform tool", name, tool))
			}
		}
		roots = append(roots, name)
	}
	return roots, violations
}

// eventSchemaRoots names the event envelope and every EventTopic payload schema.
func eventSchemaRoots(t *testing.T) []string {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "openapi", "vocab", "EventTopic.yaml"))
	contractcheck.FailErr(t, "read EventTopic vocabulary", err)
	var vocab eventTopicVocab
	contractcheck.FailErr(t, "decode EventTopic vocabulary", yaml.Unmarshal(data, &vocab))
	roots := []string{"EventEnvelope"}
	for _, topic := range vocab.Values {
		if _, name, ok := strings.Cut(topic.Payload, schemaRefPrefix); ok {
			roots = append(roots, name)
		}
	}
	return roots
}

// TestAPIConventionBodyCarriesNoProjectScope: a route under /v1/projects/{id}
// takes its project from the path, never from a body `project_id`.
func TestAPIConventionBodyCarriesNoProjectScope(t *testing.T) {
	t.Parallel()
	spec := loadConventionSpec(t)
	var violations []string
	for _, op := range spec.ops {
		if op.Path != projectRoutePrefix && !strings.HasPrefix(op.Path, projectRoutePrefix+"/") {
			continue
		}
		for _, schema := range requestSchemas(op.Op) {
			if props, _ := spec.objectShape(spec.effective(schema)); props["project_id"] != nil {
				violations = append(violations, fmt.Sprintf("%s: body-project-scope: request body carries project_id; the path addresses the project", op.ID))
			}
		}
	}
	reportConventionViolations(t, violations)
}
