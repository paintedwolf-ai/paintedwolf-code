package contract

import (
	"slices"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestUpdateProviderRequestAllowsPartialBody(t *testing.T) {
	t.Parallel()
	spec, err := wirespec.ReadSpec(contractcheck.RepoRoot(t))
	contractcheck.FailErr(t, "read docs/openapi.yaml", err)
	fields, err := spec.SchemaFields("UpdateProviderRequest")
	contractcheck.FailErr(t, "load UpdateProviderRequest properties", err)
	i := slices.IndexFunc(fields, func(f wirespec.Field) bool { return f.Name == "base_url" })
	if i < 0 {
		t.Fatal("UpdateProviderRequest.base_url is missing")
	}
	if !fields[i].Optional {
		t.Fatal("UpdateProviderRequest.base_url must be optional")
	}
}
