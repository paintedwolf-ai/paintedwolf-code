package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/editordoc"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

// presenceDrafts compares a completed worker's branch files with the primary
// text the person sees, so a ready draft shows the lines it will change. The
// files are the ones promotion would land: the branch tree's changes from its
// baseline, including files its commands changed.
type presenceDrafts struct {
	tasks     func(jobID string) (*api.WorkerTask, bool)
	projects  *project.SQLRegistry
	documents *editordoc.Service
}

func (d presenceDrafts) ReadyFiles(ctx context.Context, jobID string) ([]agentpresence.DraftFile, error) {
	task, ok := d.tasks(jobID)
	if !ok || task == nil || d.projects == nil {
		return nil, nil
	}
	p, err := d.projects.Get(ctx, task.ProjectID)
	if err != nil {
		return nil, err
	}
	roots := project.RootRefsFrom(p)
	paths, err := session.InspectOverlayChanges(ctx, task, roots)
	if err != nil {
		return nil, err
	}
	promote := worker.PromoteRootsForTask(task, roots)
	primary, err := projectroot.PrimaryRoot(roots)
	if err != nil {
		return nil, err
	}
	files := make([]agentpresence.DraftFile, 0, len(paths))
	for _, changed := range paths {
		abs, root, err := projectroot.ResolveAbs(roots, task.WorkspaceRootID, changed)
		if err != nil {
			continue
		}
		rel := filepath.ToSlash(projectroot.ScopeRel(root, abs))
		if rel == "" || rel == "." {
			continue
		}
		file := agentpresence.DraftFile{Target: agentpresence.Target{RootID: root.ID, Path: rel}}
		if d.changedLines(ctx, task, promote, projectroot.Qualify(primary, root, abs), &file) {
			files = append(files, file)
		}
	}
	return files, nil
}

// changedLines fills a file's changed lines when both sides are text. A
// deleted, binary, or unreadable file leaves the draft whole-file. It reports
// false for a branch file whose bytes still match the primary file, which
// promotion would not change.
func (d presenceDrafts) changedLines(ctx context.Context, task *api.WorkerTask, promote worker.PromoteRoots, qualified string, file *agentpresence.DraftFile) bool {
	primaryBytes, branchBytes, branchPresent, err := promote.ReadPairBytes(task, qualified)
	if err == nil && branchPresent && primaryBytes != nil && bytes.Equal(primaryBytes, branchBytes) {
		return false
	}
	if err != nil || !branchPresent || !utf8.Valid(branchBytes) || !utf8.Valid(primaryBytes) {
		return true
	}
	before := string(primaryBytes)
	if text, open, err := d.documents.PathText(ctx, task.ProjectID, file.RootID, file.Path); err == nil && open {
		before = text
	}
	changes, err := documentcore.ChangedLines(before, strings.ReplaceAll(string(branchBytes), "\r\n", "\n"))
	if err != nil {
		return true
	}
	insertions, deletions := 0, 0
	for _, change := range changes {
		insertions += change.Added
		deletions += change.Removed
		span := agentpresence.Span{StartLine: change.StartLine, EndLine: change.EndLine}
		if change.Insertion {
			zero := 0
			span.StartCharacter, span.EndCharacter = &zero, &zero
		}
		file.Spans = append(file.Spans, span)
	}
	file.Insertions, file.Deletions = &insertions, &deletions
	return true
}
