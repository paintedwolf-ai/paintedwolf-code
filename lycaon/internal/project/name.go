package project

import (
	"context"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/llm"
	"github.com/lycaon/lycaon/pkg/api"
)

const maxProjectNameRunes = 48

// NameFromFolderPath derives a display name from a folder path basename.
func NameFromFolderPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return trimProjectName(filepath.Base(path))
}

// DefaultNameForCreate selects the initial project name.
func DefaultNameForCreate(params CreateParams) string {
	if trimmed := strings.TrimSpace(params.Name); trimmed != "" {
		return trimmed
	}
	if params.Draft {
		return ""
	}
	if len(params.Roots) == 0 {
		return ""
	}
	primary := params.Roots[0]
	for _, root := range params.Roots[1:] {
		if root.IsPrimary != nil && *root.IsPrimary {
			primary = root
		}
	}
	return NameFromFolderPath(primary.Path)
}

// NameProject derives a short project title from the first user message.
func NameProject(ctx context.Context, namer llm.UtilityNamer, firstUserMessage string) string {
	text := strings.TrimSpace(firstUserMessage)
	if text == "" {
		return ""
	}
	if llm.MockOnlyFromEnv() {
		return limitProjectNameWords(text)
	}
	if namer == nil {
		return ""
	}
	system, err := guidance.RenderCatalog(ctx, guidance.UtilityProjectNameSystemRef, nil)
	if err != nil {
		return ""
	}
	raw, err := namer.Name(ctx, system, text)
	if err != nil || strings.TrimSpace(raw) == "" {
		return ""
	}
	name := limitProjectNameWords(raw)
	if name == "" {
		return ""
	}
	return name
}

func limitProjectNameWords(s string) string {
	fields := strings.Fields(trimProjectName(s))
	if len(fields) == 0 {
		return ""
	}
	if len(fields) > 2 {
		fields = fields[:2]
	}
	return trimProjectName(strings.Join(fields, " "))
}

func trimProjectName(s string) string {
	return trimDisplayName(s, maxProjectNameRunes)
}

func trimDisplayName(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	for len([]rune(s)) > maxRunes {
		s = string([]rune(s)[:maxRunes])
	}
	s = strings.TrimRightFunc(s, func(r rune) bool {
		return unicode.IsPunct(r) || unicode.IsSpace(r)
	})
	return strings.TrimSpace(s)
}

// UpdateNameIfUnset sets name when still null; reports whether a row was updated.
func (r *SQLRegistry) UpdateNameIfUnset(ctx context.Context, id, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `
		UPDATE projects SET name = ?
		WHERE id = ? AND (name IS NULL OR TRIM(name) = '')
	`, db.NullString(name), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	after, err := loadProject(ctx, r.queries.WithTx(tx), id)
	if err != nil {
		return false, err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, after); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	r.notifyProjectEvents()
	return true, nil
}

// TouchLastOpened bumps projects.last_opened_at for recents ordering.
func (r *SQLRegistry) TouchLastOpened(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := r.queries.WithTx(tx)
	now := time.Now().UTC()
	if err := q.TouchProjectLastOpened(ctx, db.TouchProjectLastOpenedParams{
		LastOpenedAt: db.FormatTime(now),
		ID:           id,
	}); err != nil {
		return err
	}
	after, err := loadProject(ctx, q, id)
	if err != nil {
		return err
	}
	if err := r.enqueueProjectEventTx(ctx, tx, api.ProjectEventUpdated, after); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.notifyProjectEvents()
	return nil
}
