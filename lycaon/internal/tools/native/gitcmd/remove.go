package gitcmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	if !has(r.p.Argv, "--cached") {
		for _, entry := range entries {
			// The shared delete removes regular files and directories only.
			if info, err := os.Lstat(entryAbs(r.repo.Root, entry.path)); err == nil && info.Mode()&os.ModeSymlink != 0 {
				return unsupportedFileForm(r.p, entry.path, "regular files; use --cached to untrack a symbolic link")
			}
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
		if err := r.pruneEmptyParents(entries); err != nil {
			return err
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
	content, err := r.cachedEntryContent(entry)
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
	head, err := r.resolveHeadCommit()
	if err != nil {
		return "", err
	}
	if head == "" {
		if workMatches {
			return "staged", nil
		}
		return "both", nil
	}
	headMatches, err := r.headEntryMatches(head, entry)
	if err != nil {
		return "", err
	}
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

func (r *fileMutation) cachedEntryContent(entry trackedEntry) ([]byte, error) {
	if r.blobs != nil {
		if content, ok := r.blobs[entry.oid]; ok {
			return content, nil
		}
	}
	content, err := entryContent(r.ctx, r.p, r.repo.Root, entry)
	if err != nil {
		return nil, err
	}
	if r.blobs == nil {
		r.blobs = map[string][]byte{}
	}
	r.blobs[entry.oid] = content
	return content, nil
}

func (r *fileMutation) resolveHeadCommit() (string, error) {
	snap := r.headMetadataSnapshot()
	if snap != "" && snap == r.headSnap {
		return r.headOID, nil
	}
	head, err := git.ResolveOptionalObject(r.ctx, r.repo.Root, "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	if snap != "" {
		r.headSnap = snap
		r.headOID = head
	}
	return head, nil
}

func (r *fileMutation) headMetadataSnapshot() string {
	headRaw, err := readBoundedGitFile(filepath.Join(r.repo.GitDir, "HEAD"), 64<<10)
	if err != nil {
		return ""
	}
	var b strings.Builder
	b.Write(headRaw)
	b.WriteByte(0)
	if ref, ok := strings.CutPrefix(strings.TrimSpace(string(headRaw)), "ref:"); ok {
		ref = strings.TrimSpace(ref)
		if loose, err := readBoundedGitFile(filepath.Join(r.repo.CommonDir, filepath.FromSlash(ref)), 64<<10); err == nil {
			b.Write(loose)
		} else if !os.IsNotExist(err) {
			return ""
		}
	}
	b.WriteByte(0)
	if packed, err := readBoundedGitFile(filepath.Join(r.repo.CommonDir, "packed-refs"), 1<<20); err == nil {
		b.Write(packed)
	} else if !os.IsNotExist(err) {
		return ""
	}
	return b.String()
}

func (r *fileMutation) headEntryMatches(head string, entry trackedEntry) (bool, error) {
	key := head + "\x00" + entry.path
	if r.headEntries != nil {
		if got, ok := r.headEntries[key]; ok {
			return got.mode == entry.mode && got.oid == entry.oid, nil
		}
	}
	var output bytes.Buffer
	diagnostics, code, err := (git.CommandReader{}).ReadCommand(r.ctx, r.repo.Root, []string{"ls-tree", "-z", head, "--", ":(literal)" + entry.path}, nil, &output)
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, fmt.Errorf("read HEAD entry: %s", diagnostics)
	}
	meta, _, _ := strings.Cut(output.String(), "\t")
	fields := strings.Fields(meta)
	var cached trackedEntry
	if len(fields) == 3 {
		cached = trackedEntry{path: entry.path, mode: fields[0], oid: fields[2]}
	}
	if r.headEntries == nil {
		r.headEntries = map[string]trackedEntry{}
	}
	r.headEntries[key] = cached
	return cached.mode == entry.mode && cached.oid == entry.oid, nil
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

// Git removes the directories a removal leaves empty, keeping the repository
// root and the working directory. Each one goes through the same recoverable
// delete as the files, deepest first so a parent is checked after its children.
func (r *fileMutation) pruneEmptyParents(entries []trackedEntry) error {
	root, cwd := filepath.Clean(r.repo.Root), filepath.Clean(r.dir)
	candidates := map[string]bool{}
	for _, entry := range entries {
		for dir := filepath.Dir(entryAbs(root, entry.path)); dir != root && dir != cwd && strings.HasPrefix(dir, root+string(filepath.Separator)); dir = filepath.Dir(dir) {
			candidates[dir] = true
		}
	}
	dirs := make([]string, 0, len(candidates))
	for dir := range candidates {
		dirs = append(dirs, dir)
	}
	sort.Slice(dirs, func(i, j int) bool {
		if len(dirs[i]) != len(dirs[j]) {
			return len(dirs[i]) > len(dirs[j])
		}
		return dirs[i] < dirs[j]
	})
	for _, dir := range dirs {
		children, err := os.ReadDir(dir)
		if err != nil || len(children) > 0 {
			continue
		}
		result, err := r.p.filesystem.Operations.Delete.Delete(r.ctx, native.DeleteInput{Memory: r.p.filesystem.Revisions.Memory, Paths: []string{dir}}, r.reviewContext(nil, nil))
		r.effects = r.effects || len(result.Deleted) > 0
		var reject *toolrejection.ToolReject
		if errors.As(err, &reject) && (reject.Code == "DELETE_NOT_EMPTY" || reject.Code == "DELETE_NOT_FOUND") {
			continue
		}
		if err != nil {
			return err
		}
	}
	return nil
}
