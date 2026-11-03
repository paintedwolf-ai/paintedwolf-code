package patchapply

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/bluekeyes/go-gitdiff/gitdiff"
)

// Options select how hunks find their place.
type Options struct {
	// Fuzz is how many context lines at each end of a hunk may be ignored
	// when no exact match exists (patch -F, default 2; git apply uses 0).
	Fuzz int
}

// Hunk is one hunk's outcome.
type Hunk struct {
	// Number is the hunk's 1-based position in its file section.
	Number int    `json:"number"`
	Header string `json:"header"`
	// Applied reports whether the hunk changed the result.
	Applied bool `json:"applied"`
	// Line is the 1-based line where the hunk landed, or was expected when
	// it failed.
	Line   int    `json:"line"`
	Offset int    `json:"offset,omitempty"`
	Fuzz   int    `json:"fuzz,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// Text is one file's application result.
type Text struct {
	Content []byte
	Hunks   []Hunk
	// Failed counts hunks that found no place.
	Failed int
}

// ApplyText applies fragments in order to src. Each hunk searches outward
// from its stated line (shifted by the offset earlier hunks landed at) and
// never overlaps text an earlier hunk changed. Exact context is tried at
// every position before any fuzz is allowed, then one more ignored context
// line per end at a time, the way GNU patch locates hunks. A hunk that finds
// no place is reported and skipped; the others still apply.
func ApplyText(src []byte, fragments []*gitdiff.TextFragment, opts Options) Text {
	lines := splitLines(src)
	out := Text{}
	floor, offset := 0, 0
	for i, frag := range fragments {
		hunk := Hunk{Number: i + 1, Header: frag.Header()}
		old, new, lead, trail := sides(frag)
		expected := int(frag.OldPosition) - 1
		if frag.OldLines == 0 {
			expected = int(frag.OldPosition)
		}
		expected = max(expected, 0) + offset
		pos, fuzz := locate(lines, old, lead, trail, expected, floor, opts.Fuzz)
		if pos < 0 {
			hunk.Line = expected + 1
			hunk.Reason = "context does not match"
			out.Failed++
			out.Hunks = append(out.Hunks, hunk)
			continue
		}
		start, end := pos+min(fuzz, lead), pos+len(old)-min(fuzz, trail)
		replacement := new[min(fuzz, lead) : len(new)-min(fuzz, trail)]
		lines = append(lines[:start], append(append([][]byte{}, replacement...), lines[end:]...)...)
		hunk.Applied = true
		hunk.Line = pos + 1
		hunk.Fuzz = fuzz
		hunk.Offset = pos - (expected - offset)
		offset = hunk.Offset
		floor = start + len(replacement)
		out.Hunks = append(out.Hunks, hunk)
	}
	out.Content = bytes.Join(lines, nil)
	return out
}

// Applies reports whether every fragment finds its place in src, without
// keeping the result: the reversed-patch check.
func Applies(src []byte, fragments []*gitdiff.TextFragment, opts Options) bool {
	return len(fragments) > 0 && ApplyText(src, fragments, opts).Failed == 0
}

// sides splits a fragment into the lines it expects and the lines it leaves,
// and counts the context lines that lead and trail its changes.
func sides(frag *gitdiff.TextFragment) (old, new [][]byte, lead, trail int) {
	for _, line := range frag.Lines {
		text := []byte(line.Line)
		if line.Old() {
			old = append(old, text)
		}
		if line.New() {
			new = append(new, text)
		}
	}
	return old, new, int(frag.LeadingContext), int(frag.TrailingContext)
}

// locate finds the first position, nearest the expected one, where old
// matches with the least fuzz. It returns -1 when no fuzz up to maxFuzz fits.
func locate(lines, old [][]byte, lead, trail, expected, floor, maxFuzz int) (int, int) {
	for fuzz := 0; fuzz <= maxFuzz; fuzz++ {
		if fuzz > 0 && fuzz > lead && fuzz > trail {
			break
		}
		skipLead, skipTrail := min(fuzz, lead), min(fuzz, trail)
		last := len(lines) - len(old) + skipTrail
		for distance := 0; ; distance++ {
			before, after := expected-distance, expected+distance
			if before < floor-skipLead && after > last {
				break
			}
			for _, pos := range []int{before, after} {
				if pos >= floor-skipLead && pos <= last && matches(lines, old, pos, skipLead, skipTrail) {
					return pos, fuzz
				}
				if distance == 0 {
					break
				}
			}
		}
	}
	return -1, 0
}

func matches(lines, old [][]byte, pos, skipLead, skipTrail int) bool {
	for i := skipLead; i < len(old)-skipTrail; i++ {
		at := pos + i
		if at < 0 || at >= len(lines) || !bytes.Equal(lines[at], old[i]) {
			return false
		}
	}
	return true
}

// splitLines keeps each line's terminator, so joining restores src exactly.
func splitLines(src []byte) [][]byte {
	var lines [][]byte
	for len(src) > 0 {
		i := bytes.IndexByte(src, '\n')
		if i < 0 {
			lines = append(lines, src)
			break
		}
		lines = append(lines, src[:i+1])
		src = src[i+1:]
	}
	return lines
}

// Apply applies f to the captured bytes before: text hunks through ApplyText,
// or a Git binary patch. A binary patch that does not fit is an error; text
// hunks report their own failures.
func (f File) Apply(before []byte, opts Options) (Text, error) {
	if f.IsBinary {
		content, err := ApplyBinary(before, f)
		return Text{Content: content}, err
	}
	return ApplyText(before, f.TextFragments, opts), nil
}

// ErrNoReverseBinary reports a binary patch without the reverse fragment
// that -R needs.
var ErrNoReverseBinary = errors.New("binary patch has no reverse hunk")

// Reverse returns the section that undoes f: sides, names, modes and
// fragments swapped.
func Reverse(f File) (File, error) {
	r := *f.File
	r.OldName, r.NewName = f.NewName, f.OldName
	r.IsNew, r.IsDelete = f.IsDelete, f.IsNew
	r.OldMode, r.NewMode = f.NewMode, f.OldMode
	r.OldOIDPrefix, r.NewOIDPrefix = f.NewOIDPrefix, f.OldOIDPrefix
	r.TextFragments = make([]*gitdiff.TextFragment, len(f.TextFragments))
	for i, frag := range f.TextFragments {
		rev := *frag
		rev.OldPosition, rev.NewPosition = frag.NewPosition, frag.OldPosition
		rev.OldLines, rev.NewLines = frag.NewLines, frag.OldLines
		rev.LinesAdded, rev.LinesDeleted = frag.LinesDeleted, frag.LinesAdded
		rev.Lines = make([]gitdiff.Line, len(frag.Lines))
		for j, line := range frag.Lines {
			switch line.Op {
			case gitdiff.OpAdd:
				line.Op = gitdiff.OpDelete
			case gitdiff.OpDelete:
				line.Op = gitdiff.OpAdd
			case gitdiff.OpContext:
			}
			rev.Lines[j] = line
		}
		r.TextFragments[i] = &rev
	}
	if f.IsBinary {
		if f.ReverseBinaryFragment == nil {
			return File{}, ErrNoReverseBinary
		}
		r.BinaryFragment, r.ReverseBinaryFragment = f.ReverseBinaryFragment, f.BinaryFragment
	}
	return File{File: &r, Git: f.Git, rawOld: f.rawNew, rawNew: f.rawOld}, nil
}

// ApplyBinary applies a Git binary patch (literal or delta) to src.
func ApplyBinary(src []byte, f File) ([]byte, error) {
	if f.BinaryFragment == nil {
		return nil, fmt.Errorf("binary patch for %s carries no data", f.NewName)
	}
	var dst bytes.Buffer
	applier := gitdiff.NewBinaryApplier(&dst, bytes.NewReader(src))
	if err := applier.ApplyFragment(f.BinaryFragment); err != nil {
		return nil, err
	}
	if err := applier.Close(); err != nil {
		return nil, err
	}
	return dst.Bytes(), nil
}
