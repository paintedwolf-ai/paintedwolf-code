package mutation

import (
	"errors"
	"io/fs"
	"os"

	"github.com/lycaon/lycaon/internal/sandbox"
)

func missingEntry(_ string, err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// visitTree counts the explicit root and every descendant before mutation.
func (inv *invocation) visitTree(abs string, onReadError func(string, error) error, visit func(sandbox.SurveyEntry) error) error {
	if err := inv.ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		if canceled := inv.ctx.Err(); canceled != nil {
			return canceled
		}
		if onReadError != nil {
			return onReadError(".", err)
		}
		return err
	}
	maximum, seen := inv.owners.limit("recursive_mutation_files"), int64(0)
	inspect := func(entry sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
		if err := inv.ctx.Err(); err != nil {
			return sandbox.SurveyStop, err
		}
		seen++
		if seen > maximum {
			return sandbox.SurveyStop, limitRefusal("recursive_mutation_files", "files", maximum, seen)
		}
		return sandbox.SurveyContinue, visit(entry)
	}
	if _, err := inspect(sandbox.SurveyEntry{Rel: ".", Abs: abs, IsDir: info.IsDir(), IsSymlink: info.Mode()&fs.ModeSymlink != 0}); err != nil {
		return err
	}
	if !info.IsDir() {
		return nil
	}
	options := sandbox.HumanFilesSurveyOptions()
	options.OnReadError = onReadError
	err = sandbox.SurveyWalk(inv.ctx, abs, options, inspect)
	if inv.ctx.Err() != nil {
		return inv.ctx.Err()
	}
	// The root may disappear between inspection and traversal.
	if err != nil && onReadError != nil && errors.Is(err, fs.ErrNotExist) {
		return onReadError(".", err)
	}
	return err
}

func (inv *invocation) directoriesUnder(abs string) (map[string]bool, error) {
	if !inv.flag("-v") {
		return nil, nil
	}
	directories := map[string]bool{}
	err := inv.visitTree(abs, missingEntry, func(entry sandbox.SurveyEntry) error {
		if entry.IsDir {
			directories[entry.Abs] = true
		}
		return nil
	})
	return directories, err
}

func (inv *invocation) refuseLinks(operand, abs string) error {
	return inv.visitTree(abs, nil, func(entry sandbox.SurveyEntry) error {
		if entry.IsSymlink {
			return inv.unsupportedForm(displayed(entry.Abs, abs, operand), "cp -a preserves symbolic links as links; native link creation is unavailable, so use cp -R -p to copy the link targets")
		}
		return nil
	})
}
