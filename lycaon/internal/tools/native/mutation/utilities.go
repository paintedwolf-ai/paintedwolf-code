package mutation

import (
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/fseffect"
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
		directories, err := inv.directoriesUnder(bound.Resolved.Abs)
		if err != nil {
			return 0, err
		}
		result, err := inv.owners.Operations.Delete.Delete(inv.ctx, native.DeleteInput{Paths: []string{bound.Resolved.Abs}, FilesOnly: !recursive, Recursive: recursive, Force: force}, inv.tc)
		// The owner reports the operand as given and the entries beneath it
		// scope-relative; both map back onto the bound path.
		scope := filepath.ToSlash(bound.Resolved.ScopeRel) + "/"
		for _, deleted := range result.Deleted {
			abs := bound.Resolved.Abs
			if rel, ok := strings.CutPrefix(deleted, scope); ok {
				abs = filepath.Join(abs, filepath.FromSlash(rel))
			}
			kind := "removed"
			if directories[abs] {
				kind = "removed directory"
			}
			inv.verbose("%s %s", kind, quote(displayed(abs, bound.Resolved.Abs, operand)))
		}
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
		result, err := inv.owners.Operations.Move.Move(inv.ctx, native.MoveInput{Pairs: []native.FilePair{{From: source.Resolved.Abs, To: to}}, NoClobber: noClobber, Directories: true}, inv.tc)
		if len(result.Moved) > 0 {
			inv.verbose("renamed %s -> %s", quote(operand), quote(inv.destinationDisplay(dest, to)))
		}
		return len(result.Moved), err
	})
}

func runCp(inv *invocation) error {
	sources, dest, err := inv.sourcesAndDestination()
	if err != nil {
		return err
	}
	archive := inv.flag("-a")
	recursive, noClobber, preserve := inv.flag("-r") || archive, inv.flag("-n"), inv.flag("-p") || archive
	return inv.each(sources, func(operand string) (int, error) {
		source, err := inv.bind(operand, false)
		if err != nil {
			return 0, err
		}
		if archive {
			if err := inv.refuseLinks(operand, source.Resolved.Abs); err != nil {
				return 0, err
			}
		}
		if info, err := os.Lstat(source.Resolved.Abs); err == nil && info.IsDir() && !recursive {
			inv.fail(operand, "-r not specified; omitting directory")
			return 0, nil
		}
		to, err := inv.destinationEntry(dest, source.Resolved.Abs, len(sources) > 1)
		if err != nil {
			return 0, err
		}
		result, err := inv.owners.Operations.Copy.Copy(inv.ctx, native.CopyInput{Pairs: []native.FilePair{{From: source.Resolved.Abs, To: to}}, Recursive: recursive, NoClobber: noClobber, Preserve: preserve,
			MaxFileBytes: inv.owners.limit("recursive_mutation_bytes")}, inv.tc)
		shown := inv.destinationDisplay(dest, to)
		for _, copied := range result.Copied {
			inv.verbose("%s -> %s", quote(displayed(copied.From, source.Resolved.Abs, operand)), quote(displayed(copied.To, to, shown)))
		}
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
		// The owner reports the requested directory; -pv also names each
		// missing parent it creates, outermost first.
		var missing []string
		for dir := bound.Resolved.Abs; ; dir = filepath.Dir(dir) {
			if _, err := os.Lstat(dir); err == nil || filepath.Dir(dir) == dir {
				break
			}
			missing = append([]string{dir}, missing...)
		}
		created, err := inv.owners.Operations.Mkdir.Mkdir(inv.ctx, native.MkdirInput{Paths: []string{bound.Resolved.Abs}, Mode: mode}, inv.tc)
		if len(created) > 0 {
			for _, dir := range missing {
				inv.verbose("mkdir: created directory %s", quote(ancestorDisplay(dir, bound.Resolved.Abs, operand)))
			}
		}
		return len(created), err
	})
}

// ancestorDisplay names a created parent of PATH as the matching prefix of
// the operand, as mkdir -pv does.
func ancestorDisplay(dir, boundAbs, operand string) string {
	rel, err := filepath.Rel(dir, boundAbs)
	if err != nil || rel == "." {
		return operand
	}
	shown := strings.TrimSuffix(operand, "/")
	for range strings.Split(rel, string(filepath.Separator)) {
		shown = strings.TrimSuffix(path.Dir(shown), "/")
	}
	return shown
}

// directoriesUnder records which paths of a tree rm -v is about to remove
// are directories, so its messages name them as GNU rm does.
func (inv *invocation) directoriesUnder(abs string) (map[string]bool, error) {
	if !inv.flag("-v") {
		return nil, nil
	}
	directories := map[string]bool{}
	maximum, seen := inv.owners.limit("recursive_mutation_files"), int64(0)
	err := filepath.WalkDir(abs, func(walked string, entry fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if seen++; seen > maximum {
			return limitRefusal("recursive_mutation_files", "files", maximum, seen)
		}
		if entry.IsDir() {
			directories[walked] = true
		}
		return nil
	})
	return directories, err
}

