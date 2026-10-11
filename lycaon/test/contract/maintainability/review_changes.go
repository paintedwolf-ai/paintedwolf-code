package maintainability

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
)

func reviewChanges(base, current *workingTree) []string {
	paths := map[string]bool{}
	for _, tree := range []*workingTree{base, current} {
		for _, file := range tree.files() {
			if strings.HasPrefix(file, reviewDirectory+"/") {
				paths[file] = true
			}
		}
	}
	var notes []string
	for file := range paths {
		before, beforeErr := base.read(file)
		after, afterErr := current.read(file)
		if beforeErr == nil && afterErr == nil && bytes.Equal(before, after) {
			continue
		}
		change := "revised"
		if beforeErr != nil {
			change = "added"
		} else if afterErr != nil {
			change = "removed"
		}
		notes = append(notes, fmt.Sprintf("Architectural review %s: %s; review rationale, ceiling, and declared boundaries with the code change.", change, file))
	}
	slices.Sort(notes)
	return notes
}
