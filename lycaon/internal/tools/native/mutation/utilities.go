package mutation

import (
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
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
		result, err := inv.owners.Operations.Move.Move(inv.ctx, native.MoveInput{Pairs: []native.FilePair{{From: source.Resolved.Abs, To: to}}, NoClobber: noClobber, Directories: true}, inv.tc)
		return len(result.Moved), err
	})
}

func runCp(inv *invocation) error {
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
		result, err := inv.owners.Operations.Copy.Copy(inv.ctx, native.CopyInput{Pairs: []native.FilePair{{From: source.Resolved.Abs, To: to}}, Recursive: recursive, NoClobber: noClobber, Preserve: inv.flag("-p"),
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

// runTouch creates missing files through the pinned write door and sets the
// current time on existing ones through the timestamp metadata operation.
// -a and -m restrict the update to the access or modification time.
func runTouch(inv *invocation) error {
	if len(inv.operands()) == 0 {
		return inv.usage(inv.name(), "at least one PATH")
	}
	now := time.Now()
	access, modify := &now, &now
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
		return 1, nil
	})
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
	return inv.owners.captureDestination(inv.ctx, inv.tc, inv.request.Invocation.Identity.ExecutionID, abs)
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

func runEdit(inv *invocation) error {
	if operands := inv.operands(); len(operands) > 0 {
		return inv.usage(operands[0], "no operands")
	}
	path, ok := inv.value("--path")
	if !ok || path == "" {
		return inv.usage(inv.name(), "--path PATH")
	}
	oldString, ok := inv.value("--old-string")
	if !ok || oldString == "" {
		return inv.usage(inv.name(), "--old-string TEXT")
	}
	newString, ok := inv.value("--new-string")
	if !ok {
		return inv.usage(inv.name(), "--new-string TEXT")
	}
	if oldString == newString {
		return &toolrejection.ToolReject{Code: "EDIT_ARGS_CONFLICT", Data: map[string]any{"reason": "old_string and new_string must differ"}}
	}
	bound, err := inv.bind(path, true)
	if err != nil {
		return err
	}
	source, err := inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(source.Revision); err != nil {
		return err
	}
	destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(destination); err != nil {
		return err
	}
	occurrences := strings.Count(source.Content, oldString)
	if occurrences == 0 {
		return &toolrejection.ToolReject{Code: "EDIT_OLD_STRING_NOT_FOUND", Data: map[string]any{"path": bound.Resolved.DisplayPath}}
	}
	replaceAll := inv.flag("--replace-all")
	if occurrences > 1 && !replaceAll {
		return &toolrejection.ToolReject{Code: "EDIT_OLD_STRING_AMBIGUOUS", Data: map[string]any{"path": bound.Resolved.DisplayPath, "occurrences": occurrences}}
	}
	edited := strings.Replace(source.Content, oldString, newString, 1)
	if replaceAll {
		edited = strings.ReplaceAll(source.Content, oldString, newString)
	}
	result, err := inv.owners.Operations.Write.Apply(inv.ctx, native.WriteInput{Path: bound.Resolved.Abs, Content: edited,
		Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.Revision}}, inv.tc)
	if err != nil {
		return err
	}
	if result.Accepted || result.Saved {
		inv.effects++
	}
	return inv.barrier.Track(result)
}

func runReplaceLines(inv *invocation) error {
	if operands := inv.operands(); len(operands) > 0 {
		return inv.usage(operands[0], "no operands")
	}
	path, ok := inv.value("--path")
	if !ok || path == "" {
		return inv.usage(inv.name(), "--path PATH")
	}
	startRaw, ok := inv.value("--start")
	if !ok {
		return inv.usage(inv.name(), "--start LINE")
	}
	endRaw, ok := inv.value("--end")
	if !ok {
		return inv.usage(inv.name(), "--end LINE")
	}
	content, ok := inv.value("--content")
	if !ok {
		return inv.usage(inv.name(), "--content TEXT")
	}
	startLine, err := strconv.Atoi(startRaw)
	if err != nil {
		return inv.usage(startRaw, "1-based start line")
	}
	endLine, err := strconv.Atoi(endRaw)
	if err != nil {
		return inv.usage(endRaw, "1-based end line")
	}
	bound, err := inv.bind(path, true)
	if err != nil {
		return err
	}
	source, err := inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(source.Revision); err != nil {
		return err
	}
	destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, bound.Resolved.Abs)
	if err != nil {
		return err
	}
	if err := inv.recordPin(destination); err != nil {
		return err
	}
	edited, err := native.ReplaceLineRange(bound.Resolved.DisplayPath, source.Content, startLine, endLine, content)
	if err != nil {
		return err
	}
	if edited == source.Content {
		return nil
	}
	result, err := inv.owners.Operations.Write.Apply(inv.ctx, native.WriteInput{Path: bound.Resolved.Abs, Content: edited,
		Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.Revision}}, inv.tc)
	if err != nil {
		return err
	}
	if result.Accepted || result.Saved {
		inv.effects++
	}
	return inv.barrier.Track(result)
}

func runCodeRewrite(inv *invocation) error {
	if operands := inv.operands(); len(operands) > 0 {
		return inv.usage(operands[0], "no operands")
	}
	pattern, ok := inv.value("--pattern")
	if !ok || strings.TrimSpace(pattern) == "" {
		return inv.usage(inv.name(), "--pattern PATTERN")
	}
	rewrite, ok := inv.value("--rewrite")
	if !ok || strings.TrimSpace(rewrite) == "" {
		return inv.usage(inv.name(), "--rewrite TEXT")
	}
	path, ok := inv.value("--path")
	if !ok || strings.TrimSpace(path) == "" {
		return inv.usage(inv.name(), "--path PATH")
	}
	dryRun := inv.flag("--dry-run")
	recursive := inv.flag("--recursive")
	bound, err := inv.bind(path, !dryRun)
	if err != nil {
		return err
	}
	targets := []string{bound.Resolved.Abs}
	if info, statErr := os.Stat(bound.Resolved.Abs); statErr == nil && info.IsDir() {
		if !recursive {
			return inv.usage(path, "a file path, or pass --recursive for a directory")
		}
		targets = nil
		if walkErr := filepath.WalkDir(bound.Resolved.Abs, func(entry string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && native.SupportsRewriteLanguage(entry) {
				targets = append(targets, entry)
			}
			return nil
		}); walkErr != nil {
			return walkErr
		}
	}
	for _, abs := range targets {
		source, err := inv.owners.Revisions.CaptureSource(inv.ctx, inv.tc, abs)
		if err != nil {
			return err
		}
		if err := inv.recordPin(source.Revision); err != nil {
			return err
		}
		rewritten, changed, err := native.RewriteSource(inv.ctx, source.Path, pattern, rewrite, source.Content)
		if err != nil {
			return err
		}
		if !changed || dryRun {
			continue
		}
		destination, err := inv.owners.Revisions.CaptureDestination(inv.ctx, inv.tc, abs)
		if err != nil {
			return err
		}
		if err := inv.recordPin(destination); err != nil {
			return err
		}
		result, err := inv.owners.Operations.Write.Apply(inv.ctx, native.WriteInput{Path: abs, Content: rewritten,
			Admission: native.WriteExecutionPinned, Destination: destination, Sources: []toolcontract.ExecutionRevision{source.Revision}}, inv.tc)
		if err != nil {
			return err
		}
		if result.Accepted || result.Saved {
			inv.effects++
		}
		if err := inv.barrier.Track(result); err != nil {
			return err
		}
	}
	return nil
}
