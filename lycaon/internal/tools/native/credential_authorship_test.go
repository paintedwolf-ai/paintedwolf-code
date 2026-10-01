package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

type authoredRecorder struct{ authored []tools.AuthoredCredentialValues }

func (r *authoredRecorder) Delivered(context.Context, tools.CredentialFileRead) error { return nil }

func (r *authoredRecorder) Authored(_ context.Context, written tools.AuthoredCredentialValues) error {
	r.authored = append(r.authored, written)
	return nil
}

// A landed credential-file write records the values its own arguments carried,
// so reading the file back does not ask about the model's own output.
func TestWriteRecordsModelAuthoredCredentialValues(t *testing.T) {
	dir := t.TempDir()
	recorder := &authoredRecorder{}
	tctx := nativefixture.Context(dir)
	tctx.ProjectID = "project"
	tctx.CredentialFiles = recorder
	args := map[string]any{"path": ".env", "content": "OIDC_CLIENT_ID=todo-web-client\nWEB_PORT=3000\n"}
	tctx.CanonicalArgs = args
	tool := &WriteTool{Boundary: nativefixture.Boundary(t)}
	_, err := tool.Run(context.Background(), args, tctx)
	testutil.FailErr(t, "write .env", err)
	written, err := os.ReadFile(filepath.Join(dir, ".env"))
	testutil.FailErr(t, "read .env", err)
	if string(written) != args["content"] {
		t.Fatalf("file = %q", written)
	}
	if len(recorder.authored) != 1 {
		t.Fatalf("authorship records = %+v", recorder.authored)
	}
	got := recorder.authored[0]
	if got.Path != ".env" || got.RootID != "r1" || len(got.Values) != 1 || got.Values[0] != "todo-web-client" {
		t.Fatalf("authored = %+v, want the one harvestable argument value", got)
	}
}
