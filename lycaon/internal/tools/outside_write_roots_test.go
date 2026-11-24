package tools

import (
	"os"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/fspath"
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

func rootPaths(roots []OutsideWriteRoot) []string {
	paths := make([]string, 0, len(roots))
	for _, root := range roots {
		paths = append(paths, root.Path)
	}
	return paths
}

// The window observes the profile's grants beyond the project, outermost
// first, skipping the project inside one, and the session's scratch without
// its program temp.
func TestOutsideWriteRootsAreTheProfilesGrantsACFL19(t *testing.T) {
	scratch := fspath.CanonicalPath(t.TempDir())
	temp := enginepaths.ProgramTempUnder(scratch)
	if err := os.Mkdir(temp, 0o700); err != nil {
		t.Fatal(err)
	}
	tc := outsideRootsContext("/fl03-work/shared", "/fl03-work/shared/nested", "/fl03-work/other", "/fl03-work/shared/project/build")
	tc.Host.SessionScratchDir = scratch
	got, unobserved := tc.OutsideWriteRoots()
	if paths := rootPaths(got); !slices.Equal(paths, []string{"/fl03-work/other", "/fl03-work/shared", scratch}) || len(unobserved) != 0 {
		t.Fatalf("outside write roots = %v, unobserved %v", paths, unobserved)
	}
	if !slices.Contains(got[1].Omit, "/fl03-work/shared/project") {
		t.Fatalf("shared root omits %v, want the attached root", got[1].Omit)
	}
	if len(got[0].Omit) != 0 {
		t.Fatalf("other root omits %v", got[0].Omit)
	}
	if !slices.Equal(got[2].Omit, []string{temp}) {
		t.Fatalf("scratch omits %v, want its program temp", got[2].Omit)
	}
}

// A whole shared temp location is reported instead of compared; a folder
// granted inside one is compared like any other.
func TestSharedTempLocationIsNeverComparedWhole(t *testing.T) {
	locations := confine.SharedTempLocations()
	if len(locations) == 0 {
		t.Fatal("no shared temp location on this host")
	}
	whole := locations[0]
	narrow := fspath.CanonicalPath(t.TempDir())
	got, unobserved := outsideRootsContext(whole, narrow).OutsideWriteRoots()
	if !slices.Equal(unobserved, []string{whole}) {
		t.Fatalf("unobserved = %v, want %s", unobserved, whole)
	}
	if paths := rootPaths(got); !slices.Contains(paths, narrow) || slices.Contains(paths, whole) {
		t.Fatalf("outside write roots = %v, want %s and not %s", paths, narrow, whole)
	}
}

func TestNoGrantedWriteRootObservesNothingOutsideACFL19(t *testing.T) {
	if got, unobserved := outsideRootsContext().OutsideWriteRoots(); len(got) != 0 || len(unobserved) != 0 {
		t.Fatalf("outside write roots = %+v, %v, want none without a grant", got, unobserved)
	}
	tc := outsideRootsContext("/fl03-work/shared")
	tc.Source.OutsideRoot = projectroot.RootRef{}
	if got, _ := tc.OutsideWriteRoots(); len(got) != 0 {
		t.Fatalf("outside write roots = %+v, want none without an outside namespace", got)
	}
}
