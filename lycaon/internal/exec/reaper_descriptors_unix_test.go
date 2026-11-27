//go:build unix

package exec

import (
	"errors"
	"slices"
	"testing"
)

func TestInheritedDescriptorCandidatesPreferDescriptorDirectories(t *testing.T) {
	unreadable := errors.New("unreadable")
	var visited []string
	procOnly := func(path string) ([]string, error) {
		visited = append(visited, path)
		if path == "/proc/self/fd" {
			return []string{"0", "1", "2", "7", "x", "12"}, nil
		}
		return nil, unreadable
	}
	if got := inheritedDescriptorCandidates(procOnly, 1<<20); !slices.Equal(got, []int{7, 12}) {
		t.Fatalf("candidates = %v, want [7 12]", got)
	}
	if !slices.Equal(visited, descriptorDirs) {
		t.Fatalf("visited %v, want %v", visited, descriptorDirs)
	}

	none := func(string) ([]string, error) { return nil, unreadable }
	if got := inheritedDescriptorCandidates(none, 1<<30); len(got) != maxDescriptorProbe-3 || got[0] != 3 {
		t.Fatalf("blind probe covers %d descriptors from %v, want %d from 3", len(got), got[:1], maxDescriptorProbe-3)
	}
	if got := inheritedDescriptorCandidates(none, 10); !slices.Equal(got, []int{3, 4, 5, 6, 7, 8, 9}) {
		t.Fatalf("small limit candidates = %v", got)
	}
}
