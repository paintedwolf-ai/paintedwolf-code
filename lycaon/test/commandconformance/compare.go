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

// Difference identifies a failed observable comparison without parsing its diagnostic.
type Difference struct {
	Kind           string
	Path           string
	ExpectedStatus int
	ActualStatus   int
	Text           string
}

// Identity excludes values that a reduction is allowed to simplify.
func (d Difference) Identity() Difference {
	d.Text = ""
	return d
}

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
func Match(got Observation, refs map[referenceuserland.ID]Snapshot) (referenceuserland.ID, map[referenceuserland.ID]Difference) {
	diffs := map[referenceuserland.ID]Difference{}
	for _, id := range slices.Sorted(maps.Keys(refs)) {
		want := reference(refs[id])
		diff := snapshotDiff(want, Snapshot{Input: want.Input, Status: got.Status, Stdout: got.Stdout, Stderr: got.Stderr, Files: got.Files})
		if diff.Kind == "" {
			return id, nil
		}
		diffs[id] = diff
	}
	return "", diffs
}

// FormatDiffs renders Match's differences, one userland per line.
func FormatDiffs(diffs map[referenceuserland.ID]Difference) string {
	var b strings.Builder
	for _, id := range slices.Sorted(maps.Keys(diffs)) {
		fmt.Fprintf(&b, "\n  %s: %s", id, diffs[id].Text)
	}
	return b.String()
}

func filesDiff(want, got map[string]*File) Difference {
	for _, name := range slices.Sorted(maps.Keys(want)) {
		w, g := want[name], got[name]
		_, observed := got[name]
		switch {
		case !observed:
			return Difference{Kind: "file_unobserved", Path: name, Text: fmt.Sprintf("file %s was not observed", name)}
		case w == nil && g == nil:
		case w == nil:
			return Difference{Kind: "file_created", Path: name, Text: fmt.Sprintf("file %s should not exist, now %s %q", name, g.Mode, g.Data)}
		case g == nil:
			return Difference{Kind: "file_missing", Path: name, Text: fmt.Sprintf("file %s %s %q is missing", name, w.Mode, w.Data)}
		case w.Mode != g.Mode || w.Link != g.Link || string(w.Data) != string(g.Data):
			kind := "file_bytes"
			if w.Mode != g.Mode {
				kind = "file_mode"
			} else if w.Link != g.Link {
				kind = "file_link"
			}
			return Difference{Kind: kind, Path: name, Text: fmt.Sprintf("file %s %s %q%s, now %s %q%s", name, w.Mode, w.Data, link(w.Link), g.Mode, g.Data, link(g.Link))}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[name]; !ok {
			return Difference{Kind: "file_untracked", Path: name, Text: fmt.Sprintf("file %s is not tracked", name)}
		}
	}
	return Difference{}
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
