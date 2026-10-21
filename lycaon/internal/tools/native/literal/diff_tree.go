package literal

import (
	"fmt"
	"path"
	"slices"

	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

// diffDirectories compares two trees in unsigned byte name order. Without
// -r only the top level is compared and common subdirectories are named.
func (inv *invocation) diffDirectories(left, right string, opts diffOptions) error {
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
		inv.diffVisits++
		if visits > 0 && inv.diffVisits > visits {
			return limitExceeded("traversal_visits", visits, inv.diffVisits)
		}
		a, b := path.Join(left, name), path.Join(right, name)
		inLeft, inRight := slices.Contains(leftNames, name), slices.Contains(rightNames, name)
		if !inLeft || !inRight {
			only := left
			if inRight {
				only = right
			}
			inv.status = max(inv.status, 1)
			if err := inv.out.text(fmt.Sprintf("Only in %s: %s\n", only, name)); err != nil {
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
			if err := inv.diffDirectories(a, b, opts); err != nil {
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

func (inv *invocation) directoryNames(label string) ([]string, bool, error) {
	bound, err := inv.paths.Bind(inv.ctx, label, false)
	if err == nil {
		var names []string
		if names, err = bound.ReadDir(); err == nil {
			return names, true, nil
		}
	}
	err = projectpaths.PathOperationError(label, err)
	if reason, ok := ordinary(err); ok {
		inv.fail(2, label, reason)
		return nil, false, nil
	}
	return nil, false, err
}
