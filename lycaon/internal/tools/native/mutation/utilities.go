package mutation

import (
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// each runs op for every operand, reporting ordinary failures and continuing;
// a typed stop ends the utility with the effects already committed.
func (inv *invocation) each(operands []string, op func(string) (int, error)) error {
	for _, operand := range operands {
		if err := inv.ctx.Err(); err != nil {
			return err
		}
		n, err := op(operand)
		inv.effects += n
		if err == nil {
			continue
		}
		if reason, ok := ordinary(err); ok {
			inv.fail(operand, reason)
			continue
		}
		return err
	}
	return nil
}

func runRm(inv *invocation) error {
	recursive, force := inv.flag("-r"), inv.flag("-f")
	if len(inv.operands()) == 0 && !force {
		return inv.usage(inv.name(), "at least one PATH")
	}
	return inv.each(inv.operands(), func(operand string) (int, error) {
		bound, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		result, err := inv.owners.Operations.Delete.Delete(inv.ctx, native.DeleteInput{Paths: []string{bound.Resolved.Abs}, FilesOnly: !recursive, Recursive: recursive, Force: force}, inv.tc)
		return len(result.Deleted), err
	})
}

func runRmdir(inv *invocation) error {
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH")
	}
	return inv.each(inv.operands(), func(operand string) (int, error) {
		bound, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		if info, err := os.Lstat(bound.Resolved.Abs); err == nil && !info.IsDir() {
			inv.fail(operand, "Not a directory")
			return 0, nil
		}
		result, err := inv.owners.Operations.Delete.Delete(inv.ctx, native.DeleteInput{Paths: []string{bound.Resolved.Abs}}, inv.tc)
		return len(result.Deleted), err
	})
}

// sourcesAndDestination splits SOURCE ... DEST.
func (inv *invocation) sourcesAndDestination() ([]string, string, error) {
	operands := inv.operands()
	if len(operands) < 2 {
		return nil, "", inv.usage(inv.name(), "SOURCE ... DEST")
	}
	return operands[:len(operands)-1], operands[len(operands)-1], nil
}

func runMv(inv *invocation) error {
	sources, dest, err := inv.sourcesAndDestination()
	if err != nil {
		return err
	}
	noClobber := inv.flag("-n")
	return inv.each(sources, func(operand string) (int, error) {
		source, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		to, err := inv.destinationEntry(dest, source.Resolved.Abs, len(sources) > 1)
		if err != nil {
			return 0, err
		}
		result, err := inv.owners.Operations.Move.Move(inv.ctx, native.MoveInput{Pairs: []native.FilePair{{From: source.Resolved.Abs, To: to}}, NoClobber: noClobber}, inv.tc)
		return len(result.Moved), err
	})
}

func runCp(inv *invocation) error {
	if inv.flag("-p") {
		return inv.unsupportedForm("-p", "copies without preserving timestamps or ownership")
	}
	sources, dest, err := inv.sourcesAndDestination()
	if err != nil {
		return err
	}
	recursive, noClobber := inv.flag("-r"), inv.flag("-n")
	return inv.each(sources, func(operand string) (int, error) {
		source, err := inv.bind(operand, false)
		if err != nil {
			return 0, err
		}
		if info, err := os.Lstat(source.Resolved.Abs); err == nil && info.IsDir() && !recursive {
			inv.fail(operand, "-r not specified; omitting directory")
			return 0, nil
		}
		to, err := inv.destinationEntry(dest, source.Resolved.Abs, len(sources) > 1)
		if err != nil {
			return 0, err
		}
		result, err := inv.owners.Operations.Copy.Copy(inv.ctx, native.CopyInput{Pairs: []native.FilePair{{From: source.Resolved.Abs, To: to}}, Recursive: recursive, NoClobber: noClobber,
			MaxFileBytes: inv.owners.limit("recursive_mutation_bytes")}, inv.tc)
		return len(result.Copied), err
	})
}

func runMkdir(inv *invocation) error {
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH")
	}
	mode := os.FileMode(0o755)
	if value, ok := inv.value("-m"); ok {
		parsed, err := strconv.ParseUint(value, 8, 32)
		if err != nil || parsed > 0o777 {
			return inv.usage(value, "an octal permission mode")
		}
		mode = os.FileMode(parsed)
	}
	parents := inv.flag("-p")
	return inv.each(inv.operands(), func(operand string) (int, error) {
		bound, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		if !parents {
			if _, err := os.Lstat(bound.Resolved.Abs); err == nil {
				inv.fail(operand, "File exists")
				return 0, nil
			}
			if info, err := os.Stat(filepath.Dir(bound.Resolved.Abs)); err != nil || !info.IsDir() {
				inv.fail(operand, "No such file or directory")
				return 0, nil
			}
		}
		created, err := inv.owners.Operations.Mkdir.Mkdir(inv.ctx, native.MkdirInput{Paths: []string{bound.Resolved.Abs}, Mode: mode}, inv.tc)
		return len(created), err
	})
}

