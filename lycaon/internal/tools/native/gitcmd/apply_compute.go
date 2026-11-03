package gitcmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/mutation"
	"github.com/lycaon/lycaon/internal/tools/native/patchapply"
)

// compute binds, captures and applies every section before anything lands.
// Any section that cannot apply sets status 1 and nothing changes; a --3way
// conflict still applies and is reported after publication.
func (r *applyRun) compute(sections []*applySection) error {
	failed := false
	for _, s := range sections {
		if has(r.p.Argv, "-v") {
			writeLine(r.stderr, "Checking patch "+s.name+"...")
		}
		if err := r.bind(s); err != nil {
			return err
		}
		problem, err := r.capture(s)
		if err != nil {
			return err
		}
		if problem != "" {
			writeLine(r.stderr, "error: "+s.name+": "+problem)
			failed = true
			continue
		}
		if r.threeWay {
			merged, err := r.merge(s)
			if err != nil {
				return err
			}
			if merged {
				continue
			}
		}
		if !r.direct(s) {
			failed = true
		}
	}
	if failed {
		r.status = 1
	}
	return nil
}

// bind resolves a section's paths against the command directory; index
// forms also name them relative to the repository.
func (r *applyRun) bind(s *applySection) error {
	for _, side := range []struct {
		name     string
		abs, rel *string
	}{{s.oldName, &s.target.OldAbs, &s.oldRel}, {s.newName, &s.target.NewAbs, &s.newRel}} {
		if side.name == "" {
			continue
		}
		bound, err := r.p.paths.Bind(r.ctx, r.join(side.name), true)
		if err != nil {
			return err
		}
		*side.abs = bound.Resolved.Abs
		if !r.cached && !r.index {
			continue
		}
		rel, err := filepath.Rel(r.repo.Root, bound.Resolved.Abs)
		if err != nil || sandbox.HasParentTraversal(rel) {
			return &toolrejection.ToolReject{Code: "GIT_PATH_DENIED", Data: map[string]any{"path": side.name, "tool": r.p.command.spec.Name}}
		}
		*side.rel = filepath.ToSlash(rel)
	}
	return nil
}

// capture pins what the section reads: the work tree file through the
// shared patch capture, the index entry, or both for --index. It returns
// Git's reason when the section cannot apply to what it found.
func (r *applyRun) capture(s *applySection) (string, error) {
	file := s.file
	if !r.cached {
		target, err := r.p.filesystem.CapturePatchTarget(r.ctx, r.tc, r.p.execution, s.target.OldAbs, s.target.NewAbs)
		if err != nil && !errors.Is(err, mutation.ErrPatchTargetMissing) {
			return "", err
		}
		s.target, s.before = target, target.Before
		switch {
		case !file.IsNew && !target.Exists:
			return "No such file or directory", nil
		case (file.IsNew || file.IsRename || file.IsCopy) && target.NewExists:
			return "already exists in working directory", nil
		}
	}
	if !r.cached && !r.index {
		return "", nil
	}
	rel := s.oldRel
	if file.IsNew {
		rel = s.newRel
	}
	entry, err := r.indexEntry(rel)
	if err != nil {
		return "", err
	}
	s.indexMod = entry.mode
	switch {
	case file.IsNew && entry.oid != "":
		return "already exists in index", nil
	case file.IsNew:
		return "", nil
	case entry.oid == "":
		return "does not exist in index", nil
	}
	maximum := r.p.owners.limit("stream_bytes")
	content, ok, err := r.p.owners.Git.BlobContent(r.ctx, r.repo.Root, entry.oid, int(maximum))
	if err != nil {
		return "", err
	}
	if !ok {
		observed, err := r.p.owners.Git.BlobSize(r.ctx, r.repo.Root, entry.oid)
		if err != nil {
			return "", err
		}
		return "", &toolrejection.ToolReject{Code: "COMMAND_LIMIT_EXCEEDED", Data: map[string]any{"limit_id": "stream_bytes", "resource_unit": "bytes", "maximum": maximum, "observed": observed, "path": rel}}
	}
	if r.cached {
		s.before = content
		return "", nil
	}
	if !bytes.Equal(content, s.before) {
		return "does not match index", nil
	}
	return "", nil
}

type indexRecord struct{ mode, oid string }

// indexEntry reads one path's stage-0 index entry through the read door.
func (r *applyRun) indexEntry(rel string) (indexRecord, error) {
	var out bytes.Buffer
	diagnostics, code, err := (git.CommandReader{}).ReadCommand(r.ctx, r.repo.Root, []string{"ls-files", "--stage", "-z", "--", ":(literal)" + rel}, nil, &out)
	if err != nil {
		return indexRecord{}, err
	}
	if code != 0 {
		return indexRecord{}, fmt.Errorf("read index entry: %s", bytes.TrimSpace(diagnostics))
	}
	for _, record := range strings.Split(out.String(), "\x00") {
		meta, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if ok && path == rel && len(fields) == 3 && fields[2] == "0" {
			return indexRecord{mode: fields[0], oid: fields[1]}, nil
		}
	}
	return indexRecord{}, nil
}

