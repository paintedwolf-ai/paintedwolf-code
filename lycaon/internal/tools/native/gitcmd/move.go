package gitcmd

import (
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/tools/native"
)

type trackedMove struct {
	from, to string
	entries  []git.IndexEntry
	expected map[string]trackedEntry
	problem  string
}

func (r *fileMutation) move() error {
	operands, err := fileOperands(r.ctx, r.p, r.dir, r.repo.Root)
	if err != nil {
		return err
	}
	destination := operands[len(operands)-1]
	sources := operands[:len(operands)-1]
	info, statErr := os.Stat(entryAbs(r.repo.Root, destination))
	directory := statErr == nil && info.IsDir()
	if len(sources) > 1 && !directory {
		r.fail(128, "fatal: destination '%s' is not a directory", destination)
		return nil
	}
	var plans []trackedMove
	targets := map[string]bool{}
	for _, source := range sources {
		target := destination
		if directory {
			target = path.Join(destination, path.Base(source))
		}
		if has(r.p.Argv, "-n") {
			fmt.Fprintf(r.out, "Checking rename of '%s' to '%s'\n", source, target)
		}
		plan, err := r.planMove(source, target)
		if err != nil {
			return err
		}
		if targets[target] {
			plan.problem = "multiple sources use the same destination"
		}
		targets[target] = true
		if plan.problem != "" && !has(r.p.Argv, "-k") {
			r.fail(128, "fatal: %s, source=%s, destination=%s", plan.problem, source, target)
			return nil
		}
		plans = append(plans, plan)
	}
	for _, plan := range plans {
		if plan.problem != "" {
			if !has(r.p.Argv, "-k") {
				r.fail(128, "fatal: %s, source=%s, destination=%s", plan.problem, plan.from, plan.to)
			}
			continue
		}
		if has(r.p.Argv, "-n") {
			fmt.Fprintf(r.out, "Renaming %s to %s\n", plan.from, plan.to)
			continue
		}
		if _, err := os.Lstat(entryAbs(r.repo.Root, plan.to)); err == nil {
			fmt.Fprintf(r.stderr, "warning: overwriting '%s'\n", plan.to)
		}
		result, err := r.p.filesystem.Operations.Move.Move(r.ctx, native.MoveInput{Memory: r.p.filesystem.Revisions.Memory, Pairs: []native.FilePair{{From: entryAbs(r.repo.Root, plan.from), To: entryAbs(r.repo.Root, plan.to)}}, Directories: true}, r.reviewContext(plan.expected, nil))
		r.effects = r.effects || len(result.Moved) > 0 || len(result.DestinationCopied) > 0
		if err != nil {
			return err
		}
		if err := r.stage(plan.entries, plan.expected, nil); err != nil {
			return err
		}
		if r.status != 0 {
			return nil
		}
		if has(r.p.Argv, "-v") {
			fmt.Fprintf(r.out, "Renaming %s to %s\n", plan.from, plan.to)
		}
	}
	return nil
}

func (r *fileMutation) planMove(source, target string) (trackedMove, error) {
	plan := trackedMove{from: source, to: target, expected: map[string]trackedEntry{}}
	if source == "." || target == "." || source == target || strings.HasPrefix(target, source+"/") {
		plan.problem = "can not move directory into itself"
		return plan, nil
	}
	info, err := os.Lstat(entryAbs(r.repo.Root, source))
	if os.IsNotExist(err) {
		plan.problem = "bad source"
		return plan, nil
	}
	if err != nil {
		return plan, err
	}
	if info.IsDir() {
		if _, err := os.Stat(entryAbs(r.repo.Root, path.Join(source, ".git"))); err == nil {
			return plan, unsupportedFileForm(r.p, source, "a directory without a nested repository")
		}
	}
	destInfo, destErr := os.Lstat(entryAbs(r.repo.Root, target))
	if destErr == nil {
		if destInfo.IsDir() || info.IsDir() {
			plan.problem = "destination already exists"
			return plan, nil
		}
		if !has(r.p.Argv, "-f") {
			plan.problem = "destination exists"
			return plan, nil
		}
	} else if !os.IsNotExist(destErr) {
		return plan, destErr
	}
	tracked := map[string]trackedEntry{}
	for _, entry := range r.index {
		tracked[entry.path] = entry
	}
	var writes []git.IndexEntry
	for _, entry := range r.index {
		if !matchesDirPrefix(entry.path, source) {
			continue
		}
		if entry.mode == "160000" {
			return plan, unsupportedFileForm(r.p, entry.path, "ordinary tracked files")
		}
		if entry.mode != "100644" && entry.mode != "100755" && entry.mode != "120000" {
			return plan, unsupportedFileForm(r.p, entry.path, "regular files or symbolic links")
		}
		content, err := r.plannedContent(entry)
		if err != nil {
			return plan, err
		}
		name := target + strings.TrimPrefix(entry.path, source)
		plan.expected[entry.path] = entry
		plan.expected[name] = tracked[name]
		plan.entries = append(plan.entries, git.IndexEntry{Path: entry.path, Remove: true})
		writes = append(writes, git.IndexEntry{Path: name, Mode: entry.mode, Content: content})
	}
	if len(plan.entries) == 0 {
		plan.problem = "not under version control"
		return plan, nil
	}
	plan.entries = append(plan.entries, writes...)
	return plan, nil
}
