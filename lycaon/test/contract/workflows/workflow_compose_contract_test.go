package contract

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestWorkflowComposeOpenAPIPath(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	routes, err := wirespec.LoadOpenAPIRoutes(root)
	contractcheck.FailErr(t, "load OpenAPI routes from docs/openapi.yaml", err)
	found := false
	for _, r := range routes {
		if r.Method == "POST" && r.Path == "/v1/sessions/{id}/workflows/compose" && r.OperationID == "composeWorkflow" {
			found = true
		}
	}
	if !found {
		t.Fatal("composeWorkflow route missing from openapi")
	}
}

func TestWorkflowCompose422Schema(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	props, err := wirespec.LoadOpenAPISchemaProperties(root, "ComposeValidationDetails")
	contractcheck.FailErr(t, "loadOpenAPISchemaProperties failed", err)
	if !slices.Contains(props, "errors") {
		t.Fatal("ComposeValidationDetails missing errors")
	}
	errProps, err := wirespec.LoadOpenAPISchemaProperties(root, "ComposeValidationError")
	contractcheck.FailErr(t, "loadOpenAPISchemaProperties failed", err)
	for _, field := range []string{"field", "code", "message"} {
		if !slices.Contains(errProps, field) {
			t.Fatalf("ComposeValidationError missing %q", field)
		}
	}
}

func TestWorkflowComposeValidExtendsTestdata(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "test", "contract", "testdata", "workflow", "compose_session_plan.yaml")
	data, err := os.ReadFile(path)
	contractcheck.FailErr(t, "read file", err)
	if !strings.Contains(string(data), "extends: plan@1.0.0") {
		t.Fatal("testdata missing extends plan@1.0.0")
	}
}
