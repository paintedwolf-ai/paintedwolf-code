package commandprogram

import (
	"context"
	"io"
	"testing"
)

func TestDeferredDescriptorsExcludeContainingSubstitution(t *testing.T) {
	f := newEvalFixture(t)
	out, err := f.run(t, t.Context(), "echo $(echo value) > out; echo $(fail 1) > absent; echo $?")
	if err != nil || out.Stop != nil || f.stdout.String() != "1\n" {
		t.Fatalf("out=%+v stdout=%q error=%v", out, f.stdout.String(), err)
	}
	if f.publication.published["out"] != "value\n" {
		t.Fatalf("published=%v", f.publication.published)
	}
	if _, exists := f.publication.published["absent"]; exists {
		t.Fatalf("prevented containing command published: %v", f.publication.published)
	}
}

func TestDeferredDescriptorLivesThroughBackgroundChildren(t *testing.T) {
	for _, command := range []string{"echo late", "fail 1"} {
		t.Run(command, func(t *testing.T) {
			f := newEvalFixture(t)
			out, err := f.run(t, t.Context(), "{ "+command+" & } > out; echo foreground")
			if err != nil || out.Stop != nil || f.stdout.String() != "foreground\n" {
				t.Fatalf("out=%+v stdout=%q error=%v", out, f.stdout.String(), err)
			}
			if len(f.publication.published) != 0 {
				t.Fatalf("published before child completed: %v", f.publication.published)
			}
			result, err := f.jobs.runs[0](context.Background(), Streams{Input: NullInput(), Output: io.Discard, Error: io.Discard})
			if err != nil {
				t.Fatal(err)
			}
			if command == "echo late" && (result.Status != 0 || f.publication.published["out"] != "late\n") {
				t.Fatalf("result=%+v published=%v", result, f.publication.published)
			}
			if command == "fail 1" && (result.Status != 1 || len(f.publication.published) != 0 || len(f.publication.abandoned) != 1) {
				t.Fatalf("result=%+v published=%v abandoned=%v", result, f.publication.published, f.publication.abandoned)
			}
		})
	}
}
