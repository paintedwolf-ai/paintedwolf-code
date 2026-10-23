//go:build linux

package packagegeneration

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRuntimeCaptureAdmissionBoundsCombinedRowsBytesAndObservations(t *testing.T) {
	snapshot := &runtimeSnapshot{Inputs: map[string]runtimeInput{
		"/usr/lib/first.so":  {Kind: "file", Size: math.MaxInt64},
		"/usr/lib/second.so": {Kind: "file", Size: 1},
	}}
	if _, err := admitRuntimeRows(snapshot, 1); err == nil {
		t.Fatal("accepted overflowing aggregate bytes")
	}
	snapshot.Inputs["/usr/lib/first.so"] = runtimeInput{Kind: "file", Size: 7}
	total, err := admitRuntimeRows(snapshot, 1)
	if err != nil || total != 8 {
		t.Fatalf("selected bytes=%d,%v", total, err)
	}
	snapshot.Links = make(map[string]RuntimeLink)
	for i := 0; i < RuntimeSealMaxEntries; i++ {
		snapshot.Links[string(rune(i))] = RuntimeLink{}
	}
	if _, err := admitRuntimeRows(snapshot, 1); err == nil {
		t.Fatal("omitted link observations from final row bound")
	}
	if err := snapshot.observationRoom(1); err == nil {
		t.Fatal("accepted combined source/custody/link overflow")
	}
}

func TestRuntimeCaptureDescriptorAdmissionUsesActualLimitAndBaseline(t *testing.T) {
	budget, err := runtimeHandleBudget(1024, 20)
	if err != nil || budget != 1000 {
		t.Fatalf("descriptor headroom=%d,%v", budget, err)
	}
	if _, err := runtimeHandleBudget(24, 20); err == nil {
		t.Fatal("reserved temporary descriptors were admitted as retained")
	}
	if budget, err := runtimeDescriptorAdmission(); err != nil || budget < 1 || budget > RuntimeSealMaxEntries {
		t.Fatalf("actual process descriptor admission=%d,%v", budget, err)
	}
}

func TestRuntimeCaptureRejectsDuplicateOriginsBeforeOpeningThem(t *testing.T) {
	l, source, _ := runtimeCaptureFixture(t)
	origin := RuntimeOrigin{Kind: "native", SelectedRoot: source}
	if _, err := l.observeRuntime([]RuntimeOrigin{origin, origin}); err == nil {
		t.Fatal("accepted duplicate selected roots")
	}
}

func TestRuntimeCaptureReplacementFIFORefusesWithoutBlocking(t *testing.T) {
	l, source, target := runtimeCaptureFixture(t)
	before, err := l.observeRuntime([]RuntimeOrigin{{Kind: "native", SelectedRoot: source}})
	if err != nil {
		t.Fatal(err)
	}
	defer before.Close()
	name := filepath.Join(source, "module.so")
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(name, 0644); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := l.copyRuntimeInputs(before, target); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("accepted source FIFO replacement")
		}
	case <-time.After(3 * time.Second):
		// Open the owned fixture's FIFO only to release a regressed blocking open.
		fd, err := unix.Open(name, unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err == nil {
			defer unix.Close(fd)
		}
		<-result
		t.Fatal("source replacement blocked the capture")
	}
}
