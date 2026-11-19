package tools

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/projectroot"
)

func outsideRootsContext(granted ...string) ToolContext {
	tc := ToolContext{Source: InvocationSource{
		Roots:       []projectroot.RootRef{{ID: "root", Path: "/fl03-work/shared/project", IsPrimary: true}},
		OutsideRoot: projectroot.OutsideRef("outside"),
	}}
	tc.Files.GrantedWriteRoots = granted
	return tc
}

// The window observes the profile's grants beyond the project, outermost
// first, skipping the project inside one and never a scratch folder.
func TestOutsideWriteRootsAreTheProfilesGrantsACFL19(t *testing.T) {
	scratch := t.TempDir()
	tc := outsideRootsContext("/fl03-work/shared", "/fl03-work/shared/nested", "/fl03-work/other", scratch, "/fl03-work/shared/project/build")
	got := tc.OutsideWriteRoots()
	paths := make([]string, 0, len(got))
	for _, root := range got {
		paths = append(paths, root.Path)
	}
	if !slices.Equal(paths, []string{"/fl03-work/other", "/fl03-work/shared"}) {
		t.Fatalf("outside write roots = %v", paths)
	}
	if !slices.Contains(got[1].Omit, "/fl03-work/shared/project") {
		t.Fatalf("shared root omits %v, want the attached root", got[1].Omit)
	}
	if len(got[0].Omit) != 0 {
		t.Fatalf("other root omits %v", got[0].Omit)
	}
}

func TestNoGrantedWriteRootObservesNothingOutsideACFL19(t *testing.T) {
	if got := outsideRootsContext().OutsideWriteRoots(); len(got) != 0 {
		t.Fatalf("outside write roots = %+v, want none without a grant", got)
	}
	tc := outsideRootsContext("/fl03-work/shared")
	tc.Source.OutsideRoot = projectroot.RootRef{}
	if got := tc.OutsideWriteRoots(); len(got) != 0 {
		t.Fatalf("outside write roots = %+v, want none without an outside namespace", got)
	}
}
