package mutation

import (
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/tools/native"
)

func runLn(inv *invocation) error {
	operands := inv.operands()
	if len(operands) != 2 {
		return inv.usage(inv.name(), "TARGET LINK")
	}
	if !inv.flag("-s") && !inv.flag("-r") {
		return inv.unsupportedForm("ln", "hard links are unavailable; use ln -s or cp")
	}
	target, link := operands[0], operands[1]
	bound, err := inv.bind(link, true)
	if err != nil {
		return err
	}
	to := bound.Resolved.Abs
	if info, statErr := os.Lstat(to); statErr == nil {
		isDirectory := info.IsDir()
		if info.Mode()&os.ModeSymlink != 0 && !inv.flag("-n") {
			followed, followErr := os.Stat(to)
			isDirectory = followErr == nil && followed.IsDir()
		}
		if isDirectory {
			to = filepath.Join(to, filepath.Base(target))
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if inv.flag("-r") {
		source, err := inv.bind(target, false)
		if err != nil {
			return err
		}
		target, err = filepath.Rel(filepath.Dir(to), source.Resolved.Abs)
		if err != nil {
			return inv.usage(operands[0], "a target on the same volume as the link")
		}
	}
	pin, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, to)
	if err != nil {
		return err
	}
	if err := inv.recordPin(pin); err != nil {
		return err
	}
	if err := inv.owners.Operations.Write.Link(inv.ctx, native.LinkInput{Path: to, Target: target, Force: inv.flag("-f"), Destination: pin, Memory: inv.owners.Revisions.Memory}, inv.tc); err != nil {
		if reason, ok := ordinary(err); ok {
			inv.fail(link, reason)
			return nil
		}
		return err
	}
	inv.effects++
	inv.verbose("%s -> %s", quote(link), quote(target))
	return nil
}
