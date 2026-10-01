package tools

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/secretcap"
	"github.com/lycaon/lycaon/internal/testutil"
)

type recordedCredentialFiles struct {
	reads    []CredentialFileRead
	authored []AuthoredCredentialValues
}

func (r *recordedCredentialFiles) Delivered(_ context.Context, read CredentialFileRead) error {
	r.reads = append(r.reads, read)
	return nil
}

func (r *recordedCredentialFiles) Authored(_ context.Context, written AuthoredCredentialValues) error {
	r.authored = append(r.authored, written)
	return nil
}

func credentialFilesContext(t *testing.T, files CredentialFiles) (ToolContext, string) {
	t.Helper()
	root := t.TempDir()
	return ToolContext{
		ProjectID: "project", SessionID: "session", RootSessionID: "root-session", ToolCallID: "call",
		Roots:        []projectroot.RootRef{{ID: "root", Path: root, IsPrimary: true}},
		ActiveRootID: "root", CredentialFiles: files,
	}, root
}

// Only bytes the model put in its own arguments are authored; a value the
// host resolved from a reference is not, because the arguments hold its token.
func TestRecordModelAuthoredCredentialsKeepsOnlyArgumentBytes(t *testing.T) {
	files := &recordedCredentialFiles{}
	tc, root := credentialFilesContext(t, files)
	const reference = "{{paintedwolf-secret:6f0c7f3e-0d59-4a55-9d64-1b2a3c4d5e6f}}"
	tc.CanonicalArgs = map[string]any{
		"path":    ".env",
		"content": "OIDC_CLIENT_ID=todo-web-client\nSESSION_SECRET=" + reference + "\n",
	}
	tc.Secrets = secretcap.NewResolutionForTest(tc.CanonicalArgs, []secretcap.TestResolvedValue{
		{ID: "6f0c7f3e-0d59-4a55-9d64-1b2a3c4d5e6f", Name: "session secret", Value: "a0VBKPXTbWzsjo2Il49ko0ng", Path: "/content"},
	})
	landed := "OIDC_CLIENT_ID=todo-web-client\nSESSION_SECRET=a0VBKPXTbWzsjo2Il49ko0ng\n"
	tc.RecordModelAuthoredCredentials(context.Background(), filepath.Join(root, ".env"), []byte(landed))
	if len(files.authored) != 1 {
		t.Fatalf("authored records = %+v", files.authored)
	}
	got := files.authored[0]
	if got.RootID != "root" || got.Path != ".env" || got.RootSessionID != "root-session" || got.ToolCallID != "call" {
		t.Fatalf("authorship identity = %+v", got)
	}
	if len(got.Values) != 1 || got.Values[0] != "todo-web-client" {
		t.Fatalf("authored values = %q, want only the argument literal", got.Values)
	}
}

func TestRecordModelAuthoredCredentialsIgnoresOrdinaryFiles(t *testing.T) {
	files := &recordedCredentialFiles{}
	tc, root := credentialFilesContext(t, files)
	tc.CanonicalArgs = map[string]any{"path": "config.txt", "content": "CLIENT_ID=todo-web-client\n"}
	tc.RecordModelAuthoredCredentials(context.Background(), filepath.Join(root, "config.txt"), []byte("CLIENT_ID=todo-web-client\n"))
	if len(files.authored) != 0 {
		t.Fatalf("a non-credential file recorded authorship: %+v", files.authored)
	}
}

func TestObserveCredentialReadAddressesTheRootFile(t *testing.T) {
	files := &recordedCredentialFiles{}
	tc, root := credentialFilesContext(t, files)
	testutil.FailErr(t, "observe readme", tc.ObserveCredentialRead(context.Background(), filepath.Join(root, "README.md"), "README.md", "KEY=value-value"))
	if len(files.reads) != 0 {
		t.Fatalf("an ordinary read reached the evidence base: %+v", files.reads)
	}
	testutil.FailErr(t, "observe .env", tc.ObserveCredentialRead(context.Background(), filepath.Join(root, "web", ".env"), "web/.env", "KEY=value-value"))
	if len(files.reads) != 1 {
		t.Fatalf("reads = %+v", files.reads)
	}
	read := files.reads[0]
	if read.RootID != "root" || read.Path != "web/.env" || read.Container != "web/.env" ||
		read.RootSessionID != "root-session" || read.Content != "KEY=value-value" {
		t.Fatalf("read = %+v", read)
	}
}
