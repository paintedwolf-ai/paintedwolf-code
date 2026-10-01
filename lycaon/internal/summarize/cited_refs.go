package summarize

import (
	"github.com/lycaon/lycaon/internal/tools/docrefs"
)

// citedFileOmission reports referenced paths absent from the gathered set.
func citedFileOmission(cands []Candidate) (omitted []string) {
	gathered := make(map[string]bool, len(cands))
	for _, c := range cands {
		if c.Kind == KindFile && c.RelPath != "" {
			gathered[c.RelPath] = true
		}
	}
	seen := make(map[string]bool)
	for _, c := range cands {
		if c.Kind != KindFile || c.Body == "" {
			continue
		}
		for _, ref := range docrefs.Extract(c.Body) {
			if gathered[ref] || seen[ref] {
				continue
			}
			seen[ref] = true
			omitted = append(omitted, ref)
		}
	}
	return omitted
}
