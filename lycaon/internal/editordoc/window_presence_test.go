package editordoc

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWindowPresenceIdentityAndSelections(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "first", "b.txt": "second"})
	a, b := f.open(t, "a.txt"), f.open(t, "b.txt")
	_, first := replicaForTest(t, f, a, "first-window")
	_, second := replicaForTest(t, f, a, "second-window")
	firstNumber := first.Participants[0].WindowNumber
	if second.Participants[0].WindowNumber == second.Participants[1].WindowNumber {
		t.Fatal("window numbers collided")
	}
	_, otherFile := replicaForTest(t, f, b, "first-window")
	if otherFile.Participants[0].WindowNumber != firstNumber {
		t.Fatal("window identity changed across files")
	}
	in := Participant{ClientID: "first-window", Incarnation: first.Participants[0].Incarnation, WindowNumber: 999, Main: 1,
		Ranges: []PresenceRange{{Anchor: []byte{1}, Head: []byte{2}}, {Anchor: []byte{3}, Head: []byte{4}}}}
	testutil.FailErr(t, "publish selections", f.service.UpdatePresence(t.Context(), a.ID, f.project.ID, in))
	in.Ranges[0].Anchor[0] = 9
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, a.ID)
	testutil.FailErr(t, "read presence", err)
	person := current.Participants[0]
	if person.WindowNumber != firstNumber || person.Main != 1 || len(person.Ranges) != 2 || person.Ranges[0].Anchor[0] != 1 {
		t.Fatalf("presence projection = %+v", person)
	}
	testutil.FailErr(t, "leave first file", f.service.Leave(t.Context(), a.ID, f.project.ID, "first-window", first.Participants[0].Incarnation))
	_, rejoined := replicaForTest(t, f, a, "first-window")
	if rejoined.Participants[0].WindowNumber != firstNumber {
		t.Fatal("rejoining renumbered the window")
	}
	if err := f.service.UpdatePresence(t.Context(), a.ID, f.project.ID, Participant{ClientID: "first-window", Main: 1}); err == nil {
		t.Fatal("accepted a primary range outside the selection")
	}
}

func TestReopenedWindowRejectsStalePresenceAndDeparture(t *testing.T) {
	f := newExternalFixture(t, map[string]string{"a.txt": "first"})
	d := f.open(t, "a.txt")
	first, err := f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "window", Incarnation: "first", Epoch: 1})
	testutil.FailErr(t, "join first editor", err)
	second, err := f.service.Join(t.Context(), d.ID, f.project.ID, ReplicaJoin{ClientID: "window", Incarnation: "second", Epoch: 1})
	testutil.FailErr(t, "reopen editor", err)
	if first.Participants[0].WindowNumber != second.Participants[0].WindowNumber {
		t.Fatal("reopening changed the window number")
	}
	err = f.service.UpdatePresence(t.Context(), d.ID, f.project.ID, Participant{ClientID: "window", Incarnation: "first"})
	if !errors.Is(err, ErrReplicaIdentity) {
		t.Fatalf("stale presence error = %v", err)
	}
	testutil.FailErr(t, "leave old editor", f.service.Leave(t.Context(), d.ID, f.project.ID, "window", "first"))
	current, err := f.service.CurrentSnapshot(t.Context(), f.project.ID, d.ID)
	testutil.FailErr(t, "read current editor", err)
	if len(current.Participants) != 1 || current.Participants[0].Incarnation != "second" {
		t.Fatalf("old departure removed the reopened editor: %+v", current.Participants)
	}
	testutil.FailErr(t, "publish current editor", f.service.UpdatePresence(t.Context(), d.ID, f.project.ID, Participant{ClientID: "window", Incarnation: "second"}))
}
