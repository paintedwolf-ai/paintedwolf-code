package contract

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/scan"
	scancadence "github.com/lycaon/lycaon/internal/scan/cadence"
	scancfg "github.com/lycaon/lycaon/internal/scan/configuration"
	scantoolapi "github.com/lycaon/lycaon/internal/scan/toolapi"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil/scantest"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/toolschema"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestScanToolsContractAllowlistAndRegistration(t *testing.T) {
	cfg, err := tools.LoadToolsConfig()
	contractcheck.FailErr(t, "tools.LoadToolsConfig failed", err)
	allowed := map[string]bool{}
	for _, name := range cfg.Tools {
		allowed[name] = true
	}
	for _, name := range []string{"scan_pack", "scan_list", "scan_summary", "scan_query", "scan_compare"} {
		if !allowed[name] {
			t.Fatalf("%s missing from lycaon-tools.yaml", name)
		}
	}

	sqlDB := testdbfixture.Open(t, "store.db")

	schemas, err := toolschema.LoadSchemaDir(filepath.Join("..", "..", "..", "config", "packs", "painted-wolf", "platform", "tools", "schemas"))
	contractcheck.FailErr(t, "load scan tool schemas", err)
	reg, err := tools.NewCatalogRegistry(schemas)
	contractcheck.FailErr(t, "new catalog registry", err)
	coord := scantest.Coordinator(t, scan.NewSQLStore(sqlDB), nil)
	if err := scantoolapi.RegisterScanTools(reg, coord, &scan.MockRegistry{Scanner: &scan.MockScanner{}}, scancadence.New(scan.StoreFromCoordinator(coord), coord, &scan.MockRegistry{Scanner: &scan.MockScanner{}}, nil, scancfg.DefaultGatesConfig(), nil), nil, nil, nil); err != nil {
		contractcheck.FailErr(t, "scan.RegisterScanTools failed", err)
	}
	meta, ok := reg.Meta("scan_pack")
	if !ok {
		t.Fatal("scan_pack not registered")
	}
	if meta.ArgsSchema == nil {
		t.Fatal("scan_pack missing args schema")
	}
	enum := schemaCategoryEnum(t, meta.ArgsSchema)
	if !enum["security"] || !enum["lint"] {
		t.Fatalf("schema enum = %v, want catalog categories including security and lint", enum)
	}
	if !strings.Contains(meta.Description, "code scanners") {
		t.Fatalf("description = %q", meta.Description)
	}
	for _, name := range []string{"scan_list", "scan_summary", "scan_query"} {
		meta, ok := reg.Meta(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if meta.ArgsSchema == nil {
			t.Fatalf("%s missing args schema", name)
		}
	}
	summaryMeta, _ := reg.Meta("scan_summary")
	t.Run("registered selectors", func(t *testing.T) {
		assertScanSummarySelectors(t, summaryMeta.ArgsSchema)
	})
	t.Run("model-visible selectors", func(t *testing.T) {
		assertScanSummarySelectors(t, tools.TrimCoordinatorToolMeta(summaryMeta).ArgsSchema)
	})
}

func assertScanSummarySelectors(t *testing.T, schema map[string]any) {
	t.Helper()
	required, _ := toolschema.ArgFieldSummary(schema)
	if len(required) != 0 {
		t.Fatalf("scan_summary required = %v, want optional scan_ids and pass_id alternatives", required)
	}
	scanIDs := nestedSchema(t, schema, "properties", "scan_ids")
	if scanIDs["type"] != "array" || scanIDs["minItems"] != 1 || scanIDs["uniqueItems"] != true {
		t.Fatalf("scan_summary scan_ids = %v, want a unique nonempty array", scanIDs)
	}
	if items := nestedSchema(t, scanIDs, "items"); items["type"] != "string" {
		t.Fatalf("scan_summary scan_ids items = %v, want strings", items)
	}
	if passID := nestedSchema(t, schema, "properties", "pass_id"); passID["type"] != "string" {
		t.Fatalf("scan_summary pass_id = %v, want string", passID)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("scan_summary additionalProperties = %v, want false", schema["additionalProperties"])
	}
}

// TestScanQueryRequestShapeMatchesGo binds the ScanQuery wire schema to its Go
// twin: the openapi property set for each schema must equal the JSON field set
// of the corresponding pkg/api struct, so a field added or renamed on either
// side without the other fails here rather than drifting silently.
func TestScanQueryRequestShapeMatchesGo(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, tc := range []struct {
		schema string
		goType reflect.Type
	}{
		{"ScanQueryRequest", reflect.TypeOf(api.ScanQueryRequest{})},
		{"ScanQueryResponse", reflect.TypeOf(api.ScanQueryResponse{})},
	} {
		props, err := wirespec.LoadOpenAPISchemaProperties(root, tc.schema)
		contractcheck.FailErr(t, "loadOpenAPISchemaProperties "+tc.schema, err)
		schemaFields := map[string]bool{}
		for _, name := range props {
			schemaFields[name] = true
		}
		goFields := map[string]bool{}
		for _, name := range wirespec.JsonFieldNames(tc.goType) {
			goFields[name] = true
		}
		for f := range goFields {
			if !schemaFields[f] {
				t.Fatalf("%s: Go json field %q missing from openapi schema", tc.schema, f)
			}
		}
		for f := range schemaFields {
			if !goFields[f] {
				t.Fatalf("%s: openapi property %q missing from Go struct", tc.schema, f)
			}
		}
	}
}
