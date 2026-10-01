package episode

import (
	"context"
	"crypto/sha1" // #nosec G505 -- Repository blob IDs use SHA-1.
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/sourcescope"
	"github.com/lycaon/lycaon/internal/sourcesnapshot"
)

type Snapshot struct {
	ID               string   `json:"id"`
	RootsKey         string   `json:"roots_key"`
	Quality          string   `json:"quality"`
	Roots            []string `json:"roots"`
	MatchesDelivered bool     `json:"matches_delivered"`
}

type fileIdentity struct{ sha256, git string }

// ReadSources compares every captured generation with the retained delivery.
func (e *Evidence) ReadSources(ctx context.Context, capture, project string) error {
	delivered, err := deliveredFiles(ctx, project)
	if err != nil {
		return err
	}
	e.DeliveredPaths = make([]string, 0, len(delivered))
	for path := range delivered {
		e.DeliveredPaths = append(e.DeliveredPaths, path)
	}
	slices.Sort(e.DeliveredPaths)
	database, err := openCapture(ctx, capture)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	rows, err := database.QueryContext(ctx, `SELECT id,roots_key,capture_quality FROM source_snapshots ORDER BY id`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	e.Snapshots = map[string]Snapshot{}
	for rows.Next() {
		var snapshot Snapshot
		if err := rows.Scan(&snapshot.ID, &snapshot.RootsKey, &snapshot.Quality); err != nil {
			return err
		}
		e.Snapshots[snapshot.ID] = snapshot
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for id, snapshot := range e.Snapshots {
		if err := snapshot.compare(ctx, database, delivered); err != nil {
			return err
		}
		e.Snapshots[id] = snapshot
	}
	return nil
}

func (s *Snapshot) compare(ctx context.Context, database *sql.DB, delivered map[string]fileIdentity) error {
	s.MatchesDelivered = false
	queries := db.New(database)
	roots, err := queries.ListSourceSnapshotRoots(ctx, s.ID)
	if err != nil {
		return err
	}
	s.Roots = []string{}
	for _, root := range roots {
		s.Roots = append(s.Roots, root.RootPath)
	}
	if s.Quality != "exact" || len(roots) != 1 {
		return nil
	}
	boundaries, err := queries.ListSourceSnapshotBoundaries(ctx, s.ID)
	if err != nil {
		return err
	}
	// A bounded or incomplete capture cannot establish whole-delivery equality.
	for _, boundary := range boundaries {
		if (sourcesnapshot.Boundary{Reason: boundary.Reason}).Budgeted() {
			return nil
		}
	}
	entries, err := queries.ListSourceSnapshotRootEntries(ctx, db.ListSourceSnapshotRootEntriesParams{SnapshotID: s.ID, RootPath: roots[0].RootPath})
	if err != nil {
		return err
	}
	if len(entries) != len(delivered) {
		return nil
	}
	for _, entry := range entries {
		file, found := delivered[entry.Path]
		if !found {
			return nil
		}
		switch entry.Identity {
		case "hashed":
			if entry.Sha256 == "" || entry.Sha256 != file.sha256 {
				return nil
			}
		case "index":
			if entry.GitOid == "" || entry.GitOid != file.git {
				return nil
			}
		default:
			return nil
		}
	}
	s.MatchesDelivered = true
	return nil
}

func deliveredFiles(ctx context.Context, root string) (map[string]fileIdentity, error) {
	result := map[string]fileIdentity{}
	if root == "" {
		return result, nil
	}
	cfg, err := sourcescope.DefaultConfig()
	if err != nil {
		return nil, err
	}
	provider, err := sourcescope.NewProvider(cfg, nil)
	if err != nil {
		return nil, err
	}
	scope := provider.Capture(ctx, root)
	files, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() { _ = files.Close() }()
	err = fs.WalkDir(files.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return walkErr
		}
		if !scope.AdmitPath(rel, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("delivery contains a non-regular entry: %s", rel)
		}
		body, err := files.ReadFile(rel)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		git := sha1.New() // #nosec G401 -- Repository blob IDs use SHA-1.
		_, _ = fmt.Fprintf(git, "blob %d\x00", len(body))
		_, _ = git.Write(body)
		result[rel] = fileIdentity{sha256: hex.EncodeToString(digest[:]), git: hex.EncodeToString(git.Sum(nil))}
		return nil
	})
	return result, err
}
