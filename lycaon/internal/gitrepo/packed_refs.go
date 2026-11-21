package gitrepo

import "strings"

// PackedRef finds an exact reference in packed-refs contents.
func PackedRef(contents, ref string) (string, bool) {
	for line := range strings.SplitSeq(contents, "\n") {
		oid, name, found := strings.Cut(strings.TrimSpace(line), " ")
		if found && name == ref {
			return oid, true
		}
	}
	return "", false
}
