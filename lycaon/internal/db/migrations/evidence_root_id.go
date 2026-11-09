package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
)

//go:embed evidence_root_id.sql
var evidenceRootIDSQL []byte

// EvidenceRootID keys evidence_records paths by their owning workspace root:
// it adds the durable root_id column, backfills it with the session-folder
// rule (session workspace root, falling back to the project primary root,
// with @label prefixes resolving to the named root), rewrites labeled paths
// to root-scope-relative paths, and syncs the evidence_index projection.
func EvidenceRootID() Part {
	return Part{
		Source:   evidenceRootIDSQL,
		Apply:    migrateEvidenceRootID,
		Validate: validateEvidenceRootID,
		Writes:   evidenceRootIDWrites,
	}
}

// evidenceRoot is the frozen per-root fact the backfill resolves against.
type evidenceRoot struct {
	id      string
	path    string
	label   string
	primary bool
}

type evidenceProjectRoots struct {
	roots []evidenceRoot
	byID  map[string]evidenceRoot
}

func (p evidenceProjectRoots) activeRoot(workspaceRootID string) (evidenceRoot, bool) {
	want := strings.TrimSpace(workspaceRootID)
	if want != "" {
		if root, ok := p.byID[want]; ok {
			return root, true
		}
	}
	for _, root := range p.roots {
		if root.primary {
			return root, true
		}
	}
	return evidenceRoot{}, false
}

// rootByLabel freezes the shipped case-insensitive label match.
func (p evidenceProjectRoots) rootByLabel(label string) (evidenceRoot, bool) {
	for _, root := range p.roots {
		if strings.EqualFold(root.label, label) {
			return root, true
		}
	}
	return evidenceRoot{}, false
}

// rootForAbsolute freezes the shipped most-specific containing-root match.
func (p evidenceProjectRoots) rootForAbsolute(abs string) (evidenceRoot, string, bool) {
	var match evidenceRoot
	var rel string
	found := false
	for _, root := range p.roots {
		rootAbs, err := filepath.Abs(root.path)
		if err != nil {
			continue
		}
		rootAbs = filepath.Clean(rootAbs)
		relPath, err := filepath.Rel(rootAbs, abs)
		if err != nil {
			continue
		}
		if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
			continue
		}
		if !found || len(relPath) < len(rel) {
			match = root
			rel = filepath.ToSlash(relPath)
			found = true
		}
	}
	return match, rel, found
}

func migrateEvidenceRootID(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, string(evidenceRootIDSQL)); err != nil {
		return err
	}
	projects, err := loadEvidenceProjectRoots(ctx, tx)
	if err != nil {
		return err
	}
	sessions, err := loadEvidenceSessions(ctx, tx)
	if err != nil {
		return err
	}
	for _, sess := range sessions {
		proj := projects[sess.projectID]
		if len(proj.roots) == 0 {
			continue
		}
		active, hasActive := proj.activeRoot(sess.workspaceRootID)
		if err := backfillEvidenceSessionRootIDs(ctx, tx, sess, proj, active, hasActive); err != nil {
			return err
		}
	}
	return nil
}

type evidenceSessionRoots struct {
	id              string
	projectID       string
	workspaceRootID string
}

