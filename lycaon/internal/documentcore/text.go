package documentcore

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/aymanbagabas/go-udiff"
)

// TextEdits converts byte ranges from the diff into descending UTF-16 ranges.
func TextEdits(before, after string) []Edit {
	return utf16Edits(before, udiff.Strings(before, after))
}

// LineEdits protects the complete source lines an agent used to plan a rewrite.
func LineEdits(before, after string) ([]Edit, error) {
	diff, err := lineEdits(before, after)
	if err != nil {
		return nil, err
	}
	return utf16Edits(before, diff), nil
}

// HunkEdits returns the whole-line hunks a rewrite touched, as guards, and
// the character edits confined to each hunk, so every edit lands inside a
// guarded line and an inserted line anchors at its boundary.
func HunkEdits(before, after string) (guards, edits []Edit, err error) {
	hunks, err := lineEdits(before, after)
	if err != nil {
		return nil, nil, err
	}
	confined := make([]udiff.Edit, 0, len(hunks))
	for _, hunk := range hunks {
		for _, edit := range udiff.Strings(before[hunk.Start:hunk.End], hunk.New) {
			confined = append(confined, udiff.Edit{Start: hunk.Start + edit.Start, End: hunk.Start + edit.End, New: edit.New})
		}
	}
	return utf16Edits(before, hunks), utf16Edits(before, confined), nil
}

// ReplacementEdits keeps each changed line hunk together while retaining its
// unchanged ends. Concurrent deletions cannot fragment its replacement text.
func ReplacementEdits(before, after string) ([]Edit, error) {
	diff, err := lineEdits(before, after)
	if err != nil {
		return nil, err
	}
	for index := range diff {
		edit := &diff[index]
		for edit.Start < edit.End && len(edit.New) > 0 {
			old, size := utf8.DecodeRuneInString(before[edit.Start:edit.End])
			next, _ := utf8.DecodeRuneInString(edit.New)
			if old != next {
				break
			}
			edit.Start += size
			edit.New = edit.New[size:]
		}
		for edit.Start < edit.End && len(edit.New) > 0 {
			old, size := utf8.DecodeLastRuneInString(before[edit.Start:edit.End])
			next, _ := utf8.DecodeLastRuneInString(edit.New)
			if old != next {
				break
			}
			edit.End -= size
			edit.New = edit.New[:len(edit.New)-size]
		}
	}
	return utf16Edits(before, diff), nil
}

// lineEdits diffs by whole lines, so an inserted line is an insertion
// between its neighbours and never a rewrite of one of them.
func lineEdits(before, after string) ([]udiff.Edit, error) {
	symbols := map[string]rune{}
	encode := func(text string) (string, []int, bool) {
		lines := strings.SplitAfter(text, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		var b strings.Builder
		offsets := make([]int, 0, len(lines)+1)
		offset := 0
		for _, line := range lines {
			symbol, ok := symbols[line]
			if !ok {
				if symbol, ok = lineSymbol(len(symbols)); !ok {
					return "", nil, false
				}
				symbols[line] = symbol
			}
			offsets = append(offsets, offset)
			offset += len(line)
			b.WriteRune(symbol)
		}
		offsets = append(offsets, offset)
		return b.String(), offsets, true
	}
	a, beforeOffsets, ok := encode(before)
	if !ok {
		return nil, errors.New("too many distinct lines to compare")
	}
	b, afterOffsets, ok := encode(after)
	if !ok {
		return nil, errors.New("too many distinct lines to compare")
	}
	// Symbol offsets count runes; one rune is one line.
	lineAt := func(byteOffset int) int { return utf8.RuneCountInString(a[:byteOffset]) }
	edits := udiff.Strings(a, b)
	diff := make([]udiff.Edit, 0, len(edits))
	afterLine, previousEnd := 0, 0
	for _, edit := range edits {
		startLine, endLine := lineAt(edit.Start), lineAt(edit.End)
		afterLine += startLine - previousEnd
		added := utf8.RuneCountInString(edit.New)
		diff = append(diff, udiff.Edit{
			Start: beforeOffsets[startLine], End: beforeOffsets[endLine],
			New: after[afterOffsets[afterLine]:afterOffsets[afterLine+added]],
		})
		afterLine += added
		previousEnd = endLine
	}
	return diff, nil
}

func utf16Edits(before string, diff []udiff.Edit) []Edit {
	if len(diff) == 0 {
		return nil
	}
	positions := make(map[int]uint32, len(diff)*2)
	for _, e := range diff {
		positions[e.Start] = 0
		positions[e.End] = 0
	}
	var offset uint32
	for index, r := range before {
		if _, ok := positions[index]; ok {
			positions[index] = offset
		}
		if r > 0xffff {
			offset += 2
		} else {
			offset++
		}
	}
	positions[len(before)] = offset
	result := make([]Edit, 0, len(diff))
	for i := len(diff) - 1; i >= 0; i-- {
		e := diff[i]
		result = append(result, Edit{Index: positions[e.Start], Delete: positions[e.End] - positions[e.Start], Insert: e.New})
	}
	return result
}

// ApplyEdits materializes a resolved plan without mutating its CRDT.
func ApplyEdits(before string, edits []Edit) (string, error) {
	positions := make(map[uint32]int, len(edits)*2)
	for _, e := range edits {
		if e.Index+e.Delete < e.Index {
			return "", errors.New("invalid resolved document range")
		}
		positions[e.Index] = -1
		positions[e.Index+e.Delete] = -1
	}
	var offset uint32
	for index, r := range before {
		if _, ok := positions[offset]; ok {
			positions[offset] = index
		}
		if r > 0xffff {
			offset += 2
		} else {
			offset++
		}
	}
	positions[offset] = len(before)
	var result strings.Builder
	last := 0
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		start, end := positions[e.Index], positions[e.Index+e.Delete]
		if start < last || end < start || end > len(before) {
			return "", errors.New("invalid resolved document range")
		}
		result.WriteString(before[last:start])
		result.WriteString(e.Insert)
		last = end
	}
	result.WriteString(before[last:])
	return result.String(), nil
}

