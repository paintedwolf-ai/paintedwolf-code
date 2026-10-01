package survey

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

type deliveredRecorder struct {
	reads []tools.CredentialFileRead
	err   error
}

func (r *deliveredRecorder) Delivered(_ context.Context, read tools.CredentialFileRead) error {
	r.reads = append(r.reads, read)
	return r.err
}

func (r *deliveredRecorder) Authored(context.Context, tools.AuthoredCredentialValues) error {
	return nil
}

func seedCredentialRead(t *testing.T, body string) (string, tools.ToolContext, *deliveredRecorder) {
	t.Helper()
	dir := t.TempDir()
	testutil.FailErr(t, "seed .env", os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600))
	recorder := &deliveredRecorder{}
	tctx := nativefixture.Context(dir)
	tctx.ProjectID = "project"
	tctx.CredentialFiles = recorder
	return dir, tctx, recorder
}

// A credential-file read hands the evidence base the text it delivered, and an
// ordinary file read hands it nothing.
func TestReadDeliversCredentialFileTextToEvidence(t *testing.T) {
	const body = "API_TOKEN=person-typed-token\n"
	dir, tctx, recorder := seedCredentialRead(t, body)
	testutil.FailErr(t, "seed readme", os.WriteFile(filepath.Join(dir, "README.md"), []byte("TOKEN=not-a-container\n"), 0o644))
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Escalation: NewReadEscalationStore()}
	_, err := tool.Run(context.Background(), map[string]any{"path": "README.md"}, tctx)
	testutil.FailErr(t, "read readme", err)
	_, err = tool.Run(context.Background(), map[string]any{"path": ".env"}, tctx)
	testutil.FailErr(t, "read .env", err)
	if len(recorder.reads) != 1 {
		t.Fatalf("delivered reads = %+v", recorder.reads)
	}
	read := recorder.reads[0]
	if read.Path != ".env" || read.RootID != "r1" || read.Content != body ||
		read.ProjectID != "project" || read.SessionID != tctx.SessionID {
		t.Fatalf("delivered = %+v", read)
	}
}

// Text whose exposure could not be recorded is not returned.
func TestReadWithholdsCredentialFileWhenExposureFails(t *testing.T) {
	const body = "API_TOKEN=person-typed-token\n"
	_, tctx, recorder := seedCredentialRead(t, body)
	recorder.err = errors.New("evidence store unavailable")
	tool := &ReadTool{Boundary: nativefixture.Boundary(t), Escalation: NewReadEscalationStore()}
	out, err := tool.Run(context.Background(), map[string]any{"path": ".env"}, tctx)
	if err == nil {
		t.Fatal("a credential read succeeded without its exposure record")
	}
	if strings.Contains(out, "person-typed-token") {
		t.Fatalf("the unrecorded read returned its text: %q", out)
	}
}
