package sandbox

import "strings"

// HasParentTraversal reports exact parent-directory components.
func HasParentTraversal(path string) bool {
	for _, component := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if component == ".." {
			return true
		}
	}
	return false
}
