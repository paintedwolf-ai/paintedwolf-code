package navigationref

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

func contextLocation(path, job string) api.NavigationTarget {
	return api.NavigationTarget{ProjectID: "p", RootID: "r", WorkerID: job, Path: path, EntryKind: api.NavigationEntryKindFile}
}

func TestContextBindingKeepsObservedScope(t *testing.T) {
	p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", Path: "/unavailable", IsPrimary: true}}}
	context := &api.SourceContext{Locations: []api.NavigationTarget{
		contextLocation("src/a.go", "worker"), contextLocation(".ignored/LICENSE", ""),
	}}
	refs := BuildProjectPathReferences(p, "Updated a.go and `LICENSE`. Branch `main` passed contract/integration tests.", context)
	if len(refs) != 3 || refs[0].WorkerID != "worker" || refs[0].Path != "src/a.go" || refs[1].Path != ".ignored/LICENSE" || refs[2].Status != api.NavigationPending {
		t.Fatalf("refs=%+v", refs)
	}
	for _, ref := range refs {
		if ref.Explicit {
			t.Fatalf("inferred ref became authored: %+v", ref)
		}
	}
}

func TestContextAmbiguityAndOccurrences(t *testing.T) {
	p := &project.Project{ID: "p"}
	context := &api.SourceContext{Locations: []api.NavigationTarget{contextLocation("src/a.go", "one"), contextLocation("src/a.go", "two")}}
	refs := BuildProjectPathReferences(p, "`a.go` and a.go [a.go](source://r/src/a.go?job_id=one)", context)
	if len(refs) != 3 || refs[0].Status != api.NavigationAmbiguous || len(refs[0].Candidates) != 2 || refs[1].Syntax != "text" || refs[2].WorkerID != "one" || !refs[2].Explicit {
		t.Fatalf("refs=%+v", refs)
	}
	if refs[0].ID == refs[1].ID || refs[1].ID == refs[2].ID {
		t.Fatal("occurrence identities collided")
	}
}

func TestContextOverflowDoesNotGuess(t *testing.T) {
	p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", IsPrimary: true}}}
	context := &api.SourceContext{}
	for i := 0; i < 33; i++ {
		context.Locations = append(context.Locations, contextLocation(fmt.Sprintf("module%d/src/a.go", i), ""))
	}
	refs := BuildProjectPathReferences(p, "`src/a.go`", context)
	if len(refs) != 1 || refs[0].Status != api.NavigationUnavailable || refs[0].RootID != "" {
		t.Fatalf("ambiguous subset became exact: %+v", refs)
	}
	context.Truncated = true
	if got := BuildProjectPathReferences(p, "`a.go`", context); len(got) != 0 {
		t.Fatalf("truncated context inferred uniqueness: %+v", got)
	}
}

func BenchmarkContextNavigation(b *testing.B) {
	for _, count := range []int{32, 512, 4096} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			p := &project.Project{ID: "p", Roots: []project.Root{{ID: "r", Path: "/unavailable", IsPrimary: true}}}
			context := &api.SourceContext{}
			var prose strings.Builder
			for i := 0; i < count; i++ {
				context.Locations = append(context.Locations, contextLocation(fmt.Sprintf("components/module%d/file%d.go", i, i), ""))
				if i < 128 {
					fmt.Fprintf(&prose, "Updated `file%d.go`.\n", i)
				}
			}
			content := prose.String()
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				refs := BuildProjectPathReferences(p, content, context)
				if len(refs) != min(count, 128) {
					b.Fatalf("refs=%d", len(refs))
				}
			}
		})
	}
}
