package commandconformance

import (
	"bytes"
	"fmt"
	"io/fs"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/lycaon/lycaon/test/referenceuserland"
)

// Observation is what a program left through the command entry.
type Observation struct {
	Status int
	Stdout []byte
	Stderr []byte
	Files  map[string]*File
}

// referenceWorkspace is where every reference program runs.
const referenceWorkspace = "/workspace"

// shellLine is the prefix a reference shell started as /bin/bash -c gives its own
// errors: its path and the program's line number.
var shellLine = regexp.MustCompile(`(?m)^(?:/bin/)?(bash|sh): line [0-9]+: `)

// Normalize maps the command entry's workspace roots to the reference workspace.
// roots lists every absolute spelling of the workspace, such as its resolved path.
func (o Observation) Normalize(roots ...string) Observation {
	roots = slices.Clone(roots)
	// The longest root first, so a root never rewrites part of a longer one.
	slices.SortFunc(roots, func(a, b string) int { return len(b) - len(a) })
	rewrite := func(data []byte) []byte {
		for _, root := range roots {
			data = bytes.ReplaceAll(data, []byte(root), []byte(referenceWorkspace))
		}
		return data
	}
	out := Observation{Status: o.Status, Stdout: rewrite(o.Stdout), Stderr: rewrite(o.Stderr)}
	if o.Files != nil {
		out.Files = map[string]*File{}
		for name, f := range o.Files {
			if f != nil {
				f = &File{Mode: f.Mode, Data: rewrite(f.Data), Link: f.Link}
			}
			out.Files[name] = f
		}
	}
	return out
}

// reference is the snapshot as the command entry reports it: a shell error carries
// the shell's name and no line number.
func reference(s Snapshot) Snapshot {
	s.Stderr = shellLine.ReplaceAll(s.Stderr, []byte("${1}: "))
	return s
}

// Match returns a userland whose result the observation equals, or, when there is
// none, the difference from each.
func Match(got Observation, refs map[referenceuserland.ID]Snapshot) (referenceuserland.ID, map[referenceuserland.ID]string) {
	diffs := map[referenceuserland.ID]string{}
	for _, id := range slices.Sorted(maps.Keys(refs)) {
		want := reference(refs[id])
		diff := snapshotDiff(want, Snapshot{Input: want.Input, Status: got.Status, Stdout: got.Stdout, Stderr: got.Stderr, Files: got.Files})
		if diff == "" {
			return id, nil
		}
		diffs[id] = diff
	}
	return "", diffs
}

// FormatDiffs renders Match's differences, one userland per line.
func FormatDiffs(diffs map[referenceuserland.ID]string) string {
	var b strings.Builder
	for _, id := range slices.Sorted(maps.Keys(diffs)) {
		fmt.Fprintf(&b, "\n  %s: %s", id, diffs[id])
	}
	return b.String()
}

func filesDiff(want, got map[string]*File) string {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		w, g := want[name], got[name]
		_, observed := got[name]
		switch {
		case !observed:
			return fmt.Sprintf("file %s was not observed", name)
		case w == nil && g == nil:
		case w == nil:
			return fmt.Sprintf("file %s should not exist, now %s %q", name, g.Mode, g.Data)
		case g == nil:
			return fmt.Sprintf("file %s %s %q is missing", name, w.Mode, w.Data)
		case w.Mode != g.Mode || w.Link != g.Link || string(w.Data) != string(g.Data):
			return fmt.Sprintf("file %s %s %q%s, now %s %q%s", name, w.Mode, w.Data, link(w.Link), g.Mode, g.Data, link(g.Link))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			return fmt.Sprintf("file %s is not tracked", name)
		}
	}
	return ""
}

func link(target string) string {
	if target == "" {
		return ""
	}
	return " -> " + target
}

func (m Mode) String() string {
	return fs.FileMode(m).String()
}
