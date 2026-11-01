package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
)

//go:embed session_progress_identity.sql
var sessionProgressIdentitySQL []byte

// SessionProgressIdentity adds durable checklist row identities and a monotonic
// document version to session_progress, backfilling existing rows with stable IDs.
func SessionProgressIdentity(target Baseline) Step {
	return Step{
		ID:       "session-progress-item-identity",
		Source:   sessionProgressIdentitySQL,
		Checksum: "3649f6409cb2306aad84ea5d8264bdc67107470da28b9487263a74e4d7fd69d7",
		From:     Baseline{Revision: 11, Shape: "3a065b2adccf2628ce0e203a4e04962c974b462a7cae85e8996ba4e7696fe9e6"},
		To:       target,
		Apply:    migrateSessionProgressIdentity,
	}
}

type progressIdentityUpdate struct {
	sessionID   string
	itemIDsJSON string
}

func migrateSessionProgressIdentity(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, string(sessionProgressIdentitySQL)); err != nil {
		return err
	}
	after := ""
	for {
		batch, last, err := readProgressIdentityBatch(ctx, tx, after)
		if err != nil {
			return err
		}
		if last == "" {
			return nil
		}
		for _, item := range batch {
			if _, err := tx.ExecContext(ctx, `UPDATE session_progress SET item_ids_json = ? WHERE session_id = ?`, item.itemIDsJSON, item.sessionID); err != nil {
				return err
			}
		}
		after = last
	}
}

func readProgressIdentityBatch(ctx context.Context, tx *sql.Tx, after string) (batch []progressIdentityUpdate, last string, err error) {
	rows, err := tx.QueryContext(ctx, `SELECT session_id, content FROM session_progress WHERE session_id > ? ORDER BY session_id LIMIT 32`, after)
	if err != nil {
		return nil, "", err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var sessionID, content string
		if err := rows.Scan(&sessionID, &content); err != nil {
			return nil, "", err
		}
		last = sessionID
		count := countRevision11ChecklistItems(content)
		if count == 0 {
			continue
		}
		ids := make([]string, count)
		for i := range ids {
			ids[i] = uuid.NewString()
		}
		encoded, err := json.Marshal(ids)
		if err != nil {
			return nil, "", err
		}
		batch = append(batch, progressIdentityUpdate{sessionID: sessionID, itemIDsJSON: string(encoded)})
	}
	return batch, last, rows.Err()
}

// countRevision11ChecklistItems freezes revision 11's checklist row grammar.
func countRevision11ChecklistItems(content string) int {
	var count int
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		search := 0
		for {
			idx, ok := findRevision11ChecklistMarker(line, search)
			if !ok {
				break
			}
			end, ok := parseRevision11ChecklistItemAt(line, idx)
			if !ok {
				search = idx + 1
				continue
			}
			count++
			if end <= idx {
				search = idx + 1
			} else {
				search = end
			}
		}
	}
	return count
}

func findRevision11ChecklistMarker(line string, start int) (int, bool) {
	for i := start; i < len(line); i++ {
		if line[i] != '-' && line[i] != '*' {
			continue
		}
		if i+2 >= len(line) || line[i+1] != ' ' || line[i+2] != '[' {
			continue
		}
		return i, true
	}
	return 0, false
}

func parseRevision11ChecklistItemAt(line string, start int) (int, bool) {
	if start >= len(line) || (line[start] != '-' && line[start] != '*') {
		return start, false
	}
	i := start + 1
	if i < len(line) && line[i] == ' ' {
		i++
	}
	if i+3 > len(line) || line[i] != '[' || line[i+2] != ']' {
		return start, false
	}
	switch line[i+1] {
	case ' ', 'x', 'X', '~', '-', '>':
	default:
		return start, false
	}
	j := i + 3
	if j < len(line) && line[j] == ' ' {
		j++
	}
	labelEnd := len(line)
	if next, found := findRevision11ChecklistMarker(line, j); found {
		labelEnd = next
	}
	if strings.TrimSpace(line[j:labelEnd]) == "" {
		return start, false
	}
	return labelEnd, true
}
