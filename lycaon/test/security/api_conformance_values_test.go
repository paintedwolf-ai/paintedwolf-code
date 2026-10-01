package security

import (
	"maps"
	"math"
	"math/rand/v2"
	"net/url"
	"regexp"
	"regexp/syntax"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/google/uuid"
)

// conformanceValues is the sweep's one source of request values. Fixture ids
// are bound by resource address (the path template up to the parameter that
// names the resource) and by parameter or property name; everything else is
// derived from the schema. It is written only while the sweep discovers
// fixtures, before any concurrent phase reads it.
type conformanceValues struct {
	byPrefix map[string]string
	byName   map[string]string
}

func newConformanceValues() *conformanceValues {
	return &conformanceValues{byPrefix: map[string]string{}, byName: map[string]string{}}
}

// bind records a real value for a resource address and, when name is set, for
// parameters and properties of that name. The first binding wins.
func (v *conformanceValues) bind(prefix, name, value string) {
	if value == "" {
		return
	}
	if _, ok := v.byPrefix[prefix]; prefix != "" && !ok {
		v.byPrefix[prefix] = value
	}
	if _, ok := v.byName[name]; name != "" && !ok {
		v.byName[name] = value
	}
}

// pathParams answers a value for every path parameter: a fixture when one is
// bound to its address, otherwise a fresh well-formed value. known[i] reports
// which parameters name a real resource.
func (v *conformanceValues) pathParams(op *conformanceOp) (map[string]string, []bool) {
	params := make(map[string]string, len(op.PathParams))
	known := make([]bool, len(op.PathParams))
	for i, p := range op.PathParams {
		if value, ok := v.byPrefix[op.prefix(i)]; ok {
			params[p.Name], known[i] = value, true
			continue
		}
		if schema := paramSchema(p); schema != nil && len(schema.Enum) > 0 {
			params[p.Name], known[i] = scalarString(schema.Enum[0]), true
			continue
		}
		params[p.Name] = freshValue(paramSchema(p))
	}
	return params, known
}

// freshValue is a well-formed value that names nothing: a new UUID, or a
// random member of the declared pattern.
func freshValue(schema *openapi3.Schema) string {
	if schema != nil && schema.Pattern != "" && schema.Format != "uuid" {
		if value, ok := patternValue(schema.Pattern); ok {
			return value
		}
	}
	return uuid.NewString()
}

// requiredQuery answers every required query parameter.
func (v *conformanceValues) requiredQuery(op *conformanceOp) url.Values {
	query := url.Values{}
	for _, p := range op.Query {
		if p.Required {
			query.Set(p.Name, v.queryValue(p))
		}
	}
	return query
}

func (v *conformanceValues) queryValue(p *openapi3.Parameter) string {
	if value, ok := v.byName[p.Name]; ok {
		return value
	}
	return scalarString(v.schemaValue(paramSchema(p), p.Name, nil))
}

// body synthesizes a request body that validates against schema. secret, when
// set, supplies the value of every writeOnly property, optional ones included.
func (v *conformanceValues) body(schema *openapi3.Schema, secret func(name string) string) any {
	return v.schemaValue(schema, "", secret)
}

func (v *conformanceValues) schemaValue(schema *openapi3.Schema, name string, secret func(string) string) any {
	switch {
	case schema == nil:
		return "conformance"
	case schema.Const != nil:
		return schema.Const
	case len(schema.Enum) > 0:
		return schema.Enum[0]
	case schema.Default != nil:
		return schema.Default
	case len(schema.AllOf) > 0:
		return v.allOfValue(schema, name, secret)
	case len(schema.OneOf) > 0:
		return v.schemaValue(firstNonNull(schema.OneOf), name, secret)
	case len(schema.AnyOf) > 0:
		return v.schemaValue(firstNonNull(schema.AnyOf), name, secret)
	}
	switch schemaType(schema) {
	case openapi3.TypeObject:
		return v.objectValue(schema, secret)
	case openapi3.TypeArray:
		items := make([]any, 0, schema.MinItems)
		for range schema.MinItems {
			items = append(items, v.schemaValue(refValue(schema.Items), name, secret))
		}
		return items
	case openapi3.TypeInteger:
		return int64(math.Ceil(numberValue(schema)))
	case openapi3.TypeNumber:
		return numberValue(schema)
	case openapi3.TypeBoolean:
		return false
	}
	return v.stringValue(schema, name)
}

func (v *conformanceValues) allOfValue(schema *openapi3.Schema, name string, secret func(string) string) any {
	merged := map[string]any{}
	for _, member := range schema.AllOf {
		value := v.schemaValue(refValue(member), name, secret)
		fields, ok := value.(map[string]any)
		if !ok {
			return value
		}
		for key, field := range fields {
			merged[key] = field
		}
	}
	return merged
}

func (v *conformanceValues) objectValue(schema *openapi3.Schema, secret func(string) string) map[string]any {
	out := map[string]any{}
	for _, name := range schema.Required {
		out[name] = v.propertyValue(refValue(schema.Properties[name]), name, secret)
	}
	// minProperties beyond the required fields is met by optional properties
	// in name order.
	optional := slices.Sorted(maps.Keys(schema.Properties))
	for _, name := range optional {
		if uint64(len(out)) >= schema.MinProps {
			break
		}
		if _, set := out[name]; !set {
			out[name] = v.propertyValue(refValue(schema.Properties[name]), name, secret)
		}
	}
	if secret == nil {
		return out
	}
	for name, ref := range schema.Properties {
		if prop := refValue(ref); prop != nil && prop.WriteOnly {
			out[name] = v.propertyValue(prop, name, secret)
		}
	}
	return out
}

