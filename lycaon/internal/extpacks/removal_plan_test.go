package extpacks

import (
	"bytes"
	"os"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPrepareRemovalValidatesWholeSelection(t *testing.T) {
	seedConfigDir(t)
	path, err := DeviceDesiredPath()
	testutil.FailErr(t, "desired path", err)
	desired := desiredWithExtensionPacks("acme/one", "acme/two", "acme/keep")
	data, err := EncodeDesired(desired)
	testutil.FailErr(t, "encode desired", err)
	testutil.FailErr(t, "seed desired", os.WriteFile(path, data, 0o600))
	for _, ids := range [][]string{nil, {"acme/one", "acme/one"}, {"acme/one", " acme/two"}, {"acme/one", "acme/missing"}, {"acme/one", "painted-wolf/platform"}} {
		if plan, err := PrepareRemoval(ids); err == nil {
			plan.Close()
			t.Errorf("PrepareRemoval(%q) accepted invalid selection", ids)
		}
	}
	ids := []string{"acme/one", "acme/two"}
	plan, err := PrepareRemoval(ids)
	testutil.FailErr(t, "prepare batch", err)
	defer plan.Close()
	ids[0] = "acme/keep"
	lock := EmptyLock()
	for _, id := range []string{"acme/one", "acme/two", "acme/keep"} {
		lock.Packages = append(lock.Packages, samplePackage(id, "1.0.0"))
	}
	next, nextLock, err := plan.Apply(desired, lock)
	testutil.FailErr(t, "apply batch", err)
	if !reflect.DeepEqual(next.Packs, []DesiredPack{{ID: "acme/keep"}}) || len(nextLock.Packages) != 1 || nextLock.Packages[0].ID != "acme/keep" {
		t.Fatalf("batch left unexpected state: desired=%+v lock=%+v", next.Packs, nextLock.Packages)
	}
	if len(desired.Packs) != 3 || len(lock.Packages) != 3 {
		t.Fatal("applying removal mutated input state")
	}
	stored, err := os.ReadFile(path)
	testutil.FailErr(t, "read desired", err)
	if !bytes.Equal(stored, data) {
		t.Fatal("preparing removal changed durable intent")
	}
}
