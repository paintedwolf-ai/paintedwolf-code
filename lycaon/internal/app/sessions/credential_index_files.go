package sessions

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/protectedpath"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/secretharvest"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/textfile"
)

const credentialFileBytes = 16 << 20

type credentialBuildStats struct {
	buildID             string
	directoriesListed   int
	pathsClassified     int
	credentialFilesRead int
}

func (i *credentialIndex) refresh(ctx context.Context, root *credentialRoot, paths []string) (credentialBuildStats, error) {
	started := time.Now()
	stats := credentialBuildStats{buildID: uuid.NewString()}
	mode := "incremental"
	if len(paths) == 1 && paths[0] == "." {
		mode = "full"
	}
	capture := i.files.harvest.CaptureFiles(root.projectID)
	staged := *root
	staged.known = maps.Clone(root.known)
	var err error
	for _, path := range paths {
		if err = i.scan(ctx, &staged, path, capture, &stats); err != nil {
			break
		}
	}
	if err == nil {
		err = capture.Commit()
	}
	secretmatch.EmitPhaseRecord(ctx, secretmatch.ScreenPhaseRecord{
		CallID:              secretmatch.ScreenCallIDFrom(ctx),
		ProjectID:           root.projectID,
		BuildID:             stats.buildID,
		RootID:              root.id,
		Phase:               "credential_build",
		Mode:                mode,
		DurationMS:          time.Since(started).Milliseconds(),
		DirectoriesListed:   stats.directoriesListed,
		PathsClassified:     stats.pathsClassified,
		CredentialFilesRead: stats.credentialFilesRead,
		Failed:              err != nil,
	})
	if err != nil {
		return stats, err
	}
	root.known = staged.known
	return stats, nil
}

func (i *credentialIndex) scan(ctx context.Context, root *credentialRoot, path string, capture *secretharvest.FileCapture, stats *credentialBuildStats) error {
	path = filepath.ToSlash(filepath.Clean(path))
	if filepath.IsAbs(path) || path == ".." || strings.HasPrefix(path, "../") {
		return errors.New("credential index path leaves root")
	}
	if i.onScan != nil {
		i.onScan(root.path, path)
	}
	seen := map[string]bool{}
	var seeds []repochange.WatchDirectory
	flush := func() {
		if len(seeds) > 0 {
			repochange.SeedWatch(ctx, root.path, seeds)
			seeds = nil
		}
	}
	defer flush()
	err := filepath.WalkDir(filepath.Join(root.path, filepath.FromSlash(path)), func(abs string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root.path, abs)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			if entry.Name() == ".git" || repochange.IsPrivatePath(abs) {
				return filepath.SkipDir
			}
			if stats != nil {
				stats.directoriesListed++
			}
			seeds = append(seeds, repochange.WatchDirectory{Path: abs})
			if len(seeds) == 128 {
				flush()
			}
			return nil
		}
		if !entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if stats != nil {
			stats.pathsClassified++
		}
		if !protectedpath.IsCredentialFile(rel) || repochange.IsPrivatePath(abs) {
			return nil
		}
		if stats != nil {
			stats.credentialFilesRead++
		}
		if err := i.read(ctx, root, rel, capture); err != nil {
			return err
		}
		seen[rel], root.known[rel] = true, true
		return nil
	})
	if err != nil {
		return err
	}
	for known := range root.known {
		inScope := path == "." || known == path || strings.HasPrefix(known, path+"/")
		if inScope && !seen[known] {
			if err := capture.Observe(secretharvest.FileContent{ProjectID: root.projectID, RootID: root.id, Path: known}); err != nil {
				return err
			}
			delete(root.known, known)
		}
	}
	return nil
}

func (i *credentialIndex) read(ctx context.Context, root *credentialRoot, path string, capture *secretharvest.FileCapture) error {
	file, err := fseffect.OpenRead(fseffect.Location{Root: root.path, Rel: path})
	if errors.Is(err, fs.ErrNotExist) {
		return capture.Observe(secretharvest.FileContent{ProjectID: root.projectID, RootID: root.id, Path: path})
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	if info.Size() > credentialFileBytes {
		return fmt.Errorf("credential file %s exceeds the %d-byte parsing limit", path, credentialFileBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(file, credentialFileBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > credentialFileBytes {
		return fmt.Errorf("credential file %s grew beyond the parsing limit", path)
	}
	if text, _, err := textfile.Decode(raw, textfile.LimitsForRaw(credentialFileBytes)); err == nil {
		raw = []byte(text)
	}
	return i.observe(ctx, root, path, raw, capture)
}

func (i *credentialIndex) observe(ctx context.Context, root *credentialRoot, path string, raw []byte, capture *secretharvest.FileCapture) error {
	held := i.files.managedFingerprints(root.projectID)
	authored, err := i.files.queries.ListCredentialAuthoredValues(ctx, db.ListCredentialAuthoredValuesParams{ProjectID: root.projectID, RootID: root.id, Path: path})
	if err != nil {
		return err
	}
	for _, fingerprint := range authored {
		held[secretmatch.SecretFingerprint(fingerprint)] = true
	}
	return capture.Observe(secretharvest.FileContent{ProjectID: root.projectID, RootID: root.id, Path: path, Content: raw, Held: func(fp secretmatch.SecretFingerprint) bool { return held[fp] }})
}

func (i *credentialIndex) withdraw(root *credentialRoot) error {
	capture := i.files.harvest.CaptureFiles(root.projectID)
	for path := range root.known {
		if err := capture.Observe(secretharvest.FileContent{ProjectID: root.projectID, RootID: root.id, Path: path}); err != nil {
			return err
		}
	}
	return capture.Commit()
}
