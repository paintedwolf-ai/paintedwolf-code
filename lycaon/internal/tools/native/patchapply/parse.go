// Package patchapply parses unified and Git diffs and applies their hunks to
// captured bytes. It is the one hunk applier behind the patch and git apply
// command frontends: offset search, fuzz, reversal and Git binary patches
// live here, and neither frontend matches hunks itself. It touches no files;
// the frontends publish what it computes through their owners.
package patchapply

import (
	"bytes"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// Git patch protocol delimiters terminate the file-name header.
const (
	gitSectionPrefix     = "diff --git "
	gitBinaryPatchHeader = "GIT binary patch"
)

// File is one file section of a patch.
type File struct {
	*gitdiff.File
	// Git reports a "diff --git" section, whose names arrive with their
	// first component (a/, b/) already removed.
	Git bool
	// rawOld and rawNew are a Git section's names as written, before any
	// component is removed; they serve -p0.
	rawOld, rawNew string
}

// Patch is one parsed diff, its file sections in input order.
type Patch struct {
	Files []File
}

// ErrNoPatch reports input that holds no file section at all.
var ErrNoPatch = errors.New("no valid patches in input")

// Parse reads every file section. A line starting "diff --git " always opens
// a Git section (hunk and binary lines never start that way), so the input is
// split there and each part parsed alone; that is how each section learns
// which naming convention its headers follow.
func Parse(data []byte) (Patch, error) {
	var patch Patch
	line := 1
	for i, part := range splitGitSections(data) {
		files, _, err := gitdiff.Parse(bytes.NewReader(part))
		if err != nil {
			if line > 1 {
				return Patch{}, fmt.Errorf("in the section starting at line %d: %w", line, err)
			}
			return Patch{}, err
		}
		line += bytes.Count(part, []byte("\n"))
		for j, file := range files {
			f := File{File: file, Git: i > 0 && j == 0}
			if f.Git {
				f.rawOld, f.rawNew = rawGitNames(part)
			}
			patch.Files = append(patch.Files, f)
		}
	}
	if len(patch.Files) == 0 {
		return Patch{}, ErrNoPatch
	}
	return patch, nil
}

// splitGitSections cuts data before every line that opens a Git section.
// The first part holds whatever precedes the first such line, possibly
// nothing; every later part starts with its Git header.
func splitGitSections(data []byte) [][]byte {
	parts := [][]byte{}
	start := 0
	for offset := 0; offset < len(data); {
		if bytes.HasPrefix(data[offset:], []byte(gitSectionPrefix)) {
			parts = append(parts, data[start:offset])
			start = offset
		}
		end := bytes.IndexByte(data[offset:], '\n')
		if end < 0 {
			break
		}
		offset += end + 1
	}
	return append(parts, data[start:])
}

// rawGitNames reads the names a Git section wrote before its first hunk:
// the ---/+++ lines when present, otherwise the halves of the header line.
func rawGitNames(section []byte) (string, string) {
	lines := strings.Split(string(section), "\n")
	var old, new string
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "@@ ") || strings.HasPrefix(line, gitBinaryPatchHeader) || strings.HasPrefix(line, gitSectionPrefix) {
			break
		}
		if name, ok := strings.CutPrefix(line, "--- "); ok {
			old = headerName(name)
		} else if name, ok := strings.CutPrefix(line, "+++ "); ok {
			new = headerName(name)
		}
	}
	if old != "" || new != "" {
		return old, new
	}
	header := strings.TrimPrefix(lines[0], gitSectionPrefix)
	if len(header)%2 == 1 && header[len(header)/2] == ' ' && trimComponents(header[:len(header)/2], 1) == trimComponents(header[len(header)/2+1:], 1) {
		return header[:len(header)/2], header[len(header)/2+1:]
	}
	return "", ""
}

func headerName(field string) string {
	if strings.HasPrefix(field, `"`) {
		if end := strings.LastIndex(field, `"`); end > 0 {
			if name, err := strconv.Unquote(field[:end+1]); err == nil {
				return name
			}
		}
	}
	name, _, _ := strings.Cut(field, "\t")
	return strings.TrimRight(name, " ")
}

// DevNull is the name a header uses for a missing side.
const DevNull = "/dev/null"

// Names returns the old and new paths after removing strip leading
// components, the way -pN reads them. A side that is absent (creation or
// deletion) is "". A name with too few components is an error.
func (f File) Names(strip int) (string, string, error) {
	old, new := f.OldName, f.NewName
	switch {
	case f.Git && strip == 0 && !f.IsRename && !f.IsCopy:
		if f.rawOld == "" && f.rawNew == "" {
			return "", "", fmt.Errorf("cannot read the unstripped names of %s", f.NewName)
		}
		old, new = f.rawOld, f.rawNew
		if f.IsNew {
			old = ""
		}
		if f.IsDelete {
			new = ""
		}
	case f.Git:
		strip--
	}
	if old == DevNull {
		old = ""
	}
	if new == DevNull {
		new = ""
	}
	var err error
	if old, err = stripName(old, strip); err != nil {
		return "", "", err
	}
	if new, err = stripName(new, strip); err != nil {
		return "", "", err
	}
	return old, new, nil
}

func stripName(name string, strip int) (string, error) {
	if name == "" || strip <= 0 {
		return name, nil
	}
	stripped := trimComponents(name, strip)
	if stripped == "" {
		return "", fmt.Errorf("%s has fewer than %d leading components", name, strip)
	}
	return stripped, nil
}

// trimComponents removes n slash-separated leading components; runs of
// slashes count as one separator.
func trimComponents(name string, n int) string {
	for ; n > 0; n-- {
		i := strings.IndexByte(name, '/')
		if i < 0 {
			return ""
		}
		name = strings.TrimLeft(name[i:], "/")
	}
	return name
}

// Symlink reports a section that changes a symbolic link (Git mode 120000).
func (f File) Symlink() bool {
	return f.OldMode&0o170000 == 0o120000 || f.NewMode&0o170000 == 0o120000
}

// ModeChange returns the permission ("644" or "755") a section sets when it
// changes a file's executable bit, or "".
func (f File) ModeChange() string {
	if f.NewMode == 0 || (f.NewMode&0o111 != 0) == (f.OldMode&0o111 != 0) {
		return ""
	}
	if f.NewMode&0o111 != 0 {
		return "755"
	}
	return "644"
}
