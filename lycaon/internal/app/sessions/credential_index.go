package sessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/keylock"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/workscope"
)

type credentialRoot struct {
	projectID, id, path string
	known               map[string]bool
	epoch               repochange.Epoch
	ready               bool
	validated           time.Time
}

type credentialIndex struct {
	files  *credentialFiles
	access keylock.Group
	roots  map[string]*credentialRoot
	work   workscope.Group
	stop   func()
	onScan func(rootPath, path string)
}

func newCredentialIndex(files *credentialFiles) *credentialIndex {
	return &credentialIndex{files: files, roots: map[string]*credentialRoot{}}
}

// Startup restores durable evidence and subscribes to changes. Discovery belongs
// to EnsureProject's provider/tool admission barrier, not the host's boot path.
func (c *credentialFiles) startCredentialIndex() {
	if c.index == nil || c.harvest == nil || c.fp == nil {
		return
	}
	i := c.index
	i.stop = repochange.RegisterObserver(func(ctx context.Context, event repochange.Event) {
		if err := i.changed(ctx, event); err != nil && !errors.Is(err, context.Canceled) {
			slog.WarnContext(ctx, "credential-file evidence refresh failed", "root", event.ProjectDir, "error", err)
		}
	})
}

func (c *credentialFiles) closeCredentialIndex(ctx context.Context) error {
	if c == nil || c.index == nil {
		return nil
	}
	c.index.work.Stop()
	if c.index.stop != nil {
		c.index.stop()
	}
	return c.index.work.Wait(ctx)
}

// EnsureProject is the admission barrier for new roots and missed watcher windows.
func (c *credentialFiles) EnsureProject(ctx context.Context, projectID string) error {
	if c == nil || c.index == nil || c.harvest == nil || c.fp == nil || projectID == "" {
		return nil
	}
	i := c.index
	ctx, finish, err := i.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	rows, err := c.queries.ListProjectRoots(ctx, projectID)
	if err != nil {
		return err
	}
	attached := map[string]bool{}
	var localTrace secretmatch.EvidenceTrace
	emitReadiness := func(failed bool) {
		snap := localTrace.Snapshot()
		secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
			CallID:              secretmatch.ScreenCallIDFrom(ctx),
			ProjectID:           projectID,
			Phase:               "root_readiness_wait",
			DurationMS:          snap.ReadinessWaitMS,
			RootsChecked:        snap.RootsChecked,
			BuildsJoined:        snap.BuildsJoined,
			BuildIDs:            snap.BuildIDs,
			DirectoriesListed:   snap.DirectoriesListed,
			PathsClassified:     snap.PathsClassified,
			CredentialFilesRead: snap.CredentialFilesRead,
			Failed:              failed,
		})
	}
	for _, row := range rows {
		// Agents cannot write in an editor-only root, so it needs no index.
		if admitted, _ := (project.Root{Path: row.Path}).Admission(); !admitted {
			continue
		}
		key := row.ProjectID + "\x00" + row.ID
		attached[key] = true
		root := i.roots[key]
		if root == nil || root.path != row.Path {
			if root != nil {
				if err := i.withdraw(root); err != nil {
					emitReadiness(true)
					return err
				}
			}
			root = rootFromRow(row)
			i.roots[key] = root
		}
		repochange.EnsureRoot(ctx, root.path)
		coverageStale := root.coverageExpired(repochange.Coverage(root.path), time.Now())
		if !root.ready || coverageStale || !repochange.EpochCurrent(root.path, root.epoch) {
			epoch := repochange.CurrentEpoch(root.path)
			started := time.Now()
			stats, err := i.refresh(ctx, root, []string{"."})
			waitMS := time.Since(started).Milliseconds()
			localTrace.NoteRootWait(waitMS, stats.buildID, stats.directoriesListed, stats.pathsClassified, stats.credentialFilesRead)
			if trace := secretmatch.EvidenceTraceFrom(ctx); trace != nil {
				trace.NoteRootWait(waitMS, stats.buildID, stats.directoriesListed, stats.pathsClassified, stats.credentialFilesRead)
			}
			if err != nil {
				root.ready = false
				emitReadiness(true)
				return fmt.Errorf("credential-file index: %w", err)
			}
			root.ready, root.epoch, root.validated = true, epoch, time.Now()
		} else {
			localTrace.NoteRootWait(0, "", 0, 0, 0)
			if trace := secretmatch.EvidenceTraceFrom(ctx); trace != nil {
				trace.NoteRootWait(0, "", 0, 0, 0)
			}
		}
	}
	for key, root := range i.roots {
		if root.projectID == projectID && !attached[key] {
			if err := i.withdraw(root); err != nil {
				emitReadiness(true)
				return err
			}
			delete(i.roots, key)
		}
	}
	emitReadiness(false)
	return nil
}

func rootFromRow(row db.ProjectRoots) *credentialRoot {
	return &credentialRoot{projectID: row.ProjectID, id: row.ID, path: row.Path, known: map[string]bool{}}
}

func (i *credentialIndex) changed(ctx context.Context, event repochange.Event) error {
	ctx, finish, err := i.begin(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if event.Kind == repochange.IndexChanged {
		for _, root := range i.roots {
			if sameCredentialRootDir(root.path, event.ProjectDir) && root.ready && event.ContiguousAfter(root.epoch) {
				root.epoch = repochange.Epoch{BootID: event.EpochBootID, Value: event.WorktreeEpoch}
			}
		}
		return nil
	}
	for _, root := range i.roots {
		if !sameCredentialRootDir(root.path, event.ProjectDir) {
			continue
		}
		epoch := repochange.Epoch{BootID: event.EpochBootID, Value: event.WorktreeEpoch}
		contiguous := root.ready && event.ContiguousAfter(root.epoch)
		paths := event.Paths
		if !contiguous || event.Kind == repochange.HeadMoved || len(paths) == 0 {
			paths = []string{"."}
			epoch = repochange.CurrentEpoch(root.path)
		}
		if _, err := i.refresh(ctx, root, paths); err != nil {
			root.ready = false
			return err
		}
		root.ready, root.epoch = true, epoch
		if len(paths) == 1 && paths[0] == "." {
			root.validated = time.Now()
		}
	}
	return nil
}

func (r *credentialRoot) coverageExpired(coverage repochange.WatchCoverage, now time.Time) bool {
	return !coverage.Complete && now.Sub(r.validated) >= repochange.CoverageRevalidationInterval
}

func sameCredentialRootDir(left, right string) bool {
	if filepath.Clean(left) == filepath.Clean(right) {
		return true
	}
	lCanon, rCanon := gitrepo.CanonicalDir(left), gitrepo.CanonicalDir(right)
	return lCanon != "" && lCanon == rCanon
}
