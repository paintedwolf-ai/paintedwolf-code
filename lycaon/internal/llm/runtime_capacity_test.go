package llm

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providers/nativegen"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestRegistryAliasesPreemptSharedBackgroundCapacity(t *testing.T) {
	runtime := nativegen.NewRuntime(nativegen.RuntimeConfig{})
	defer runtime.Close()
	registry := &Registry{}
	for _, id := range []string{"coordinator", "utility"} {
		testutil.FailErr(t, "register alias", registry.Register(nativegen.New(id, nativegen.Options{Runtime: runtime})))
	}
	foreground, err := registry.Get("coordinator")
	testutil.FailErr(t, "resolve coordinator", err)
	background, err := registry.Get("utility")
	testutil.FailErr(t, "resolve utility", err)
	if modelcall.CapacityKey(foreground) != modelcall.CapacityKey(background) {
		t.Fatal("registry decorators lost shared runtime identity")
	}
	plane := NewUtilityPlane()
	ctx, release, err := plane.lanes.acquireBackground(t.Context(), modelcall.CapacityKey(background), true)
	testutil.FailErr(t, "start background work", err)
	defer release()
	foregroundRelease, err := plane.acquireForeground(t.Context(), foreground)
	testutil.FailErr(t, "admit foreground", err)
	defer foregroundRelease()
	if ctx.Err() == nil {
		t.Fatal("foreground alias did not cancel background work")
	}
	release()
	plane.lanes.mu.Lock()
	defer plane.lanes.mu.Unlock()
	if len(plane.lanes.lanes) != 1 {
		t.Fatal("aliases created separate lanes")
	}
}
