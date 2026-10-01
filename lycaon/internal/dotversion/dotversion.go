// Package dotversion compares dotted numeric versions field by field.
//
// Fields compare as numbers, so "9.0" ranks below "13.0".
//
// Missing fields compare as zero, so "13" and "13.0.0" are equal.
package dotversion

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnparseable marks a version string the comparator could not read.
var ErrUnparseable = errors.New("unparseable version")

// Compare returns -1, 0, or 1 for a against b.
func Compare(a, b string) (int, error) {
	aParts, err := parse(a)
	if err != nil {
		return 0, err
	}
	bParts, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < len(aParts) || i < len(bParts); i++ {
		var av, bv int
		if i < len(aParts) {
			av = aParts[i]
		}
		if i < len(bParts) {
			bv = bParts[i]
		}
		if av != bv {
			if av < bv {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

// parse splits a dotted numeric version into its fields.
func parse(v string) ([]int, error) {
	fields := strings.Split(strings.TrimSpace(v), ".")
	out := make([]int, 0, len(fields))
	for _, field := range fields {
		n, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			return nil, fmt.Errorf("%w: %q", ErrUnparseable, v)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrUnparseable, v)
	}
	return out, nil
}
