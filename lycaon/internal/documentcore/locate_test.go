package documentcore

import (
	"context"
	"errors"
	"testing"
)

func TestLocateFollowsAnchorsThroughLaterEdits(t *testing.T) {
	engine := coreForTest(t)
	callForTest(t, engine, Request{Action: "open", Handle: 1, Client: 1})
	callForTest(t, engine, Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: "alpha\nbeta\ngamma\n"}}})
	anchored := callForTest(t, engine, Request{Action: "anchor", Handle: 1, OmitText: true, Edits: []Edit{{Index: 6, Delete: 4}, {Index: 11}}})
	selection, cursor := anchored.Anchors[0], anchored.Anchors[1]
	const inserted = "zero\n"
	callForTest(t, engine, Request{Action: "edit", Handle: 1, Edits: []Edit{{Insert: inserted}}})

	backward := AnchoredEdit{Start: selection.End, End: selection.Start}
	located := callForTest(t, engine, Request{Action: "locate", Handle: 1, OmitText: true, Anchors: []AnchoredEdit{backward, cursor}})
	shift := uint32(len(inserted))
	want := []Edit{{Index: 6 + shift, Delete: 4}, {Index: 11 + shift}}
	if len(located.Resolved) != len(want) {
		t.Fatalf("located %d ranges; want %d", len(located.Resolved), len(want))
	}
	for i := range want {
		if located.Resolved[i] != want[i] {
			t.Fatalf("range %d located at %+v; want %+v", i, located.Resolved[i], want[i])
		}
	}
}

func TestLocateRejectsUndecodableAnchors(t *testing.T) {
	engine := coreForTest(t)
	callForTest(t, engine, Request{Action: "open", Handle: 1, Client: 1})
	_, err := engine.Call(context.Background(), Request{Action: "locate", Handle: 1, Anchors: []AnchoredEdit{{Start: []byte{1}, End: []byte{1}}}})
	var rejected *Rejected
	if !errors.As(err, &rejected) || rejected.Code != "invalid_anchor" {
		t.Fatalf("locate with garbage anchors = %v; want invalid_anchor", err)
	}
}
