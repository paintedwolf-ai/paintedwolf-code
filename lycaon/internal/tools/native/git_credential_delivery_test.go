package native

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
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
