package wirespec

import (
	"fmt"
	"os"
	"path/filepath"
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

// SyncDTO compares a Go DTO's JSON fields with its OpenAPI schema and its
// generated TypeScript schema in types.ts.
func (s *Spec) SyncDTO(ts string, spec DtoSyncSpec) error {
	goFields := JsonFieldNames(reflect.TypeOf(spec.GoValue))
	openAPI, err := s.SchemaProperties(spec.OpenAPISchema)
	if err != nil {
		return err
	}
	if !contractcheck.SortedSetEqual(goFields, openAPI) {
		return fmt.Errorf("%s\nfix: align pkg/api JSON tags with docs/openapi.yaml schema %q",
			testutil.FormatSetDiff("Go vs OpenAPI "+spec.OpenAPISchema, goFields, openAPI), spec.OpenAPISchema)
	}
	tsFields, err := parseGeneratedSchemaFieldNames(ts, spec.TSInterface)
	if err != nil {
		return err
	}
	if !contractcheck.SortedSetEqual(goFields, tsFields) {
		return fmt.Errorf("%s\nfix: align pkg/api JSON tags with generated lycaon-den/src/api/types.ts schema %q",
			testutil.FormatSetDiff("Go vs TS "+spec.TSInterface, goFields, tsFields), spec.TSInterface)
	}
	return nil
}

// SyncDTOFields runs SyncDTO against a checkout.
func SyncDTOFields(repoRoot string, spec DtoSyncSpec) error {
	s, err := ReadSpec(repoRoot)
	if err != nil {
		return err
	}
	ts, err := os.ReadFile(filepath.Join(repoRoot, "lycaon-den", "src", "api", "types.ts"))
	if err != nil {
		return err
	}
	return s.SyncDTO(string(ts), spec)
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
