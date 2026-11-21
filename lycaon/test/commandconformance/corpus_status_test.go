package commandconformance

import (
	"strings"
	"testing"
)

func TestOwnerKindStatusKeepsOneSourceOfEachOwnerKind(t *testing.T) {
	kinds := 0
	for _, source := range statusSources {
		if source.ownerKind {
			kinds++
		}
	}
	for _, full := range StatusCases() {
		reduced, ok := OwnerKindStatusOf(full)
		if !ok {
			t.Fatalf("%s is not reduced", full.ID)
		}
		reports := strings.Count(full.Program, "\nprintf '") - 1
		kept := strings.Count(reduced.Case.Program, "\nprintf '") - 1
		if reports%len(statusSources) != 0 || kept != reports/len(statusSources)*kinds {
			t.Fatalf("%s keeps %d of %d reports, want %d of each %d", full.ID, kept, reports, kinds, len(statusSources))
		}
		if reduced.Case.ID != full.ID || !strings.HasSuffix(reduced.Case.Program, "wait\nprintf 'end status=%s\\n' \"$?\"\n") {
			t.Fatalf("%s reduction lost its identity or final report:\n%s", full.ID, reduced.Case.Program)
		}
	}
	if _, ok := OwnerKindStatusOf(Case{ID: "status"}); ok {
		t.Fatal("a case outside the status compositions was reduced")
	}
}

func TestOwnerKindStatusReferenceKeepsTheSameReports(t *testing.T) {
	reduced, _ := OwnerKindStatusOf(StatusCases()[0])
	full := Snapshot{Status: 0, Stdout: Text("0.0 status=0 pipe=0\n0.1 status=1 pipe=1\n0.2 status=3 pipe=3\n1.3 status=1 pipe=1\nend status=0\n")}
	got := reduced.Reference(full)
	if want := "0.0 status=0 pipe=0\n0.2 status=3 pipe=3\nend status=0\n"; string(got.Stdout) != want {
		t.Fatalf("reduced reference = %q, want %q", got.Stdout, want)
	}
}
