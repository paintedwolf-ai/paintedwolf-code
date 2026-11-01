package search

import (
	"bytes"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestGrepQueuedPinsRetainAdmissionThroughReduction(t *testing.T) {
	h := newHarness(t, map[string]string{"a": "needle a\n"})
	memory := grepMemory(t, 4<<20)
	h.owners.Revisions.Memory = memory
	r := parallelFixture(t, h, t.Context(), &bytes.Buffer{})
	defer r.inv.closePins()
	submitFiles(t, r, 1, "a")
	job := r.files.pending[0]
	var events []grepFileEvent
	for event := range job.events {
		events = append(events, event)
	}
	pins := 0
	for _, event := range events {
		pins += len(event.pins)
		if len(event.pins) > 0 && event.release == nil {
			t.Fatal("queued pin has no admitted lifetime")
		}
	}
	if pins != 1 || memory.InUse().State == 0 {
		t.Fatalf("worker released queued pins: count %d memory %+v", pins, memory.InUse())
	}
	for _, event := range events {
		testutil.FailErr(t, "reduce queued event", r.files.apply(event, 0))
		r.files.buffers.put(event.bytes)
	}
	testutil.FailErr(t, "finish worker", r.files.finish(nil))
	if memory.InUse().State != 0 {
		t.Fatalf("reduced pins retained %+v", memory.InUse())
	}
}

func TestGrepDiscardReleasesQueuedPinAdmission(t *testing.T) {
	memory := grepMemory(t, 4<<20)
	release, err := memory.ReserveProgramState(t.Context(), 1024)
	testutil.FailErr(t, "retain queued pin", err)
	job := &grepFileJob{events: make(chan grepFileEvent, 1)}
	job.events <- grepFileEvent{release: release}
	close(job.events)
	files := &grepFiles{buffers: newGrepBuffers(1)}
	files.discard(job)
	if memory.InUse().State != 0 {
		t.Fatalf("discard retained %+v", memory.InUse())
	}
}