// direct applies a section exactly (offsets allowed, no fuzz) and reports
// Git's reason when it does not apply.
func (r *applyRun) direct(s *applySection) bool {
	file := s.file
	text, err := file.Apply(s.before, patchapply.Options{})
	switch {
	case err != nil:
		writeLine(r.stderr, "error: binary patch does not apply to '"+s.name+"'")
	case text.Failed > 0:
		r.failures = append(r.failures, text.Failures(s.name)...)
		for _, hunk := range text.Hunks {
			if hunk.Applied {
				continue
			}
			frag := file.TextFragments[hunk.Number-1]
			if has(r.p.Argv, "-v") {
				var expected strings.Builder
				for _, line := range frag.Lines {
					if line.Old() {
						expected.WriteString(line.Line)
					}
				}
				_, _ = io.WriteString(r.stderr, "error: while searching for:\n"+expected.String()+"\n")
			}
			writeLine(r.stderr, fmt.Sprintf("error: patch failed: %s:%d", s.name, frag.OldPosition))
			break
		}
	case file.IsDelete && len(text.Content) > 0:
		writeLine(r.stderr, "error: removal patch leaves file contents")
	default:
		renamed := s.oldName != "" && s.newName != "" && s.oldName != s.newName && !file.IsCopy
		s.change = mutation.PatchResult{After: text.Content, Write: !file.IsDelete, RemoveOld: file.IsDelete || renamed, Mode: file.ModeChange()}
		return true
	}
	writeLine(r.stderr, "error: "+s.name+": patch does not apply")
	return false
}

// merge applies a section three ways when the patch names a preimage blob
// the repository holds: the patch applies to that blob, and Git's merge-file
// merges the result into the current content. It reports false when the
// section falls back to direct application.
func (r *applyRun) merge(s *applySection) (bool, error) {
	file := s.file
	if file.IsNew || file.IsDelete || file.IsBinary {
		writeLine(r.stderr, "Falling back to direct application...")
		return false, nil
	}
	var base []byte
	ok := false
	if prefix := strings.Trim(file.OldOIDPrefix, "0"); prefix != "" {
		var err error
		base, ok, err = r.p.owners.Git.BlobContent(r.ctx, r.repo.Root, file.OldOIDPrefix, int(r.p.owners.limit("stream_bytes")))
		if err != nil {
			return false, err
		}
	}
	if !ok {
		writeLine(r.stderr, "error: repository lacks the necessary blob to perform 3-way merge.")
		writeLine(r.stderr, "Falling back to direct application...")
		return false, nil
	}
	theirs := patchapply.ApplyText(base, file.TextFragments, patchapply.Options{})
	if theirs.Failed > 0 {
		writeLine(r.stderr, "Falling back to direct application...")
		return false, nil
	}
	merged, conflicts, err := git.MergeFile(r.ctx, s.before, base, theirs.Content, [3]string{"ours", "base", "theirs"})
	if err != nil {
		return false, err
	}
	s.change = mutation.PatchResult{After: merged, Write: true, Mode: file.ModeChange()}
	if conflicts > 0 {
		s.stages = &[3][]byte{base, s.before, theirs.Content}
		writeLine(r.stderr, "Applied patch to '"+s.name+"' with conflicts.")
	} else {
		writeLine(r.stderr, "Applied patch to '"+s.name+"' cleanly.")
	}
	return true, nil
}

// publish lands the work tree through the shared patch publication, then
// makes the index changes in one reviewed Git operation.
func (r *applyRun) publish(sections []*applySection) error {
	if !r.cached {
		for _, s := range sections {
			effect, err := r.p.filesystem.PublishPatchTarget(r.ctx, r.tc, s.target, s.change)
			if effect.Wrote || effect.Removed {
				r.effects++
			}
			if err != nil {
				return err
			}
			if has(r.p.Argv, "-v") && s.stages == nil {
				writeLine(r.stderr, "Applied patch "+s.name+" cleanly.")
			}
		}
	}
	if r.cached || r.index {
		if err := r.stage(sections); err != nil {
			return err
		}
	}
	for _, s := range sections {
		if s.stages != nil {
			writeLine(r.stderr, "U "+s.name)
			r.status = 1
		}
	}
	return nil
}

// stage records the sections' results in the index: new content at stage 0,
// removals, and a conflict's three stages. Its review and session staging
// follow git add.
func (r *applyRun) stage(sections []*applySection) error {
	var entries []git.IndexEntry
	for _, s := range sections {
		if s.change.RemoveOld && s.oldRel != "" {
			entries = append(entries, git.IndexEntry{Path: s.oldRel, Remove: true})
		}
		if !s.change.Write {
			continue
		}
		entries = append(entries, git.IndexEntry{Path: s.newRel, Mode: indexMode(s), Content: s.change.After, Stages: s.stages})
	}
	if len(entries) == 0 {
		return nil
	}
	paths := make([]string, len(entries))
	for i, entry := range entries {
		paths[i] = entry.Path
	}
	tc := withRepoRoot(r.tc, r.repo.Root)
	result, err := native.GitMutations{Git: r.p.owners.Git, Boundary: r.p.owners.Boundary}.Operate(r.ctx, tc, git.OperationRequest{Kind: "stage", Entries: entries, Paths: paths})
	if result.Attempted {
		r.effects++
	}
	if err != nil {
		return err
	}
	if result.Status == "failed" {
		writeLine(r.stderr, strings.TrimSpace(result.Diagnostics))
		r.status = 1
		return nil
	}
	recordStaged(r.p.owners, tc.ChatSessionID(), r.repo.Root, paths)
	return nil
}

// indexMode is the section's new mode, else the mode the index held.
func indexMode(s *applySection) string {
	if s.file.NewMode != 0 {
		return strconv.FormatUint(uint64(s.file.NewMode), 8)
	}
	if s.indexMod != "" {
		return s.indexMod
	}
	return "100644"
}
