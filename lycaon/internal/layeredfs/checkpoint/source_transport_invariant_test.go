package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// An external fixture controller interrupts the actual NFS transport. The
// source mount uses bounded error-return semantics; this is not a hard-mount
// cancellation or power-loss test. Checkpoint storage stays on local storage.
func TestSourceTransportOutagePreservesCheckpointAndRetry(t *testing.T) {
	parent, control := os.Getenv("PW_M14_OUTAGE_SOURCE_PARENT"), os.Getenv("PW_M14_OUTAGE_CONTROL")
	if parent == "" && control == "" {
		t.Skip("requires explicitly provisioned network outage fixture")
	}
	if parent == "" || control == "" {
		t.Fatal("both outage fixture paths are required")
	}
	waitOutageMarker(t, control, "armed.json")
	source, err := os.MkdirTemp(parent, "checkpoint-outage-")
	testutil.FailErr(t, "create network source", err)
	// Restore before source cleanup, even when an assertion fails.
	t.Cleanup(func() { _ = os.RemoveAll(source) })
	t.Cleanup(func() {
		testutil.FailErr(t, "request fixture restoration", os.WriteFile(filepath.Join(control, "restore.request"), nil, 0600))
		waitOutageMarker(t, control, "restored.json")
	})
	for i := range 3 {
		testutil.FailErr(t, "seed network source", os.WriteFile(filepath.Join(source, fmt.Sprintf("f%02d.txt", i)), []byte("base\n"), 0640))
	}
	store, promoter, preview := publishManyChanges(t, source, 3)
	before, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "load complete checkpoint", err)
	testutil.FailErr(t, "request transport stop", os.WriteFile(filepath.Join(control, "stop.request"), nil, 0600))
	waitOutageMarker(t, control, "stopped.json")
	// Require actual filesystem failure before interpreting the application result;
	// an all-cached success is not evidence of outage handling.
	if _, err := os.Stat(filepath.Join(source, "uncached-outage-probe")); err == nil || os.IsNotExist(err) {
		t.Fatalf("source did not report transport failure: %v", err)
	}
	if _, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-scale"}); err == nil {
		t.Fatal("preview treated unavailable source as usable")
	}
	if _, err := promoteAndCommit(t, promoter, ApplyRequest{Preview: preview}); err == nil {
		t.Fatal("promotion accepted unavailable source")
	}
	if _, err := os.Stat(filepath.Join(control, "restored.json")); !os.IsNotExist(err) {
		t.Fatalf("outage ended before failure assertions completed: %v", err)
	}
	after, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "reopen retained checkpoint during outage", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("outage changed published checkpoint")
	}
	testutil.FailErr(t, "restore source transport", os.WriteFile(filepath.Join(control, "restore.request"), nil, 0600))
	waitOutageMarker(t, control, "restored.json")
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

func waitOutageMarker(t *testing.T, control, name string) {
	t.Helper()
	deadline := time.Now().Add(80 * time.Second)
	for {
		_, err := os.Stat(filepath.Join(control, name))
		if err == nil {
			return
		}
		if !os.IsNotExist(err) {
			t.Fatalf("read outage marker %s: %v", name, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("outage marker %s not received", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Hard mounts may wait inside the kernel despite a canceled Go context. The
// independent fixture watchdog bounds the outage even if this test exits.
func TestHardSourceTransportResumesWithoutLosingCheckpoint(t *testing.T) {
	parent, control := os.Getenv("PW_M14_HARD_SOURCE_PARENT"), os.Getenv("PW_M14_OUTAGE_CONTROL")
	if parent == "" {
		t.Skip("requires explicitly provisioned hard network mount")
	}
	if control == "" {
		t.Fatal("outage control path required")
	}
	waitOutageMarker(t, control, "armed.json")
	source, err := os.MkdirTemp(parent, "checkpoint-hard-")
	testutil.FailErr(t, "create hard-mounted source", err)
	t.Cleanup(func() { _ = os.RemoveAll(source) })
	t.Cleanup(func() {
		testutil.FailErr(t, "request fixture restoration", os.WriteFile(filepath.Join(control, "restore.request"), nil, 0600))
		waitOutageMarker(t, control, "restored.json")
	})
	testutil.FailErr(t, "seed source", os.WriteFile(filepath.Join(source, "f00.txt"), []byte("base\n"), 0640))
	store, promoter, _ := publishManyChanges(t, source, 1)
	before, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "load checkpoint before outage", err)
	testutil.FailErr(t, "request transport stop", os.WriteFile(filepath.Join(control, "stop.request"), nil, 0600))
	waitOutageMarker(t, control, "stopped.json")
	type outcome struct {
		preview *PromotionPreview
		err     error
	}
	result := make(chan outcome, 1)
	entered := make(chan struct{})
	go func() {
		close(entered)
		preview, err := promoter.Preview(t.Context(), PreviewRequest{CheckpointID: "cp-scale"})
		result <- outcome{preview, err}
	}()
	<-entered
	select {
	case outcome := <-result:
		t.Fatalf("hard-mounted preview completed while transport was stopped: %v", outcome.err)
	case <-time.After(500 * time.Millisecond):
	}
	after, err := store.LoadManifest("cp-scale")
	testutil.FailErr(t, "read retained local checkpoint while source waits", err)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("waiting preview changed checkpoint")
	}
	testutil.FailErr(t, "restore transport", os.WriteFile(filepath.Join(control, "restore.request"), nil, 0600))
	waitOutageMarker(t, control, "restored.json")
	var recovered outcome
	select {
	case recovered = <-result:
	case <-time.After(120 * time.Second):
		t.Fatal("preview did not recover after source transport returned")
	}
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
