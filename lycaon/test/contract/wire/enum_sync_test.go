package contract

import (
	"path/filepath"
	"sort"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// anchorEnums must be discovered; an empty or partial discovery fails instead of
// passing on nothing.
var anchorEnums = []string{
	"BlueprintStatus", "CheckpointKind", "CodeScanStatus", "LegStatus",
	"SessionPosture", "SessionStatus", "WorkerStatus", "WorkerSummaryStatus",
}

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

	for _, name := range anchorEnums {
		if len(goEnums[name]) < 2 {
			t.Errorf("anchor enum %s discovered with %d values; discovery must see every enum", name, len(goEnums[name]))
		}
	}

	var names []string
	for name := range goEnums {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			goVals := goEnums[name]
			seen := make(map[string]bool, len(goVals))
			for _, v := range goVals {
				if v == "" || seen[v] {
					t.Fatalf("%s holds an empty or duplicate value %q", name, v)
				}
				seen[v] = true
			}
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
