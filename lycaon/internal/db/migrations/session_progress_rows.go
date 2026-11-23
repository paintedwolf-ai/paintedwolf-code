package migrations

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"strconv"
	"strings"
)

//go:embed session_progress_rows.sql
var sessionProgressRowsSQL []byte

//go:embed session_progress_rows.go
var sessionProgressRowsSource []byte

var sessionProgressContentRetirement = Retirement{
	Table:  "session_progress",
	Column: "content",
	Capture: `WITH RECURSIVE
lines(session_id, workflow_run_id, line_ord, line, rest) AS (
    SELECT session_id, workflow_run_id, 0,
           substr(content || char(10), 1, instr(content || char(10), char(10)) - 1),
           substr(content || char(10), instr(content || char(10), char(10)) + 1)
    FROM session_progress
    UNION ALL
    SELECT session_id, workflow_run_id, line_ord + 1,
           substr(rest, 1, instr(rest, char(10)) - 1),
           substr(rest, instr(rest, char(10)) + 1)
    FROM lines
    WHERE length(rest) > 0
),
trimmed(session_id, workflow_run_id, line_ord, tline) AS (
    SELECT session_id, workflow_run_id, line_ord,
           trim(line, char(32) || char(9) || char(11) || char(12) || char(13))
    FROM lines
    WHERE length(trim(line, char(32) || char(9) || char(11) || char(12) || char(13))) > 0
),
markers(session_id, workflow_run_id, line_ord, tline, pos) AS (
    SELECT session_id, workflow_run_id, line_ord, tline,
           CASE
               WHEN instr(tline, '- [') > 0 AND (instr(tline, '* [') = 0 OR instr(tline, '- [') < instr(tline, '* [')) THEN instr(tline, '- [')
               ELSE instr(tline, '* [')
           END
    FROM trimmed
    WHERE instr(tline, '- [') > 0 OR instr(tline, '* [') > 0
    UNION ALL
    SELECT session_id, workflow_run_id, line_ord, tline,
           pos + CASE
               WHEN instr(substr(tline, pos + 1), '- [') > 0
                    AND (instr(substr(tline, pos + 1), '* [') = 0 OR instr(substr(tline, pos + 1), '- [') < instr(substr(tline, pos + 1), '* ['))
               THEN instr(substr(tline, pos + 1), '- [')
               ELSE instr(substr(tline, pos + 1), '* [')
           END
    FROM markers
    WHERE instr(substr(tline, pos + 1), '- [') > 0 OR instr(substr(tline, pos + 1), '* [') > 0
),
bounded AS (
    SELECT
        session_id,
        workflow_run_id,
        line_ord,
        tline,
        pos,
        COALESCE(LEAD(pos) OVER (PARTITION BY session_id, line_ord ORDER BY pos), length(tline) + 1) AS next_pos
    FROM markers
),
candidates AS (
    SELECT
        session_id,
        workflow_run_id,
        line_ord,
        pos,
        CASE substr(tline, pos + 3, 1)
            WHEN ' ' THEN 'pending'
            WHEN 'x' THEN 'done'
            WHEN 'X' THEN 'done'
            WHEN '~' THEN 'na'
            WHEN '-' THEN 'na'
            WHEN '>' THEN 'optional'
            ELSE ''
        END AS state,
        trim(
            CASE
                WHEN next_pos <= (CASE WHEN substr(tline, pos + 5, 1) = ' ' THEN pos + 6 ELSE pos + 5 END) THEN ''
                ELSE substr(
                    tline,
                    CASE WHEN substr(tline, pos + 5, 1) = ' ' THEN pos + 6 ELSE pos + 5 END,
                    next_pos - (CASE WHEN substr(tline, pos + 5, 1) = ' ' THEN pos + 6 ELSE pos + 5 END)
                )
            END,
            char(32) || char(9) || char(11) || char(12) || char(13)
        ) AS label
    FROM bounded
    WHERE substr(tline, pos + 4, 1) = ']'
),
parsed AS (
    SELECT
        session_id,
        workflow_run_id,
        ROW_NUMBER() OVER (PARTITION BY session_id ORDER BY line_ord, pos) - 1 AS ord,
        state,
        label
    FROM candidates
    WHERE state != '' AND length(label) > 0
)
SELECT sp.session_id, sp.workflow_run_id, COALESCE(p.ord, -1), COALESCE(p.state, ''), COALESCE(p.label, '')
FROM session_progress sp
LEFT JOIN parsed p ON p.session_id = sp.session_id
ORDER BY sp.session_id, COALESCE(p.ord, -1)`,
	Rebuild: `SELECT sp.session_id, sp.workflow_run_id, COALESCE(spi.ord, -1), COALESCE(spi.state, ''), COALESCE(spi.label, '')
FROM session_progress sp
LEFT JOIN session_progress_items spi ON spi.session_id = sp.session_id
ORDER BY sp.session_id, COALESCE(spi.ord, -1)`,
}

