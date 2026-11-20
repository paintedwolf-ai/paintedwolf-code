package wirespec

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Spec is docs/openapi.yaml parsed once. Every view decodes from the parsed
// tree, so no reader parses the document again.
type Spec struct {
	root yaml.Node
	text string
}

// ParseSpec parses the bundled OpenAPI document.
func ParseSpec(data []byte) (*Spec, error) {
	s := &Spec{text: string(data)}
	if err := yaml.Unmarshal(data, &s.root); err != nil {
		return nil, fmt.Errorf("parse docs/openapi.yaml: %w", err)
	}
	if len(s.root.Content) == 0 {
		return nil, fmt.Errorf("docs/openapi.yaml is empty")
	}
	return s, nil
}

var specs sync.Map // repo root -> *specOnce

type specOnce struct {
	once sync.Once
	spec *Spec
	err  error
}

// ReadSpec returns the OpenAPI document of a checkout, read once per root.
func ReadSpec(repoRoot string) (*Spec, error) {
	v, _ := specs.LoadOrStore(filepath.Clean(repoRoot), &specOnce{})
	entry := v.(*specOnce)
	entry.once.Do(func() {
		data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "openapi.yaml"))
		if err != nil {
			entry.err = err
			return
		}
		entry.spec, entry.err = ParseSpec(data)
	})
	return entry.spec, entry.err
}

// Decode decodes the document into v.
func (s *Spec) Decode(v any) error {
	return s.root.Decode(v)
}

type SecurityRequirement map[string][]string

type securityScheme struct {
	Type   string `yaml:"type"`
	Scheme string `yaml:"scheme"`
}

type securityOperation struct {
	OperationID string                 `yaml:"operationId"`
	Security    *[]SecurityRequirement `yaml:"security"`
}

// SecurityDoc is the security view of the document.
type SecurityDoc struct {
	Security   []SecurityRequirement                   `yaml:"security"`
	Paths      map[string]map[string]securityOperation `yaml:"paths"`
	Components struct {
		SecuritySchemes map[string]securityScheme `yaml:"securitySchemes"`
	} `yaml:"components"`
}

