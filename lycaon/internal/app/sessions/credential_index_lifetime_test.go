package sessions

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/app/security"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialEvidenceWaitersCancelWithoutReleasingActiveCapture(t *testing.T) {
	files := credentialFilesFixture(t)
	root := attachCredentialRoot(t, files)
	_, finish, err := files.index.begin(t.Context())
	testutil.FailErr(t, "hold evidence capture", err)
	defer finish()
	runtime := &security.Runtime{CredentialFiles: files}
	for _, operation := range []struct {
		name string
		run  func(context.Context) error
	}{
		{"provider", func(ctx context.Context) error {
			_, err := runtime.ModelScreeningValues(ctx, "proj-1", "chat")
			return err
		}},
		{"observer", func(ctx context.Context) error {
			return files.index.changed(ctx, repochange.Event{ProjectDir: root, Kind: repochange.WorktreeChanged})
		}},
		{"deletion", func(ctx context.Context) error { return files.ForgetProject(ctx, "proj-1") }},
	} {
		t.Run(operation.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- operation.run(ctx) }()
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled evidence admission = %v", err)
				}
			case <-t.Context().Done():
				t.Fatal("canceled admission waited for the active capture")
			}
		})
	}
	// Cancellation must not release or retire another operation's accounting.
	files.index.work.Stop()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := files.index.work.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("active capture disappeared from the drain: %v", err)
	}
}

func TestCredentialIndexShutdownCancelsAndDrainsAdmittedWork(t *testing.T) {
	files := credentialFilesFixture(t)
	ctx, finish, err := files.index.begin(t.Context())
	testutil.FailErr(t, "admit capture", err)
	result := make(chan error, 1)
	go func() { result <- files.closeCredentialIndex(t.Context()) }()
	select {
	case <-ctx.Done():
	case <-t.Context().Done():
		t.Fatal("shutdown did not cancel the capture")
	}
	select {
	case err := <-result:
		t.Fatalf("shutdown returned before the capture drained: %v", err)
	default:
	}
	finish()
	select {
	case err := <-result:
		testutil.FailErr(t, "drain index", err)
	case <-t.Context().Done():
		t.Fatal("shutdown did not finish after the capture drained")
	}
	if err := files.EnsureProject(t.Context(), "proj-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped index admitted project work: %v", err)
	}
}
