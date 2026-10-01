package security

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// conformanceOp is one OpenAPI operation as the conformance sweep drives it.
type conformanceOp struct {
	ID     string
	Method string
	Path   string
	// PathParams are in template order.
	PathParams []*openapi3.Parameter
	Query      []*openapi3.Parameter
	// BodyMedia lists the declared request media types; empty means no body.
	BodyMedia    []string
	BodyRequired bool
	// BodyMaxBytes is the largest body a binary media schema admits
	// (its maxLength); zero when undeclared.
	BodyMaxBytes uint64
	// JSONBody is the application/json request schema, when declared.
	JSONBody *openapi3.Schema
	// Declared holds every explicitly declared status; `default` declares none.
	Declared   map[int]bool
	HasDefault bool
	// Streams marks a text/event-stream success response.
	Streams bool
	// JSONSuccess marks a 2xx application/json response.
	JSONSuccess bool
}

var templateParamRE = regexp.MustCompile(`\{([^}]+)\}`)

// conformanceOperations lists every operation in doc, sorted by operationId.
func conformanceOperations(doc *openapi3.T) []*conformanceOp {
	var ops []*conformanceOp
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			ops = append(ops, newConformanceOp(path, strings.ToUpper(method), item.Parameters, op))
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].ID < ops[j].ID })
	return ops
}

func newConformanceOp(path, method string, shared openapi3.Parameters, op *openapi3.Operation) *conformanceOp {
	out := &conformanceOp{ID: op.OperationID, Method: method, Path: path, Declared: map[int]bool{}}
	if out.ID == "" {
		out.ID = method + " " + path
	}
	byName := map[string]*openapi3.Parameter{}
	for _, ref := range append(append(openapi3.Parameters{}, shared...), op.Parameters...) {
		if ref == nil || ref.Value == nil {
			continue
		}
		switch ref.Value.In {
		case openapi3.ParameterInPath:
			byName[ref.Value.Name] = ref.Value
		case openapi3.ParameterInQuery:
			out.Query = append(out.Query, ref.Value)
		}
	}
	for _, match := range templateParamRE.FindAllStringSubmatch(path, -1) {
		param := byName[match[1]]
		if param == nil {
			param = &openapi3.Parameter{Name: match[1], In: openapi3.ParameterInPath, Required: true}
		}
		out.PathParams = append(out.PathParams, param)
	}
	if body := op.RequestBody; body != nil && body.Value != nil {
		out.BodyRequired = body.Value.Required
		for media, content := range body.Value.Content {
			out.BodyMedia = append(out.BodyMedia, media)
			if media == "application/json" && content.Schema != nil {
				out.JSONBody = content.Schema.Value
			}
			if content.Schema != nil && content.Schema.Value != nil && content.Schema.Value.MaxLength != nil {
				out.BodyMaxBytes = max(out.BodyMaxBytes, *content.Schema.Value.MaxLength)
			}
		}
		sort.Strings(out.BodyMedia)
	}
	for key, ref := range op.Responses.Map() {
		if key == "default" {
			out.HasDefault = true
			continue
		}
		status, err := strconv.Atoi(key)
		if err != nil {
			continue
		}
		out.Declared[status] = true
		if status >= 200 && status < 300 && ref.Value != nil {
			if _, ok := ref.Value.Content["text/event-stream"]; ok {
				out.Streams = true
			}
			if _, ok := ref.Value.Content["application/json"]; ok {
				out.JSONSuccess = true
			}
		}
	}
	return out
}

// declares reports whether status is declared. `default` stands only for the
// unexpected failure, 500: every other status must be declared by number.
func (op *conformanceOp) declares(status int) bool {
	return op.Declared[status] || (status == http.StatusInternalServerError && op.HasDefault)
}

// query returns the named query parameter.
func (op *conformanceOp) query(name string) *openapi3.Parameter {
	for _, p := range op.Query {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// pageParams returns JSON list continuation inputs, excluding stream cursors.
func (op *conformanceOp) pageParams() []string {
	if op.Method != http.MethodGet || !op.JSONSuccess {
		return nil
	}
	var names []string
	for _, name := range []string{"cursor", "before", "after"} {
		if op.query(name) != nil {
			names = append(names, name)
		}
	}
	return names
}

// prefix is the path template up to and including path parameter i: the
// address of the resource that parameter names.
func (op *conformanceOp) prefix(i int) string {
	marker := "{" + op.PathParams[i].Name + "}"
	return op.Path[:strings.Index(op.Path, marker)+len(marker)]
}

// namesResource reports whether path parameter i can name an unknown resource.
// A closed enum has no well-formed unknown member.
func (op *conformanceOp) namesResource(i int) bool {
	schema := paramSchema(op.PathParams[i])
	return schema == nil || len(schema.Enum) == 0
}

// upsertTarget reports whether path parameter i is the resource a PUT replaces
// (the last path segment): PUT may create it, so an unknown id may succeed.
func (op *conformanceOp) upsertTarget(i int) bool {
	return op.Method == http.MethodPut && i == len(op.PathParams)-1 &&
		strings.HasSuffix(op.Path, "{"+op.PathParams[i].Name+"}")
}

func paramSchema(p *openapi3.Parameter) *openapi3.Schema {
	if p == nil || p.Schema == nil {
		return nil
	}
	return p.Schema.Value
}
