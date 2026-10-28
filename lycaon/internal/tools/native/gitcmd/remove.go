package gitcmd

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
)

func (r *fileMutation) remove() error {
	operands, err := fileOperands(r.ctx, r.p, r.dir, r.repo.Root)
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for i, operand := range operands {
		matched := false
		for _, entry := range r.index {
			if entry.path == operand || matchesDirPrefix(entry.path, operand) || wildmatch(operand, entry.path) {
				if entry.mode == "160000" {
					return unsupportedFileForm(r.p, entry.path, "ordinary tracked files")
				}
				if entry.path != operand && matchesDirPrefix(entry.path, operand) && !has(r.p.Argv, "-r") {
					r.fail(128, "fatal: not removing '%s' recursively without -r", r.p.Argv.Operands()[i].Value)
					return nil
				}
				matched = true
				selected[entry.path] = true
			}
		}
		if !matched && !has(r.p.Argv, "--ignore-unmatch") {
			r.fail(128, "fatal: pathspec '%s' did not match any files", r.p.Argv.Operands()[i].Value)
			return nil
		}
	}
	var entries []trackedEntry
	expected := map[string]trackedEntry{}
	for _, entry := range r.index {
		if selected[entry.path] {
			entries = append(entries, entry)
			expected[entry.path] = entry
		}
	}
	if !has(r.p.Argv, "-f") {
		problems := map[string][]string{}
		for _, entry := range entries {
			problem, err := r.removable(entry)
			if err != nil {
				return err
			}
			if problem != "" {
				problems[problem] = append(problems[problem], entry.path)
			}
		}
		for _, problem := range []string{"both", "staged", "local"} {
			if len(problems[problem]) > 0 {
				r.removeDiagnostic(problem, problems[problem])
			}
		}
		if r.status != 0 {
			return nil
		}
	}
	if has(r.p.Argv, "-n") {
		for _, entry := range entries {
			if !has(r.p.Argv, "-q") {
				fmt.Fprintf(r.out, "rm '%s'\n", entry.path)
			}
		}
		return nil
	}
	if !has(r.p.Argv, "--cached") {
		for _, entry := range entries {
			abs := entryAbs(r.repo.Root, entry.path)
			if _, err := os.Lstat(abs); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
			result, err := r.p.filesystem.Operations.Delete.Delete(r.ctx, native.DeleteInput{Memory: r.p.filesystem.Revisions.Memory, Paths: []string{abs}, FilesOnly: true}, r.reviewContext(map[string]trackedEntry{entry.path: entry}, func() error {
				if has(r.p.Argv, "-f") {
					return nil
				}
				problem, err := r.removable(entry)
				if err != nil {
					return err
				}
				if problem != "" {
					return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "repository_changed_during_review", "path": entry.path}}
				}
				return nil
			}))
			r.effects = r.effects || len(result.Deleted) > 0
			if err != nil {
				return err
			}
		}
	}
	var changes []git.IndexEntry
	for _, entry := range entries {
		changes = append(changes, git.IndexEntry{Path: entry.path, Remove: true})
	}
	if err := r.stage(changes, expected, func() error {
		if !has(r.p.Argv, "--cached") || has(r.p.Argv, "-f") {
			return nil
		}
		for _, entry := range entries {
			problem, err := r.removable(entry)
			if err != nil {
				return err
			}
			if problem != "" {
				return &toolrejection.ToolReject{Code: "GIT_OPERATION_PRECONDITION", Data: map[string]any{"reason": "repository_changed_during_review", "path": entry.path}}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if r.status == 0 && !has(r.p.Argv, "-q") {
		for _, entry := range entries {
			fmt.Fprintf(r.out, "rm '%s'\n", entry.path)
		}
	}
	return nil
}

// --cached may discard the index when either the saved file or HEAD still
// contains its content. A normal removal must preserve both comparisons.
func (r *fileMutation) removable(entry trackedEntry) (string, error) {
	if entry.unmerged != "" {
		return "", nil
	}
	content, err := entryContent(r.ctx, r.p, r.repo.Root, entry)
	if err != nil {
		return "", err
	}
	abs := entryAbs(r.repo.Root, entry.path)
	info, err := os.Lstat(abs)
	missing := os.IsNotExist(err)
	if missing && !has(r.p.Argv, "--cached") {
		return "", nil
	}
	workMatches := false
	if err == nil {
		var saved []byte
		if info.Mode()&os.ModeSymlink != 0 {
			var target string
			target, err = os.Readlink(abs)
			saved = []byte(target)
		} else if info.Mode().IsRegular() {
			saved, err = r.p.filesystem.CapturePatchInput(r.ctx, r.tc, r.p.execution, abs)
		} else {
			return "", unsupportedFileForm(r.p, entry.path, "regular files or symbolic links")
		}
		if err != nil {
			return "", err
		}
		mode := "100644"
		if info.Mode()&os.ModeSymlink != 0 {
			mode = "120000"
		} else if info.Mode()&0111 != 0 {
			mode = "100755"
		}
		workMatches = bytes.Equal(saved, content) && entry.mode == mode
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if has(r.p.Argv, "--cached") && workMatches {
		return "", nil
	}
	head, err := git.ResolveOptionalObject(r.ctx, r.repo.Root, "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	if head == "" {
		if workMatches {
			return "staged", nil
		}
		return "both", nil
	}
	var output bytes.Buffer
	diagnostics, code, err := (git.CommandReader{}).ReadCommand(r.ctx, r.repo.Root, []string{"ls-tree", "-z", head, "--", ":(literal)" + entry.path}, nil, &output)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("read HEAD entry: %s", diagnostics)
	}
	meta, _, _ := strings.Cut(output.String(), "\t")
	fields := strings.Fields(meta)
	headMatches := len(fields) == 3 && fields[0] == entry.mode && fields[2] == entry.oid
	if has(r.p.Argv, "--cached") && headMatches {
		return "", nil
	}
	if headMatches && (workMatches || missing) {
		return "", nil
	}
	if !headMatches && !workMatches && !missing {
		return "both", nil
	}
	if !headMatches {
		return "staged", nil
	}
	return "local", nil
}

func (r *fileMutation) removeDiagnostic(problem string, names []string) {
	reason := "local modifications"
	suffix := "(use --cached to keep the file, or -f to force removal)"
	if problem == "staged" {
		reason = "changes staged in the index"
	}
	if problem == "both" {
		reason = "staged content different from both the\nfile and the HEAD"
		suffix = "(use -f to force removal)"
	}
	subject := "file has"
	if len(names) > 1 {
		subject = "files have"
	}
	r.fail(1, "error: the following %s %s:\n    %s\n%s", subject, reason, strings.Join(names, "\n    "), suffix)
}
