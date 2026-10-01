package contract

import (
	"fmt"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

// TestStructSyncAutoDiscovered tri-syncs exported pkg/api structs across Go, OpenAPI,
// and types.ts, including field optionality (omitempty / TS ? / OpenAPI required).
func TestStructSyncAutoDiscovered(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	catalog, err := wirespec.WireStructCatalog(root)
	contractcheck.FailErr(t, "discover wire structs in pkg/api", err)
	if len(catalog.Specs) == 0 {
		t.Fatal("no wire structs discovered in pkg/api")
	}
	for _, spec := range catalog.Specs {
		t.Run(spec.GoName, func(t *testing.T) {
			t.Parallel()
			if err := wirespec.SyncWireStructFields(root, spec); err != nil {
				contractcheck.FailErr(t, "sync wire struct fields across Go, OpenAPI, and TS", err)
			}
		})
	}
}

// TestStructSyncNoUnaccountedStructs closes the Go→spec direction: a tagged
// pkg/api struct reaches both wire projections or claims a declared exemption.
func TestStructSyncNoUnaccountedStructs(t *testing.T) {
	t.Parallel()
	catalog, err := wirespec.WireStructCatalog(contractcheck.RepoRoot(t))
	contractcheck.FailErr(t, "discover wire structs in pkg/api", err)
	var violations []string
	for _, gap := range catalog.Gaps {
		violations = append(violations, fmt.Sprintf("%s: %s", gap.GoName, gap.Reason()))
	}
	contractcheck.FailViolations(t, "pkg/api structs with JSON tags that reach no wire projection"+
		"\nfix: publish the schema under docs/openapi/components/schemas and rerun"+
		" ./task openapi:bundle && ./task codegen:den-types, or declare the struct in"+
		" structGoInternal / structOneOfCarrier with a reason", violations)
}

// TestStructSyncUntaggedStructsAccounted catches structs that would marshal with
// Go field names: no JSON tag means PascalCase on a snake_case wire.
func TestStructSyncUntaggedStructsAccounted(t *testing.T) {
	t.Parallel()
	scan, err := wirespec.CachedDiscoverAPIStructFields(contractcheck.RepoRoot(t))
	contractcheck.FailErr(t, "discover wire structs in pkg/api", err)
	var violations []string
	for _, name := range scan.Untagged {
		if wirespec.StructExempt(name) {
			continue
		}
		violations = append(violations, name+": exported struct in pkg/api with no JSON-tagged fields")
	}
	contractcheck.FailViolations(t, "pkg/api structs that would marshal with Go field names"+
		"\nfix: add snake_case json tags and publish a schema, or declare the struct in"+
		" structGoInternal / structOneOfCarrier with a reason", violations)
}

// TestStructSyncEmbeddedStructsAccounted catches embedding: the flat AST scan
// cannot resolve promoted fields, so the discovered shape would be short.
func TestStructSyncEmbeddedStructsAccounted(t *testing.T) {
	t.Parallel()
	scan, err := wirespec.CachedDiscoverAPIStructFields(contractcheck.RepoRoot(t))
	contractcheck.FailErr(t, "discover wire structs in pkg/api", err)
	var violations []string
	for _, name := range scan.Embedded {
		if wirespec.StructExempt(name) {
			continue
		}
		violations = append(violations, name+": embeds a field; discovered wire shape would omit promoted fields")
	}
	contractcheck.FailViolations(t, "pkg/api structs with embedded fields"+
		"\nfix: spell the promoted fields out with explicit json tags, or declare the"+
		" struct in structGoInternal / structOneOfCarrier with a reason", violations)
}

// TestStructSyncExemptionsLive keeps the exemption maps from outliving their
// structs; a stale entry would excuse a name a later struct reuses.
func TestStructSyncExemptionsLive(t *testing.T) {
	t.Parallel()
	scan, err := wirespec.CachedDiscoverAPIStructFields(contractcheck.RepoRoot(t))
	contractcheck.FailErr(t, "discover wire structs in pkg/api", err)
	live := make(map[string]struct{}, len(scan.Tagged)+len(scan.Untagged))
	for name := range scan.Tagged {
		live[name] = struct{}{}
	}
	for _, name := range scan.Untagged {
		live[name] = struct{}{}
	}
	for _, name := range scan.Empty {
		live[name] = struct{}{}
	}
	var violations []string
	for name, reason := range wirespec.StructExemptions() {
		if _, ok := live[name]; !ok {
			violations = append(violations, name+": no such exported struct in pkg/api")
			continue
		}
		if reason == "" {
			violations = append(violations, name+": exempted from the tri-sync without a reason")
		}
	}
	for name := range wirespec.StructGoInternal {
		if _, both := wirespec.StructOneOfCarrier[name]; both {
			violations = append(violations, name+": declared in both structGoInternal and structOneOfCarrier")
		}
	}
	contractcheck.FailViolations(t, "stale, unexplained, or duplicated tri-sync exemptions", violations)
}