func (v *conformanceValues) propertyValue(prop *openapi3.Schema, name string, secret func(string) string) any {
	if prop != nil && prop.WriteOnly && secret != nil {
		if schemaType(prop) == openapi3.TypeObject {
			return map[string]any{"PW_CONFORMANCE_" + strings.ToUpper(name): secret(name)}
		}
		return secret(name)
	}
	if value, ok := v.byName[name]; ok && (prop == nil || schemaType(prop) == openapi3.TypeString) {
		return value
	}
	return v.schemaValue(prop, name, secret)
}

func (v *conformanceValues) stringValue(schema *openapi3.Schema, name string) string {
	if value, ok := v.byName[name]; ok {
		return value
	}
	switch schema.Format {
	case "uuid":
		return uuid.NewString()
	case "date-time":
		return time.Now().UTC().Format(time.RFC3339)
	case "date":
		return time.Now().UTC().Format(time.DateOnly)
	case "uri", "url":
		return "https://example.invalid/conformance"
	}
	if schema.Pattern != "" {
		if value, ok := patternValue(schema.Pattern); ok {
			return value
		}
	}
	value := "conformance"
	for uint64(len(value)) < schema.MinLength {
		value += "-conformance"
	}
	if schema.MaxLength != nil && uint64(len(value)) > *schema.MaxLength {
		value = value[:*schema.MaxLength]
	}
	return value
}

func numberValue(schema *openapi3.Schema) float64 {
	value := 0.0
	if schema.Min != nil {
		value = *schema.Min
		if schema.ExclusiveMin.Bool != nil && *schema.ExclusiveMin.Bool {
			value++
		}
	}
	if schema.ExclusiveMin.Value != nil {
		value = *schema.ExclusiveMin.Value + 1
	}
	if schema.Max != nil && value > *schema.Max {
		value = *schema.Max
	}
	return value
}

// schemaType is the first declared non-null type.
func schemaType(schema *openapi3.Schema) string {
	if schema == nil || schema.Type == nil {
		if schema != nil && len(schema.Properties) > 0 {
			return openapi3.TypeObject
		}
		return ""
	}
	for _, t := range *schema.Type {
		if t != openapi3.TypeNull {
			return t
		}
	}
	return ""
}

func firstNonNull(refs openapi3.SchemaRefs) *openapi3.Schema {
	for _, ref := range refs {
		if schema := refValue(ref); schema != nil && schemaType(schema) != openapi3.TypeNull {
			return schema
		}
	}
	return refValue(refs[0])
}

func refValue(ref *openapi3.SchemaRef) *openapi3.Schema {
	if ref == nil {
		return nil
	}
	return ref.Value
}

func scalarString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	case bool:
		return strconv.FormatBool(v)
	}
	return "conformance"
}

// patternValue generates a random member of pattern. Optional and repeated
// parts are expanded at random so repeated calls name different values.
func patternValue(pattern string) (string, bool) {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return "", false
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", false
	}
	parsed = parsed.Simplify()
	for range 16 {
		var b strings.Builder
		generatePattern(&b, parsed)
		if re.MatchString(b.String()) {
			return b.String(), true
		}
	}
	return "", false
}

func generatePattern(b *strings.Builder, re *syntax.Regexp) {
	switch re.Op {
	case syntax.OpLiteral:
		b.WriteString(string(re.Rune))
	case syntax.OpCharClass:
		b.WriteRune(classRune(re.Rune))
	case syntax.OpAnyChar, syntax.OpAnyCharNotNL:
		b.WriteRune(rune('a' + rand.IntN(26)))
	case syntax.OpCapture:
		generatePattern(b, re.Sub[0])
	case syntax.OpConcat:
		for _, sub := range re.Sub {
			generatePattern(b, sub)
		}
	case syntax.OpAlternate:
		generatePattern(b, re.Sub[rand.IntN(len(re.Sub))])
	case syntax.OpQuest:
		if rand.IntN(5) > 0 {
			generatePattern(b, re.Sub[0])
		}
	case syntax.OpStar, syntax.OpPlus:
		count := 4 + rand.IntN(6)
		for range count {
			generatePattern(b, re.Sub[0])
		}
	case syntax.OpRepeat:
		for range max(re.Min, 1) {
			generatePattern(b, re.Sub[0])
		}
	default:
		// Anchors, boundaries, and empty matches write nothing.
	}
}

// classRune picks a random ASCII letter or digit from a character class,
// falling back to the class's first rune.
func classRune(ranges []rune) rune {
	var candidates []rune
	for i := 0; i+1 < len(ranges); i += 2 {
		for r := ranges[i]; r <= ranges[i+1] && r < unicode.MaxASCII; r++ {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				candidates = append(candidates, r)
			}
		}
	}
	if len(candidates) == 0 {
		return ranges[0]
	}
	return candidates[rand.IntN(len(candidates))]
}