func runChmod(inv *invocation) error {
	operands := inv.operands()
	if len(operands) < 2 {
		return inv.usage(inv.name(), "MODE PATH ...")
	}
	mode := operands[0]
	return inv.each(operands[1:], func(operand string) (int, error) {
		bound, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		results, err := inv.owners.Operations.Chmod.Chmod(inv.ctx, []string{bound.Resolved.Abs}, mode, inv.tc)
		return len(results), err
	})
}

// runTouch creates missing files through the pinned write door. Updating an
// existing file's timestamps has no shared owner operation, so it refuses.
func runTouch(inv *invocation) error {
	for _, key := range []string{"-m", "-a"} {
		if inv.flag(key) {
			return inv.unsupportedForm(key, "creation of missing files only")
		}
	}
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH")
	}
	noCreate := inv.flag("-c")
	return inv.each(inv.operands(), func(operand string) (int, error) {
		bound, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		target, err := inv.capture(bound.Resolved.Abs)
		if err != nil {
			return 0, err
		}
		if target.pin.Mode != toolcontract.AbsentRevision {
			return 0, inv.unsupportedForm(operand, "creation of missing files; existing timestamps are not updated")
		}
		if noCreate {
			return 0, nil
		}
		if _, err := inv.owners.publish(inv.ctx, inv.tc, inv.barrier, target, ""); err != nil {
			return 0, err
		}
		return 1, nil
	})
}

func (inv *invocation) capture(abs string) (pinnedTarget, error) {
	target, err := inv.owners.captureDestination(inv.ctx, inv.tc, inv.request.Invocation.Identity.ExecutionID, inv.pins, abs)
	if err == nil {
		inv.pins = append(inv.pins, target.pin)
	}
	return target, err
}

// runTee captures every destination before reading stdin, copies stdin to
// stdout, and publishes each destination only after stdin completes.
func runTee(inv *invocation) error {
	appendMode := inv.flag("-a")
	var targets []pinnedTarget
	for _, operand := range inv.operands() {
		bound, err := inv.paths.Bind(inv.ctx, operand, true)
		if err != nil {
			return err
		}
		if bound.Null {
			continue
		}
		target, err := inv.capture(bound.Resolved.Abs)
		if err != nil {
			return err
		}
		target.append = appendMode
		targets = append(targets, target)
	}
	maximum := inv.owners.limit("stream_bytes")
	data, err := io.ReadAll(io.LimitReader(inv.input, maximum+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maximum {
		return &commandprogram.StreamLimitError{Limit: "stream_bytes", Maximum: maximum, Observed: int64(len(data))}
	}
	if _, err := inv.stdout.Write(data); err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := inv.owners.publish(inv.ctx, inv.tc, inv.barrier, target, string(data)); err != nil {
			return err
		}
		inv.effects++
	}
	return nil
}

const mktempAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

// runMktemp creates a uniquely named file or directory in the session's real
// TMPDIR, the directory child processes receive, and prints its absolute path.
func runMktemp(inv *invocation) error {
	dir, err := inv.paths.TemporaryDirectory(inv.ctx)
	if err != nil {
		return err
	}
	template := "tmp.XXXXXXXXXX"
	if prefix, ok := inv.value("-t"); ok {
		template = prefix + ".XXXXXXXXXX"
	}
	operands := inv.operands()
	if len(operands) > 1 {
		return inv.usage(operands[1], "at most one TEMPLATE")
	}
	if len(operands) == 1 {
		template = operands[0]
	}
	if strings.ContainsRune(template, '/') || strings.ContainsRune(template, filepath.Separator) {
		return inv.usage(template, "a file name template; mktemp creates in the session TMPDIR")
	}
	stem := strings.TrimRight(template, "X")
	if len(template)-len(stem) < 3 {
		return inv.usage(template, "a template ending in at least three X characters")
	}
	name, err := randomName(stem, len(template)-len(stem))
	if err != nil {
		return err
	}
	abs := filepath.Join(dir, name)
	if inv.flag("-d") {
		created, err := inv.owners.Operations.Mkdir.Mkdir(inv.ctx, native.MkdirInput{Paths: []string{abs}, Mode: 0o700}, inv.tc)
		inv.effects += len(created)
		if err != nil {
			return err
		}
	} else {
		target, err := inv.capture(abs)
		if err != nil {
			return err
		}
		if target.pin.Mode != toolcontract.AbsentRevision {
			return inv.usage(name, "an unused temporary name")
		}
		if _, err := inv.owners.publish(inv.ctx, inv.tc, inv.barrier, target, ""); err != nil {
			return err
		}
		inv.effects++
	}
	_, err = io.WriteString(inv.stdout, abs+"\n")
	return err
}

func randomName(stem string, n int) (string, error) {
	random := make([]byte, n)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	for i := range random {
		random[i] = mktempAlphabet[int(random[i])%len(mktempAlphabet)]
	}
	return stem + string(random), nil
}