// Security returns the global and per-operation security declarations.
func (s *Spec) Security() (*SecurityDoc, error) {
	var doc SecurityDoc
	if err := s.Decode(&doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

type routeOperation struct {
	OperationID  string               `yaml:"operationId"`
	LycaonStatus string               `yaml:"x-paintedwolf-status"`
	Responses    map[string]yaml.Node `yaml:"responses"`
}

type Route struct {
	Method             string
	Path               string
	OperationID        string
	Stub               bool
	DocumentedStatuses []string
}

// isOperationKey reports whether a path item key names an HTTP method.
func isOperationKey(key string) bool {
	key = strings.ToUpper(key)
	return key != "PARAMETERS" && key != "SERVERS"
}

// Routes returns every documented operation.
func (s *Spec) Routes() ([]Route, error) {
	var doc struct {
		Paths map[string]map[string]routeOperation `yaml:"paths"`
	}
	if err := s.Decode(&doc); err != nil {
		return nil, err
	}
	var routes []Route
	for path, methods := range doc.Paths {
		for method, op := range methods {
			if !isOperationKey(method) {
				continue
			}
			var statuses []string
			for code := range op.Responses {
				statuses = append(statuses, code)
			}
			sort.Strings(statuses)
			routes = append(routes, Route{
				Method:             strings.ToUpper(method),
				Path:               path,
				OperationID:        op.OperationID,
				Stub:               op.LycaonStatus == "stub",
				DocumentedStatuses: statuses,
			})
		}
	}
	sort.Slice(routes, func(i, j int) bool {
		return RouteKey(routes[i].Method, routes[i].Path) < RouteKey(routes[j].Method, routes[j].Path)
	})
	return routes, nil
}

// OperationIDs returns every declared operationId.
func (s *Spec) OperationIDs() ([]string, error) {
	routes, err := s.Routes()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range routes {
		if r.OperationID != "" {
			ids = append(ids, r.OperationID)
		}
	}
	return ids, nil
}

type queryParam struct {
	Name string `yaml:"name"`
	In   string `yaml:"in"`
	Ref  string `yaml:"$ref"`
}

type RouteQuery struct {
	Method      string
	Path        string
	OperationID string
	QueryParams []string
}

var ForbiddenOpenAPIQueryNames = map[string]struct{}{
	"project":     {},
	"project_dir": {},
	"session":     {},
}

// RouteQueries returns each operation with its query parameter names,
// resolving component references.
func (s *Spec) RouteQueries() ([]RouteQuery, error) {
	var doc struct {
		Paths map[string]map[string]struct {
			OperationID string       `yaml:"operationId"`
			Parameters  []queryParam `yaml:"parameters"`
		} `yaml:"paths"`
		Components struct {
			Parameters map[string]queryParam `yaml:"parameters"`
		} `yaml:"components"`
	}
	if err := s.Decode(&doc); err != nil {
		return nil, err
	}
	var routes []RouteQuery
	for path, methods := range doc.Paths {
		for method, op := range methods {
			if !isOperationKey(method) {
				continue
			}
			var names []string
			for _, p := range op.Parameters {
				if p.Ref != "" {
					comp, ok := doc.Components.Parameters[strings.TrimPrefix(p.Ref, "#/components/parameters/")]
					if ok && (comp.In == "query" || comp.In == "") {
						names = append(names, comp.Name)
					}
					continue
				}
				if p.In == "query" {
					names = append(names, p.Name)
				}
			}
			routes = append(routes, RouteQuery{Method: strings.ToUpper(method), Path: path, OperationID: op.OperationID, QueryParams: names})
		}
	}
	return routes, nil
}

type componentSchema struct {
	Type       string         `yaml:"type"`
	Enum       []string       `yaml:"enum"`
	Properties map[string]any `yaml:"properties"`
	Required   []string       `yaml:"required"`
}

func (s *Spec) schemas() (map[string]componentSchema, error) {
	var doc struct {
		Components struct {
			Schemas map[string]componentSchema `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := s.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Components.Schemas) == 0 {
		return nil, fmt.Errorf("docs/openapi.yaml declares no component schemas")
	}
	return doc.Components.Schemas, nil
}

// Enums returns each string enum component schema by name.
func (s *Spec) Enums() (map[string][]string, error) {
	schemas, err := s.schemas()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string)
	for name, schema := range schemas {
		if schema.Type == "string" && len(schema.Enum) > 0 {
			out[name] = append([]string(nil), schema.Enum...)
		}
	}
	return out, nil
}

// ObjectSchemas returns the property names of each object component schema.
// A published object with no properties is still published.
func (s *Spec) ObjectSchemas() (map[string][]string, error) {
	schemas, err := s.schemas()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string)
	for name, schema := range schemas {
		if schema.Type != "object" {
			continue
		}
		props := make([]string, 0, len(schema.Properties))
		for prop := range schema.Properties {
			props = append(props, prop)
		}
		sort.Strings(props)
		out[name] = props
	}
	return out, nil
}

// SchemaProperties returns the property names of one object schema.
func (s *Spec) SchemaProperties(schema string) ([]string, error) {
	objects, err := s.ObjectSchemas()
	if err != nil {
		return nil, err
	}
	props, ok := objects[schema]
	if !ok {
		return nil, fmt.Errorf("schema %q not found", schema)
	}
	return props, nil
}

// SchemaFields returns the properties of one schema with their optionality.
func (s *Spec) SchemaFields(schema string) ([]Field, error) {
	schemas, err := s.schemas()
	if err != nil {
		return nil, err
	}
	sc, ok := schemas[schema]
	if !ok {
		return nil, fmt.Errorf("schema %q not found", schema)
	}
	required := make(map[string]bool, len(sc.Required))
	for _, r := range sc.Required {
		required[r] = true
	}
	var out []Field
	for name := range sc.Properties {
		out = append(out, Field{Name: name, Optional: !required[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

var schemaRefRE = regexp.MustCompile(`#/components/schemas/([A-Za-z0-9_]+)`)

// ResolveSchemaRefs fails on a schema reference no component declares.
func (s *Spec) ResolveSchemaRefs() error {
	schemas, err := s.schemas()
	if err != nil {
		return err
	}
	var missing []string
	for _, m := range schemaRefRE.FindAllStringSubmatch(s.text, -1) {
		if _, ok := schemas[m[1]]; !ok {
			missing = append(missing, m[1])
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing components.schemas: %s", strings.Join(slicesCompact(missing), ", "))
	}
	return nil
}

func slicesCompact(sorted []string) []string {
	out := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// LoadOpenAPIRoutes returns the documented operations of a checkout.
func LoadOpenAPIRoutes(repoRoot string) ([]Route, error) {
	s, err := ReadSpec(repoRoot)
	if err != nil {
		return nil, err
	}
	return s.Routes()
}

// LoadOpenAPISchemaProperties returns one object schema's property names.
func LoadOpenAPISchemaProperties(repoRoot, schema string) ([]string, error) {
	s, err := ReadSpec(repoRoot)
	if err != nil {
		return nil, err
	}
	return s.SchemaProperties(schema)
}

// LoadOpenAPIObjectSchemas returns the property names of each object schema.
func LoadOpenAPIObjectSchemas(repoRoot string) (map[string][]string, error) {
	s, err := ReadSpec(repoRoot)
	if err != nil {
		return nil, err
	}
	return s.ObjectSchemas()
}

// ValidateOpenAPISchemaRefs fails on a dangling schema reference.
func ValidateOpenAPISchemaRefs(repoRoot string) error {
	s, err := ReadSpec(repoRoot)
	if err != nil {
		return err
	}
	return s.ResolveSchemaRefs()
}
