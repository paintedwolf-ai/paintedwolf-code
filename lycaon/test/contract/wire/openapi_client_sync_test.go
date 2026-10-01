package contract

import (
	"path/filepath"
	"sort"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// Every Den client method is named for the one OpenAPI operation it calls.
func TestLycaonClientMethodsAreOperations(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	operationIDs, err := wirespec.LoadOpenAPIOperationIDs(root)
	contractcheck.FailErr(t, "load OpenAPI operationIds", err)
	operations := make(map[string]struct{}, len(operationIDs))
	for _, id := range operationIDs {
		operations[id] = struct{}{}
	}
	methods, err := wirespec.ParseTSClientMethods(filepath.Join(root, "lycaon-den", "src", "api", "client.ts"))
	contractcheck.FailErr(t, "parse LycaonClient methods from lycaon-den/src/api/client.ts", err)

	var unnamed []string
	for method := range methods {
		if _, ok := operations[method]; !ok {
			unnamed = append(unnamed, method)
		}
	}
	sort.Strings(unnamed)
	if len(unnamed) > 0 {
		t.Fatalf("LycaonClient methods not named for an OpenAPI operationId: %v", unnamed)
	}
}