// SessionProgressRows extracts released checklist rows into session_progress_items
// and drops the retired Markdown content column.
func SessionProgressRows() Part {
	source := append(append([]byte(nil), sessionProgressRowsSQL...), sessionProgressRowsSource...)
	return Part{Source: source, Apply: migrateSessionProgressRows}
}

type shippedProgressRow struct {
	state string
	label string
}

type shippedProgressSession struct {
	sessionID string
	items     []shippedProgressRow
}

func migrateSessionProgressRows(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, string(sessionProgressRowsSQL)); err != nil {
		return err
	}
	after := ""
	for {
		batch, last, err := readShippedProgressBatch(ctx, tx, after)
		if err != nil {
			return err
		}
		if last == "" {
			break
		}
		for _, sess := range batch {
			for i, item := range sess.items {
				id := "p" + strconv.Itoa(i+1)
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO session_progress_items (session_id, item_id, ord, state, label) VALUES (?, ?, ?, ?, ?)`,
					sess.sessionID, id, i, item.state, item.label,
				); err != nil {
					return err
				}
			}
			nextSeq := len(sess.items) + 1
			if _, err := tx.ExecContext(ctx,
				`UPDATE session_progress SET next_item_seq = ? WHERE session_id = ?`,
				nextSeq, sess.sessionID,
			); err != nil {
				return err
			}
		}
		after = last
	}
	return DropReleased(ctx, tx, "source-manifests-and-command-records", sessionProgressContentRetirement)
}

func readShippedProgressBatch(ctx context.Context, tx *sql.Tx, after string) (batch []shippedProgressSession, last string, err error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT session_id, content FROM session_progress WHERE session_id > ? ORDER BY session_id LIMIT 32`,
		after,
	)
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
		items := parseShippedChecklistItems(content)
		if len(items) == 0 {
			continue
		}
		batch = append(batch, shippedProgressSession{sessionID: sessionID, items: items})
	}
	return batch, last, rows.Err()
}

// parseShippedChecklistItems freezes the revision 4 checklist row grammar.
func parseShippedChecklistItems(content string) []shippedProgressRow {
	var items []shippedProgressRow
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		search := 0
		for {
			idx, ok := findShippedMarker(line, search)
			if !ok {
				break
			}
			row, end, ok := parseShippedItemAt(line, idx)
			if !ok {
				search = idx + 1
				continue
			}
			items = append(items, row)
			if end <= idx {
				search = idx + 1
			} else {
				search = end
			}
		}
	}
	return items
}

func findShippedMarker(line string, start int) (int, bool) {
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

func parseShippedItemAt(line string, start int) (shippedProgressRow, int, bool) {
	if start >= len(line) || (line[start] != '-' && line[start] != '*') {
		return shippedProgressRow{}, start, false
	}
	i := start + 1
	if i < len(line) && line[i] == ' ' {
		i++
	}
	if i+3 > len(line) || line[i] != '[' || line[i+2] != ']' {
		return shippedProgressRow{}, start, false
	}
	var state string
	switch line[i+1] {
	case ' ':
		state = "pending"
	case 'x', 'X':
		state = "done"
	case '~', '-':
		state = "na"
	case '>':
		state = "optional"
	default:
		return shippedProgressRow{}, start, false
	}
	j := i + 3
	if j < len(line) && line[j] == ' ' {
		j++
	}
	labelEnd := len(line)
	if next, found := findShippedMarker(line, j); found {
		labelEnd = next
	}
	label := strings.TrimSpace(line[j:labelEnd])
	if label == "" {
		return shippedProgressRow{}, start, false
	}
	return shippedProgressRow{state: state, label: label}, labelEnd, true
}
