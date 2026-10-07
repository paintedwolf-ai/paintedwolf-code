// Package coverage summarizes scanner limitations without classifying source intent.
package coverage

import (
	"cmp"
	"path"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

const SampleLimit = 12
const DistributionLimit = 8

type Count struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type Distribution struct {
	Entries []Count `json:"entries"`
	Other   int     `json:"other,omitempty"`
}

// Scope retains complete counts while bounding every displayed collection.
type Scope struct {
	Files           int      `json:"files"`
	Warnings        int      `json:"warnings"`
	PathsSample     []string `json:"paths_sample"`
	SampleTruncated bool     `json:"sample_truncated"`
	Profile
}

type Profile struct {
	Directories Distribution `json:"directories"`
	Extensions  Distribution `json:"extensions"`
	Constructs  Distribution `json:"constructs"`
}

func Summarize(warnings []api.ScanWarning) Scope {
	paths := make(map[string]bool)
	constructs := make(map[string]int)
	for _, warning := range warnings {
		if warning.File != "" {
			paths[warning.File] = true
		}
		constructs[warning.Construct]++
	}
	ordered := make([]string, 0, len(paths))
	dirs, extensions := make(map[string]int), make(map[string]int)
	for name := range paths {
		ordered = append(ordered, name)
		dir := path.Dir(name)
		parts := strings.Split(dir, "/")
		if len(parts) > 2 {
			dir = strings.Join(parts[:2], "/")
		}
		dirs[dir]++
		extensions[path.Ext(name)]++
	}
	slices.Sort(ordered)
	return Scope{Files: len(paths), Warnings: len(warnings), PathsSample: Sample(ordered), SampleTruncated: len(paths) > SampleLimit,
		Profile: Profile{Directories: distribution(dirs), Extensions: distribution(extensions), Constructs: distribution(constructs)}}
}

// Sample spans the sorted scope; it is not evidence about unsampled members.
func Sample(ordered []string) []string {
	if len(ordered) <= SampleLimit {
		return slices.Clone(ordered)
	}
	out := make([]string, SampleLimit)
	for i := range out {
		out[i] = ordered[i*(len(ordered)-1)/(SampleLimit-1)]
	}
	return out
}

func distribution(counts map[string]int) Distribution {
	entries := make([]Count, 0, len(counts))
	for name, count := range counts {
		entries = append(entries, Count{Name: name, Count: count})
	}
	slices.SortFunc(entries, func(a, b Count) int { return cmp.Or(cmp.Compare(b.Count, a.Count), strings.Compare(a.Name, b.Name)) })
	out := Distribution{Entries: entries}
	if len(entries) > DistributionLimit {
		out.Entries = entries[:DistributionLimit]
		for _, entry := range entries[DistributionLimit:] {
			out.Other += entry.Count
		}
	}
	return out
}
