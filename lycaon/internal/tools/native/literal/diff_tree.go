package literal

import (
	"fmt"
	"path"
	"slices"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// diffDirectories compares two trees in unsigned byte name order, leaving out
// names an exclusion matches. Without -r only the top level is compared and
// common subdirectories are named. Under -N an entry on one side only is
// compared with an empty file or directory on the other; an absent side
// lists nothing.
func (inv *invocation) diffDirectories(left, right diffSide, opts diffOptions) error {
	leftNames, ok, err := inv.directoryNames(left)
	if err != nil || !ok {
		return err
	}
	rightNames, ok, err := inv.directoryNames(right)
	if err != nil || !ok {
		return err
	}
	names := append(slices.Clone(leftNames), rightNames...)
	slices.Sort(names)
	names = slices.Compact(names)
	visits := inv.owners.limit("traversal_visits")
	for _, name := range names {
		if opts.excluded(name) {
			continue
		}
		inv.diffVisits++
		if visits > 0 && inv.diffVisits > visits {
			return tools.CommandLimitExceeded("traversal_visits", visits, inv.diffVisits)
		}
		a, b := path.Join(left.label, name), path.Join(right.label, name)
		inLeft, inRight := slices.Contains(leftNames, name), slices.Contains(rightNames, name)
		if !inLeft || !inRight {
			if err := inv.diffOneSided(left.label, right.label, name, inLeft, opts); err != nil {
				return err
			}
			continue
		}
		stA, okA, err := inv.entry(a, true)
		if err != nil {
			return err
		}
		stB, okB, err := inv.entry(b, true)
		if err != nil {
			return err
		}
		if !okA || !okB {
			continue
		}
		dirA, dirB := typeLetter(stA.Mode) == 'd', typeLetter(stB.Mode) == 'd'
		switch {
		case dirA && dirB && opts.recursive:
			if err := inv.diffDirectories(diffSide{label: a, dir: true}, diffSide{label: b, dir: true}, opts); err != nil {
				return err
			}
		case dirA && dirB:
			if err := inv.out.text(fmt.Sprintf("Common subdirectories: %s and %s\n", a, b)); err != nil {
				return err
			}
		case dirA || dirB:
			inv.status = max(inv.status, 1)
			if err := inv.out.text(fmt.Sprintf("File %s is a %s while file %s is a %s\n", a, gnuType(stA), b, gnuType(stB))); err != nil {
				return err
			}
		default:
			if err := inv.diffFiles(diffSide{label: a, modTime: stA.ModTime}, diffSide{label: b, modTime: stB.ModTime}, opts, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// diffOneSided reports an entry present on one side only: "Only in" without
// -N, otherwise a comparison with the empty counterpart.
func (inv *invocation) diffOneSided(leftDir, rightDir, name string, inLeft bool, opts diffOptions) error {
	a, b := path.Join(leftDir, name), path.Join(rightDir, name)
	present, missing, holder := a, b, leftDir
	if !inLeft {
		present, missing, holder = b, a, rightDir
	}
	if !opts.newFile {
		inv.status = max(inv.status, 1)
		return inv.out.text(fmt.Sprintf("Only in %s: %s\n", holder, name))
	}
	st, ok, err := inv.entry(present, true)
	if err != nil || !ok {
		return err
	}
	side, absent := diffSide{label: present, modTime: st.ModTime, dir: typeLetter(st.Mode) == 'd'}, absentSide(missing)
	left, right := side, absent
	if !inLeft {
		left, right = absent, side
	}
	switch {
	case side.dir && opts.recursive:
		return inv.diffDirectories(left, right, opts)
	case side.dir:
		return inv.out.text(fmt.Sprintf("Common subdirectories: %s and %s\n", a, b))
	}
	return inv.diffFiles(left, right, opts, true)
}

// directoryNames lists a directory's entries; an absent side has none.
func (inv *invocation) directoryNames(side diffSide) ([]string, bool, error) {
	if side.absent {
		return nil, true, nil
	}
	bound, err := inv.paths.Bind(inv.ctx, side.label, false)
	if err == nil {
		var names []string
		if names, err = bound.ReadDir(); err == nil {
			return names, true, nil
		}
	}
	err = projectpaths.PathOperationError(side.label, err)
	if reason, ok := ordinary(err); ok {
		inv.fail(2, side.label, reason)
		return nil, false, nil
	}
	return nil, false, err
}
