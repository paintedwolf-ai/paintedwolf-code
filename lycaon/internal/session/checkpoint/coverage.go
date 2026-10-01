package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/sourceblob"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/sourcerewind"
)

// CheckCoverage blocks a rewind when checkpoint metadata shows host mutations
// missing source authorship.
func CheckCoverage(ctx context.Context, repository store.CheckpointRepository, p *project.Project, rootSession string, anchors []string, op *sourcerewind.Operation) error {
	issues := []sourceledger.RewindIssue{}
	for _, anchor := range anchors {
		raw, err := repository.ReadCheckpointForRewind(ctx, rootSession, anchor)
		if errors.Is(err, store.ErrCheckpointMissing) {
			continue
		}
		if err != nil {
			return err
		}
		var man Manifest
		if err := json.Unmarshal([]byte(raw), &man); err != nil {
			return err
		}
		if man.Version != ManifestVersion || man.SessionID != rootSession || man.AnchorMessageID != anchor {
			return fmt.Errorf("invalid rewind coverage identity")
		}
		rootID := ""
		for _, root := range p.Roots {
			if filepath.Clean(root.Path) == filepath.Clean(man.ProjectDir) {
				rootID = root.ID
				break
			}
		}
		issue := func(path, code string) {
			issues = append(issues, sourceledger.RewindIssue{RootID: rootID, Path: path, Code: code})
		}
		if man.BlueprintPath != "" {
			issue(man.BlueprintPath, "workflow_binding_changed")
		}
		if man.Truncated {
			issue("", "capture_incomplete")
		}
		for _, path := range man.Skipped {
			if !rewindCoversPath(op, man.ProjectDir, path) {
				issue(path, "unrecorded_change")
			}
		}
		for path, entry := range man.Paths {
			if rootID == "" {
				issue(path, "workspace_unavailable")
				continue
			}
			if rewindCoversPath(op, man.ProjectDir, path) {
				// The pre-image retains permissions for a deleted file.
				for i := range op.Files {
					f := &op.Files[i]
					if f.Expected.State == "absent" && f.RootPath == man.ProjectDir && f.Target.Path == path && f.Target.SHA256 == entry.SHA256 {
						f.Mode = entry.Mode
					}
				}
				continue
			}
			if !checkpointPathUnchanged(man.ProjectDir, path, entry) {
				issue(path, "unrecorded_change")
			}
		}
	}
	if len(issues) > 0 {
		return &sourceledger.RewindBlockedError{Issues: issues}
	}
	return nil
}

func rewindCoversPath(op *sourcerewind.Operation, root, path string) bool {
	for _, f := range op.Files {
		if filepath.Clean(f.RootPath) == filepath.Clean(root) && (f.Expected.Path == path || f.Target.Path == path) {
			return true
		}
	}
	return false
}

func checkpointPathUnchanged(root, path string, entry PathEntry) bool {
	if !filepath.IsLocal(path) || path == "." {
		return false
	}
	dir, err := os.OpenRoot(root)
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	info, err := dir.Lstat(path)
	if entry.Op == OpAbsent {
		return errors.Is(err, os.ErrNotExist)
	}
	if err != nil || !info.Mode().IsRegular() || entry.Op != OpSnapshot {
		return false
	}
	file, err := dir.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, entry.Size+1))
	return err == nil && int64(len(raw)) == entry.Size && sourceblob.ContentSHA(raw) == entry.SHA256
}
