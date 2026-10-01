package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestWorkflowScopeEnumSync(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	goEnums, err := wirespec.DiscoverAPIStringEnums(root)
	contractcheck.FailErr(t, "discover API string enums in pkg/api", err)
	want := []string{
		string(api.WorkflowScopeBundled),
		string(api.WorkflowScopeProject),
		string(api.WorkflowScopeSession),
	}
	contractcheck.FailSetEqual(t, "Go WorkflowScope enum", goEnums["WorkflowScope"], want)

	openAPI, err := wirespec.LoadOpenAPIEnums(root)
	contractcheck.FailErr(t, "load OpenAPI enum schemas from docs/openapi.yaml", err)
	contractcheck.FailSetEqual(t, "OpenAPI WorkflowScope enum", openAPI["WorkflowScope"], want)

	tsPath := filepath.Join(root, "lycaon-den", "src", "api", "types.ts")
	tsEnums, err := wirespec.ParseTSEnumUnions(tsPath)
	contractcheck.FailErr(t, "parse TS enum unions in lycaon-den/src/api/types.ts", err)
	contractcheck.FailSetEqual(t, "TS WorkflowScope enum", tsEnums["WorkflowScope"], want)
}

func TestListWorkflowsDocumentsSessionIDQuery(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "docs", "openapi.yaml"))
	contractcheck.FailErr(t, "read file", err)
	text := string(data)
	if !strings.Contains(text, "/v1/workflows:") || !strings.Contains(text, "session_id") {
		t.Fatal("openapi.yaml must document session_id on GET /v1/workflows")
	}
}
