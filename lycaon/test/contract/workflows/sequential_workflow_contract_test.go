package contract

import (
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/wirespec"
)

func TestMessageWorkflowSpanFieldsInOpenAPI(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	props, err := wirespec.LoadOpenAPISchemaProperties(root, "Message")
	contractcheck.FailErr(t, "loadOpenAPISchemaProperties failed", err)
	want := map[string]bool{"workflow_run_id": false, "workflow_boundary": false, "kind": false}
	for _, p := range props {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for field, ok := range want {
		if !ok {
			t.Fatalf("Message missing %s in OpenAPI", field)
		}
	}
}
