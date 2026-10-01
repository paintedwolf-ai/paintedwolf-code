package wirespec

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type DtoSyncSpec struct {
	GoValue       any
	OpenAPISchema string
	TSInterface   string
}

type openAPISchemaDoc struct {
	Type       string         `yaml:"type"`
	Properties map[string]any `yaml:"properties"`
}

func SyncDTOFields(root string, spec DtoSyncSpec) error {
	goType := reflect.TypeOf(spec.GoValue)
	goFields := JsonFieldNames(goType)

	openAPI, err := LoadOpenAPISchemaProperties(root, spec.OpenAPISchema)
	if err != nil {
		return err
	}
	if !contractcheck.SortedSetEqual(goFields, openAPI) {
		return fmt.Errorf("%s\nfix: align pkg/api JSON tags with docs/openapi.yaml schema %q",
			testutil.FormatSetDiff("Go vs OpenAPI "+spec.OpenAPISchema, goFields, openAPI), spec.OpenAPISchema)
	}

	tsFields, err := parseTSInterfaceFields(root, spec.TSInterface)
	if err != nil {
		return err
	}
	if !contractcheck.SortedSetEqual(goFields, tsFields) {
		return fmt.Errorf("%s\nfix: align pkg/api JSON tags with generated lycaon-den/src/api/types.ts schema %q",
			testutil.FormatSetDiff("Go vs TS "+spec.TSInterface, goFields, tsFields), spec.TSInterface)
	}
	return nil
}

func LoadOpenAPIObjectSchemas(repoRoot string) (map[string]openAPISchemaDoc, error) {
	doc, err := cachedOpenAPIDoc(repoRoot)
	if err != nil {
		return nil, err
	}
	return doc.objectSchemas, nil
}

func JsonFieldNames(typ reflect.Type) []string {
	var out []string
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func LoadOpenAPISchemaProperties(repoRoot, schema string) ([]string, error) {
	schemas, err := LoadOpenAPIObjectSchemas(repoRoot)
	if err != nil {
		return nil, err
	}
	s, ok := schemas[schema]
	if !ok {
		return nil, fmt.Errorf("schema %q not found", schema)
	}
	var out []string
	for k := range s.Properties {
		out = append(out, k)
	}
	return out, nil
}

func parseTSInterfaceFields(repoRoot, name string) ([]string, error) {
	content, err := cachedTSContent(repoRoot)
	if err != nil {
		return nil, err
	}
	return parseGeneratedSchemaFieldNames(content, name)
}
