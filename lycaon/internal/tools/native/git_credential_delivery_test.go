package native

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
	"github.com/lycaon/lycaon/internal/tools"
	nativefixture "github.com/lycaon/lycaon/internal/tools/native/internal/testfixture"
)

type gitCredentialRecorder struct {
	reads []tools.CredentialFileRead
	err   error
}

func (*gitCredentialRecorder) EnsureProject(context.Context, string) error { return nil }
func (r *gitCredentialRecorder) Delivered(_ context.Context, read tools.CredentialFileRead) error {
	r.reads = append(r.reads, read)
	return r.err
}
func (*gitCredentialRecorder) Authored(context.Context, tools.AuthoredCredentialValues) error {
	return nil
}

func TestGitPatchCredentialDeliveryUsesFileHeaders(t *testing.T) {
	tc := nativefixture.Context(t.TempDir())
	recorder := &gitCredentialRecorder{}
	tc.Effects.CredentialFiles = recorder
	patch := "diff --git a/.env b/.env\n--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-TOKEN=old-value-4411\n+TOKEN=new-value-5533\n--- a/.aws/not-a-header\n"
	if err := deliverGitPatchCredentials(t.Context(), tc, patch); err != nil {
		t.Fatalf("deliver patch: %v", err)
	}
	if len(recorder.reads) != 1 || recorder.reads[0].Path != ".env" {
		t.Fatalf("attribution=%+v", recorder.reads)
	}
	recorder.err = errors.New("exposure unavailable")
	if err := deliverGitPatchCredentials(t.Context(), tc, patch); !errors.Is(err, recorder.err) {
		t.Fatalf("delivery error=%v", err)
	}
}

func TestGitPatchCredentialDeliveryPreservesLeadingDirectory(t *testing.T) {
	tc := nativefixture.Context(t.TempDir())
	recorder := &gitCredentialRecorder{}
	tc.Effects.CredentialFiles = recorder
	patch := "diff --git a/b/.env b/b/.env\n--- a/b/.env\n+++ b/b/.env\n@@ -1 +1 @@\n-TOKEN=old-value-4411\n+TOKEN=new-value-5533\n"
	if err := deliverGitPatchCredentials(t.Context(), tc, patch); err != nil {
		testutil.FailErr(t, "deliverGitPatchCredentials failed", err)
	}
	if len(recorder.reads) != 1 || recorder.reads[0].Path != "b/.env" {
		t.Fatalf("attribution=%+v", recorder.reads)
	}
}

func TestGitPatchCredentialDeliveryUsesActiveAttachedRoot(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	tc := nativefixture.Context(first)
	tc.Source.Roots = []projectroot.RootRef{
		{ID: "first", Label: "first", Path: first, IsPrimary: true},
		{ID: "second", Label: "second", Path: second},
	}
	tc.Source.ActiveRootID = "second"
	recorder := &gitCredentialRecorder{}
	tc.Effects.CredentialFiles = recorder
	patch := "diff --git a/.env b/.env\n--- a/.env\n+++ b/.env\n@@ -1 +1 @@\n-TOKEN=old\n+TOKEN=new\n"
	if err := deliverGitPatchCredentials(t.Context(), tc, patch); err != nil {
		testutil.FailErr(t, "deliverGitPatchCredentials failed", err)
	}
	if len(recorder.reads) != 1 || recorder.reads[0].RootID != "second" || recorder.reads[0].Path != ".env" {
		t.Fatalf("attribution=%+v", recorder.reads)
	}
}

func (*gitCredentialRecorder) ForgetProject(context.Context, string) error { return nil }

func TestGitPatchCredentialDeliveryDecodesQuotedHeadersAndSkipsDevNull(t *testing.T) {
	tc := nativefixture.Context(t.TempDir())
	recorder := &gitCredentialRecorder{}
	tc.Effects.CredentialFiles = recorder
	patch := "diff --git \"a/sp ace/.env\" \"b/sp ace/.env\"\n--- \"a/sp ace/.env\"\n+++ \"b/sp ace/.env\"\n@@ -1 +1 @@\n-TOKEN=old-value-4411\n+TOKEN=new-value-5533\n" +
		"diff --git a/.aws/credentials b/.aws/credentials\nnew file mode 100644\n--- /dev/null\n+++ b/.aws/credentials\n@@ -0,0 +1 @@\n+aws_secret_access_key=value-7788\n" +
		"diff --git a/config/.env b/config/.env\n--- \"a/config/.env\\q\"\n+++ b/config/.env\n@@ -1 +1 @@\n-TOKEN=old\n+TOKEN=new\n"
	testutil.FailErr(t, "deliverGitPatchCredentials failed", deliverGitPatchCredentials(t.Context(), tc, patch))
	var paths []string
	for _, read := range recorder.reads {
		paths = append(paths, read.Path)
	}
	if len(paths) != 3 || paths[0] != "sp ace/.env" || paths[1] != ".aws/credentials" || paths[2] != "config/.env" {
		t.Fatalf("attribution=%+v", recorder.reads)
	}
	tc.Source.Roots = nil
	if err := deliverGitPatchCredentials(t.Context(), tc, patch); !errors.Is(err, projectroot.ErrNoProjectRoots) {
		t.Fatalf("delivery without roots=%v", err)
	}
}

// git_show and git_blame deny credential paths before any delivery, so blob and
// blame reads deliver nothing; a commit's patch attributes through its headers.
func TestGitShowDeliversCommitPatchCredentialsAndBlobReadsDeliverNothing(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	testutil.FailErr(t, "write credential file", os.WriteFile(filepath.Join(dir, ".env"), []byte("TOKEN=person-value-4411\n"), 0o600))
	testutil.FailErr(t, "write notes", os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("plain notes\n"), 0o644))
	gittest.CommitAll(t, dir, "Add the environment file")
	tc := nativefixture.Context(dir)
	recorder := &gitCredentialRecorder{}
	tc.Effects.CredentialFiles = recorder
	manager := git.NewManager()
	show := &GitShowTool{Git: manager}
	blame := &GitBlameTool{Git: manager}

	_, err := show.Run(t.Context(), map[string]any{"path": "notes.txt"}, tc)
	testutil.FailErr(t, "show the blob", err)
	_, err = show.Run(t.Context(), map[string]any{"path": "notes.txt", "stat": true}, tc)
	testutil.FailErr(t, "show the stat", err)
	_, err = blame.Run(t.Context(), map[string]any{"path": "notes.txt"}, tc)
	testutil.FailErr(t, "blame the file", err)
	if len(recorder.reads) != 0 {
		t.Fatalf("plain reads delivered=%+v", recorder.reads)
	}
	if _, err := show.Run(t.Context(), map[string]any{"path": ".env"}, tc); err == nil {
		t.Fatal("git_show returned a credential file")
	}
	_, err = show.Run(t.Context(), map[string]any{}, tc)
	testutil.FailErr(t, "show the commit", err)
	if len(recorder.reads) != 1 || recorder.reads[0].Path != ".env" || recorder.reads[0].RootID != "r1" || recorder.reads[0].Content == "" {
		t.Fatalf("commit patch deliveries=%+v", recorder.reads)
	}

	recorder.err = errors.New("exposure unavailable")
	if _, err := show.Run(t.Context(), map[string]any{}, tc); !errors.Is(err, recorder.err) {
		t.Fatalf("patch delivery error=%v", err)
	}
}
