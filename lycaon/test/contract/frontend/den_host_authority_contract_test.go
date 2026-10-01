package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

type denHostAuthorityRegistry struct {
	OpenAPISchemas map[string][]string `json:"openapi_schemas"`
	ToolDecision   []string            `json:"tool_decision_fields"`
}

func loadDenHostAuthorityRegistry(t *testing.T) denHostAuthorityRegistry {
	t.Helper()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "lycaon", "test", "contract", "testdata", "den_host_authority.json"))
	contractcheck.FailErr(t, "read den_host_authority.json", err)
	var reg denHostAuthorityRegistry
	contractcheck.FailErr(t, "unmarshal den_host_authority.json", json.Unmarshal(data, &reg))
	return reg
}

func TestDenHostAuthorityFieldsInOpenAPI(t *testing.T) {
	t.Parallel()
	reg := loadDenHostAuthorityRegistry(t)
	root := contractcheck.RepoRoot(t)
	schemas, err := wirespec.LoadOpenAPIObjectSchemas(root)
	contractcheck.FailErr(t, "load OpenAPI object schemas", err)

	for schema, fields := range reg.OpenAPISchemas {
		obj, ok := schemas[schema]
		if !ok {
			t.Fatalf("openapi schema %q missing", schema)
		}
		for _, field := range fields {
			if _, ok := obj.Properties[field]; !ok {
				t.Fatalf("schema %q missing property %q", schema, field)
			}
		}
	}
}

func TestDenHostAuthorityToolFieldsOnWireDTO(t *testing.T) {
	t.Parallel()
	reg := loadDenHostAuthorityRegistry(t)
	root := contractcheck.RepoRoot(t)
	tsPath := filepath.Join(root, "lycaon-den", "src", "api", "types.ts")
	ts, err := os.ReadFile(tsPath)
	contractcheck.FailErr(t, "read types.ts", err)
	body := string(ts)
	for _, dotted := range reg.ToolDecision {
		parts := strings.Split(dotted, ".")
		if len(parts) != 2 || parts[0] != "tool_result" {
			continue
		}
		field := parts[1]
		if !strings.Contains(body, field+":") && !strings.Contains(body, field+"?:") {
			t.Fatalf("types.ts ToolResult missing field %q from den host authority registry", field)
		}
	}
}
