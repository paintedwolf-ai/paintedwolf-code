//go:build fieldfixture && darwin

package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/testutil/sourceoutage"
)

// The source sits on a soft NFS mount whose transport the fixture cuts; this
// is not a hard-mount cancellation or power-loss test. Checkpoint storage
// stays on local storage.
func TestSourceTransportOutagePreservesCheckpointAndRetry(t *testing.T) {
	volume := sourceoutage.Mount(t, sourceoutage.Soft)
	source := filepath.Join(volume.Dir, "source")
	testutil.FailErr(t, "create network source", os.Mkdir(source, 0o750))
	for i := range 3 {
		testutil.FailErr(t, "seed network source", os.WriteFile(filepath.Join(source, fmt.Sprintf("f%02d.txt", i)), []byte("base\n"), 0640))
	}
	store, promoter, preview := publishManyChanges(t, source, 3)
	before, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "load complete checkpoint", err)
	volume.Stop()
	// Require actual filesystem failure before interpreting the application result;
	// an all-cached success is not evidence of outage handling. A preview may still
	// answer from the client's cache; applying a promotion cannot.
	if _, err := os.Stat(filepath.Join(source, "uncached-outage-probe")); err == nil || os.IsNotExist(err) {
		t.Fatalf("source did not report transport failure: %v", err)
	}
	if _, err := promoteAndCommit(t, promoter, ApplyRequest{Preview: preview}); err == nil {
		t.Fatal("promotion accepted unavailable source")
	}
	after, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "reopen retained checkpoint during outage", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("outage changed published checkpoint")
	}
	volume.Restore()
	for i := range 3 {
		body, err := os.ReadFile(filepath.Join(source, fmt.Sprintf("f%02d.txt", i)))
		testutil.FailErr(t, "read source after failed promotion", err)
		if string(body) != "base\n" {
			t.Fatalf("failed promotion changed source: %q", body)
		}
	}
	preview, err = promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-scale"})
	testutil.FailErr(t, "renew preview after reconnect", err)
	_, err = promoteAndCommit(t, promoter, ApplyRequest{Preview: preview})
	testutil.FailErr(t, "retry retained checkpoint", err)
	for i := range 3 {
		body, err := os.ReadFile(filepath.Join(source, fmt.Sprintf("f%02d.txt", i)))
		testutil.FailErr(t, "read promoted source", err)
		if string(body) != "worker\n" {
			t.Fatalf("retry lost checkpoint content: %q", body)
		}
	}
}

// A hard mount holds the preview inside the kernel until the transport
// returns; the preview then completes from the same retained checkpoint.
func TestHardSourceTransportResumesWithoutLosingCheckpoint(t *testing.T) {
	volume := sourceoutage.Mount(t, sourceoutage.Hard)
	source := filepath.Join(volume.Dir, "source")
	testutil.FailErr(t, "create hard-mounted source", os.Mkdir(source, 0o750))
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(source, "f00.txt"), []byte("base\n"), 0640))
	store, promoter, _ := publishManyChanges(t, source, 1)
	before, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "load checkpoint before outage", err)
	volume.Stop()
	type outcome struct {
		preview *PromotionPreview
		err     error
	}
	result := make(chan outcome, 1)
	go func() {
		preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-scale"})
		result <- outcome{preview, err}
	}()
	volume.AwaitHeldCall()
	select {
	case outcome := <-result:
		t.Fatalf("hard-mounted preview completed while its source call was held: %v", outcome.err)
	default:
	}
	after, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "read retained local checkpoint while source waits", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("waiting preview changed checkpoint")
	}
	volume.Restore()
	recovered := <-result
	testutil.FailErr(t, "resume blocked preview", recovered.err)
	body, err := os.ReadFile(filepath.Join(source, "f00.txt"))
	testutil.FailErr(t, "read original source", err)
	if string(body) != "base\n" {
		t.Fatalf("preview mutated source: %q", body)
	}
	_, err = promoteAndCommit(t, promoter, ApplyRequest{Preview: recovered.preview})
	testutil.FailErr(t, "promote after hard-mount recovery", err)
	body, err = os.ReadFile(filepath.Join(source, "f00.txt"))
	testutil.FailErr(t, "read promoted source", err)
	if string(body) != "worker\n" {
		t.Fatalf("recovery lost checkpoint bytes: %q", body)
	}
}
