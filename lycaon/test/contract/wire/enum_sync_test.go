package contract

import (
	"path/filepath"
	"sort"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestEnumSyncAutoDiscovered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	goEnums, err := wirespec.DiscoverAPIStringEnums(root)
	contractcheck.FailErr(t, "discover API string enums in pkg/api", err)
	openAPI, err := wirespec.LoadOpenAPIEnums(root)
	contractcheck.FailErr(t, "load OpenAPI enum schemas from docs/openapi.yaml", err)
	tsPath := filepath.Join(root, "lycaon-den", "src", "api", "types.ts")
	tsEnums, err := wirespec.ParseTSEnumUnions(tsPath)
	contractcheck.FailErr(t, "parse TS enum unions in lycaon-den/src/api/types.ts", err)

	var names []string
	for name := range goEnums {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			goVals := goEnums[name]
			tsVals, ok := tsEnums[name]
			if !ok {
				t.Fatalf("missing TS enum type %s", name)
			}
			contractcheck.FailSetEqual(t, name+" Go vs TS", goVals, tsVals)

			oapiVals, ok := openAPI[name]
			if !ok {
				t.Fatalf("missing OpenAPI enum schema %s", name)
			}
			contractcheck.FailSetEqual(t, name+" Go vs OpenAPI "+name, goVals, oapiVals)
		})
	}
}