func loadEvidenceSessions(ctx context.Context, tx *sql.Tx) ([]evidenceSessionRoots, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, project_id, COALESCE(workspace_root_id, '') FROM sessions ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	var out []evidenceSessionRoots
	for rows.Next() {
		var sess evidenceSessionRoots
		if err := rows.Scan(&sess.id, &sess.projectID, &sess.workspaceRootID); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func loadEvidenceProjectRoots(ctx context.Context, tx *sql.Tx) (map[string]evidenceProjectRoots, error) {
	rows, err := tx.QueryContext(ctx, `SELECT project_id, id, path, COALESCE(label, ''), is_primary FROM project_roots ORDER BY project_id, id`)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	out := map[string]evidenceProjectRoots{}
	for rows.Next() {
		var projectID string
		var root evidenceRoot
		var primary int64
		if err := rows.Scan(&projectID, &root.id, &root.path, &root.label, &primary); err != nil {
			return nil, err
		}
		root.primary = primary != 0
		proj := out[projectID]
		proj.roots = append(proj.roots, root)
		if proj.byID == nil {
			proj.byID = map[string]evidenceRoot{}
		}
		proj.byID[root.id] = root
		out[projectID] = proj
	}
	return out, rows.Err()
}

// evidenceRootIDUpdate is one resolved record rewrite.
type evidenceRootIDUpdate struct {
	rootID      string
	path        string
	pathChanged bool
}

// resolveEvidenceRootID freezes the shipped session-folder identity rule for
// stored paths: @label/ tokens resolve to the named root and rewrite to the
// root-scope-relative path, bare tokens pair with the session's active root,
// and absolute paths pair with their most specific containing root. Paths
// that resolve under no attached root stay legacy (empty root ID).
func resolveEvidenceRootID(proj evidenceProjectRoots, active evidenceRoot, hasActive bool, path string) evidenceRootIDUpdate {
	path = strings.TrimSpace(path)
	if path == "" {
		return evidenceRootIDUpdate{}
	}
	if strings.HasPrefix(path, "@") {
		rest := strings.TrimPrefix(path, "@")
		label := rest
		rel := ""
		if slash := strings.Index(rest, "/"); slash >= 0 {
			label = rest[:slash]
			rel = strings.TrimPrefix(rest[slash:], "/")
		}
		root, ok := proj.rootByLabel(label)
		if !ok {
			return evidenceRootIDUpdate{}
		}
		if rel == "" {
			rel = "."
		}
		return evidenceRootIDUpdate{rootID: root.id, path: rel, pathChanged: rel != path}
	}
	if filepath.IsAbs(path) {
		clean := filepath.Clean(path)
		root, rel, ok := proj.rootForAbsolute(clean)
		if !ok {
			return evidenceRootIDUpdate{}
		}
		return evidenceRootIDUpdate{rootID: root.id, path: rel, pathChanged: rel != path}
	}
	if !hasActive {
		return evidenceRootIDUpdate{}
	}
	return evidenceRootIDUpdate{rootID: active.id, path: path}
}

func backfillEvidenceSessionRootIDs(ctx context.Context, tx *sql.Tx, sess evidenceSessionRoots, proj evidenceProjectRoots, active evidenceRoot, hasActive bool) error {
	after := ""
	for {
		rows, err := tx.QueryContext(ctx, `SELECT handle, path FROM evidence_records
WHERE session_id = ? AND root_id = '' AND path != '' AND handle > ?
ORDER BY handle LIMIT 64`, sess.id, after)
		if err != nil {
			return err
		}
		type pending struct {
			handle string
			path   string
		}
		var batch []pending
		for rows.Next() {
			var p pending
			if err := rows.Scan(&p.handle, &p.path); err != nil {
				_ = rows.Close()
				return err
			}
			batch = append(batch, p)
		}
		closeErr := rows.Close()
		if closeErr != nil {
			return closeErr
		}
		if len(batch) == 0 {
			return nil
		}
		for _, p := range batch {
			after = p.handle
			update := resolveEvidenceRootID(proj, active, hasActive, p.path)
			if update.rootID == "" && !update.pathChanged {
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE evidence_records SET root_id = ?, path = ? WHERE session_id = ? AND handle = ?`,
				update.rootID, update.path, sess.id, p.handle); err != nil {
				return err
			}
			if update.pathChanged {
				if _, err := tx.ExecContext(ctx, `UPDATE evidence_index SET path = ? WHERE session_id = ? AND handle = ? AND path = ?`,
					update.path, sess.id, p.handle, p.path); err != nil {
					return err
				}
			}
		}
	}
}

// validateEvidenceRootID requires rooted records to stay root-scope-relative;
// unresolvable labels keep their legacy spelling with an empty root ID.
func validateEvidenceRootID(ctx context.Context, tx *sql.Tx) error {
	var rooted int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM evidence_records
WHERE root_id != '' AND (path = '' OR path LIKE '@%' OR path LIKE '../%' OR path = '..')`).Scan(&rooted); err != nil {
		return err
	}
	if rooted != 0 {
		return fmt.Errorf("%d rooted evidence records kept a non-scope-relative path", rooted)
	}
	return nil
}

// Budget the record rewrites, their index sync, and the ALTER's schema page
// work with the rows' actual encoded text lengths.
const (
	evidenceRootIDDropDivisor = 256
	evidenceRootIDFixedBytes  = 1 << 20
)

func evidenceRootIDWrites(ctx context.Context, store DBTX, storeBytes uint64) (uint64, error) {
	var records, recordText, indexRows, indexText int64
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(
 length(CAST(path AS BLOB)) + length(CAST(handle AS BLOB)) + length(CAST(session_id AS BLOB))), 0)
 FROM evidence_records`).Scan(&records, &recordText); err != nil {
		return 0, err
	}
	if err := store.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(length(CAST(path AS BLOB)) + length(CAST(session_id AS BLOB))), 0) FROM evidence_index`).Scan(&indexRows, &indexText); err != nil {
		return 0, err
	}
	total := storeBytes/evidenceRootIDDropDivisor + evidenceRootIDFixedBytes
	for _, part := range []struct {
		count int64
		scale uint64
	}{
		{records, 8}, {recordText, 4}, {indexRows, 8}, {indexText, 4},
	} {
		if part.count < 0 || uint64(part.count) > (math.MaxUint64-total)/part.scale {
			return 0, fmt.Errorf("evidence root migration write estimate overflows")
		}
		total += uint64(part.count) * part.scale
	}
	return total, nil
}
