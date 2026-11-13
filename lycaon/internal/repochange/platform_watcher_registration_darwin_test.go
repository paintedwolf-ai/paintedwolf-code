//go:build darwin && cgo

package repochange

import (
	"errors"
	"testing"

	"github.com/fsnotify/fsevents"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFSEventsRegistrationPreservesFullCallbackBuffer(t *testing.T) {
	const root = "/project"
	var queued int
	stream, err := startFSEventStream(root, func(stream *fsevents.EventStream) error {
		if len(stream.Paths) != 1 || stream.Paths[0] != root || !stream.Resume {
			t.Fatal("registration lost the root or event cursor")
		}
		// Model callbacks arriving during Start, before the pump can run. No
		// native stream or elapsed-time threshold is needed for this invariant.
		queued = cap(stream.Events)
		for id := range queued {
			stream.Events <- []fsevents.Event{{Path: root + "/file", ID: uint64(id + 1), Flags: fsevents.ItemModified}}
		}
		return nil
	})
	testutil.FailErr(t, "register stream", err)
	if len(stream.Events) != queued {
		t.Fatalf("registration retained %d callback batches, want %d", len(stream.Events), queued)
	}
	for id := range queued {
		batch := <-stream.Events
		if len(batch) != 1 || batch[0].ID != uint64(id+1) || batch[0].Flags != fsevents.ItemModified {
			t.Fatalf("registration changed callback batch %d: %+v", id, batch)
		}
	}
}

func TestFSEventsRegistrationReturnsStartFailure(t *testing.T) {
	want := errors.New("native registration refused")
	stream, err := startFSEventStream("/project", func(*fsevents.EventStream) error { return want })
	if stream != nil || !errors.Is(err, want) {
		t.Fatalf("stream=%v error=%v, want registration failure", stream, err)
	}
}