// refuseLinks stops cp -a before copying a tree that holds a symbolic link:
// -a copies links as links, and no owner creates links.
func (inv *invocation) refuseLinks(operand, abs string) error {
	maximum, seen := inv.owners.limit("recursive_mutation_files"), int64(0)
	return filepath.WalkDir(abs, func(walked string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if seen++; seen > maximum {
			return limitRefusal("recursive_mutation_files", "files", maximum, seen)
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return inv.unsupportedForm(displayed(walked, abs, operand), "cp -a preserves symbolic links as links; native link creation is unavailable, so use cp -R -p to copy the link targets")
		}
		return nil
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

// runTouch creates missing files through the pinned write door and sets the
// current time on existing ones through the timestamp metadata operation.
// -a and -m restrict the update to the access or modification time.
func runTouch(inv *invocation) error {
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH")
	}
	now := time.Now()
	accessAt, modifyAt, err := inv.touchTimes(now)
	if err != nil {
		return err
	}
	explicit := !accessAt.Equal(now) || !modifyAt.Equal(now)
	access, modify := &accessAt, &modifyAt
	if inv.flag("-a") && !inv.flag("-m") {
		modify = nil
	}
	if inv.flag("-m") && !inv.flag("-a") {
		access = nil
	}
	noCreate := inv.flag("-c")
	return inv.each(inv.operands(), func(operand string) (int, error) {
		bound, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		if _, err := os.Lstat(bound.Resolved.Abs); err == nil {
			changed, err := inv.owners.Operations.Times.SetTimes(inv.ctx, []string{bound.Resolved.Abs}, access, modify, inv.tc)
			return len(changed), err
		}
		if noCreate {
			return 0, nil
		}
		target, err := inv.capture(bound.Resolved.Abs)
		if err != nil {
			return 0, err
		}
		if _, err := inv.owners.publish(inv.ctx, inv.tc, inv.barrier, target, []byte{}); err != nil {
			return 0, err
		}
		if explicit {
			if _, err := inv.owners.Operations.Times.SetTimes(inv.ctx, []string{bound.Resolved.Abs}, access, modify, inv.tc); err != nil {
				return 1, err
			}
		}
		return 1, nil
	})
}

// touchTimes resolves -t, -d or -r into the access and modification times;
// without them both are the invocation's captured clock.
func (inv *invocation) touchTimes(now time.Time) (time.Time, time.Time, error) {
	stamp, hasStamp := inv.value("-t")
	date, hasDate := inv.value("-d")
	reference, hasReference := inv.value("-r")
	switch {
	case btoi(hasStamp)+btoi(hasDate)+btoi(hasReference) > 1:
		return now, now, inv.usage(inv.name(), "at most one of -t, -d and -r")
	case hasStamp:
		t, ok := parseStamp(stamp, now)
		if !ok {
			return now, now, inv.usage(stamp, "a time as [[CC]YY]MMDDhhmm[.ss]")
		}
		return t, t, nil
	case hasDate:
		t, ok := parseDate(date, now)
		if !ok {
			return now, now, inv.usage(date, "a date such as 2026-10-20 12:00:00, 2026-10-20T12:00:00Z, @SECONDS, yesterday or 2 hours ago")
		}
		return t, t, nil
	case hasReference:
		bound, err := inv.bind(reference, false)
		if err != nil {
			return now, now, err
		}
		info, err := os.Stat(bound.Resolved.Abs)
		if err != nil {
			return now, now, inv.usage(reference, "an existing reference file")
		}
		accessed, ok := fseffect.AccessTime(info)
		if !ok {
			accessed = info.ModTime()
		}
		return accessed, info.ModTime(), nil
	}
	return now, now, nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// runChown accepts the numeric UID[:GID] form the catalog declares and applies
// the existing ownership policy; an omitted GID keeps each file's group.
func runChown(inv *invocation) error {
	operands := inv.operands()
	if len(operands) < 2 {
		return inv.usage(inv.name(), "UID[:GID] PATH ...")
	}
	owner, group, hasGroup := strings.Cut(operands[0], ":")
	if !numericID(owner) || (hasGroup && !numericID(group)) {
		return inv.usage(operands[0], "numeric UID[:GID]")
	}
	if !hasGroup {
		group = "current"
	}
	uid, gid, err := native.ResolveChownIdentities(owner, group)
	if err != nil {
		return err
	}
	if !hasGroup {
		gid = -1
	}
	return inv.each(operands[1:], func(operand string) (int, error) {
		bound, err := inv.bind(operand, true)
		if err != nil {
			return 0, err
		}
		results, err := inv.owners.Operations.Chown.Chown(inv.ctx, native.ChownInput{Paths: []string{bound.Resolved.Abs}, UID: uid, GID: gid}, inv.tc)
		return len(results), err
	})
}

func numericID(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
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
		if _, err := inv.owners.publish(inv.ctx, inv.tc, inv.barrier, target, data); err != nil {
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
		if _, err := inv.owners.publish(inv.ctx, inv.tc, inv.barrier, target, []byte{}); err != nil {
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