// LineChange is one changed hunk of a text, in the text's 1-based lines.
type LineChange struct {
	// StartLine and EndLine are the removed lines. An insertion removes none
	// and names the line it goes in before.
	StartLine, EndLine int
	Insertion          bool
	Added, Removed     int
}

// ChangedLines returns the exact line hunks that turn before into after.
// Lines are diffed as whole symbols, so unchanged lines always separate hunks.
func ChangedLines(before, after string) ([]LineChange, error) {
	symbols := map[string]rune{}
	tooMany := false
	encode := func(text string) (string, []int) {
		lines := strings.SplitAfter(text, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		var b strings.Builder
		offsets := make([]int, 0, len(lines)+1)
		for _, line := range lines {
			symbol, ok := symbols[line]
			if !ok {
				if symbol, ok = lineSymbol(len(symbols)); !ok {
					tooMany = true
					return "", nil
				}
				symbols[line] = symbol
			}
			offsets = append(offsets, b.Len())
			b.WriteRune(symbol)
		}
		offsets = append(offsets, b.Len())
		return b.String(), offsets
	}
	a, offsets := encode(before)
	b, _ := encode(after)
	if tooMany {
		return nil, errors.New("too many distinct lines to compare")
	}
	lineAt := func(byteOffset int) int {
		index, _ := slices.BinarySearch(offsets, byteOffset)
		return index
	}
	edits := udiff.Strings(a, b)
	out := make([]LineChange, 0, len(edits))
	for _, edit := range edits {
		start, end := lineAt(edit.Start), lineAt(edit.End)
		change := LineChange{StartLine: start + 1, Removed: end - start, Added: utf8.RuneCountInString(edit.New)}
		if change.Removed == 0 {
			change.Insertion, change.EndLine = true, change.StartLine
		} else {
			change.EndLine = end
		}
		out = append(out, change)
	}
	return out, nil
}

// maxLineSymbols is how many distinct lines fit in the Unicode scalar values
// above NUL once the surrogate block is skipped.
const maxLineSymbols = utf8.MaxRune - 0x800

// lineSymbol maps a line index to a distinct rune, skipping surrogates. It
// fails once the text has more distinct lines than there are scalar values.
func lineSymbol(index int) (rune, bool) {
	if index < 0 || index >= maxLineSymbols {
		return 0, false
	}
	symbol := rune(index + 1) // #nosec G115 -- index < maxLineSymbols, below utf8.MaxRune
	if symbol >= 0xD800 {
		symbol += 0x800
	}
	return symbol, true
}
