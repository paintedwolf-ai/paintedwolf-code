package sourceledger

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// WalkSummary counts the same grouped steps as Walk, only for requested transcript turns.
func (s *Store) WalkSummary(ctx context.Context, projectID, sessionID string, messageIDs []string) ([]api.SourceWalkTurnSummary, error) {
	if len(messageIDs) > 100 {
		return nil, fmt.Errorf("at most 100 message ids are allowed")
	}
	out := make([]api.SourceWalkTurnSummary, 0, len(messageIDs))
	if len(messageIDs) == 0 {
		return out, nil
	}
	raw, err := db.MarshalJSON(messageIDs)
	if err != nil {
		return nil, err
	}
	rows, err := s.sqlDB.QueryContext(ctx, walkSummarySQL, sessionID, projectID, raw.String)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var summary api.SourceWalkTurnSummary
		if err := rows.Scan(&summary.MessageID, &summary.Turn, &summary.Steps, &summary.Items); err != nil {
			return nil, err
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}

const walkSummarySQL = `WITH numbered AS MATERIALIZED (
 SELECT m.id, m.session_id, s.project_id,
        t.turn
 FROM messages m INDEXED BY idx_messages_user_turn JOIN sessions s ON s.id = m.session_id
 JOIN session_source_turns t ON t.opening_message_id = m.id
 WHERE m.session_id = ? AND s.project_id = ? AND m.role = 'user'
   AND m.visibility <> 'internal' AND m.kind <> 'workflow_boundary' AND m.kind <> 'user_continuation'
   AND m.workflow_boundary_json IS NULL
), requested AS MATERIALIZED (
 SELECT id, session_id, project_id, turn FROM numbered WHERE id IN (SELECT value FROM json_each(?))
), git_candidates AS MATERIALIZED (
 SELECT DISTINCT o.git_transition_id AS id, r.session_id
 FROM requested r CROSS JOIN source_operations o
 WHERE o.project_id = r.project_id AND o.session_id = r.session_id AND o.session_id <> ''
   AND o.turn = r.turn AND o.git_transition_id IS NOT NULL
), git_groups AS MATERIALIZED (
 SELECT g.id, MIN(o.turn) AS first_turn, MAX(o.turn) AS last_turn
 FROM git_candidates g CROSS JOIN source_operations o
 WHERE o.git_transition_id = g.id AND o.session_id = g.session_id
   AND EXISTS (SELECT 1 FROM source_effects e WHERE e.operation_id = o.id AND e.walk_visible = 1)
 GROUP BY g.id
), effects AS (
 SELECT r.id AS message_id, e.file_id,
   CASE WHEN o.git_transition_id IS NOT NULL THEN 'git:' || o.git_transition_id ELSE 'effect:' || e.id END AS step
 FROM requested r CROSS JOIN source_operations o CROSS JOIN source_effects e
 LEFT JOIN git_groups g ON g.id = o.git_transition_id
 LEFT JOIN source_git_transitions t ON t.id = o.git_transition_id
 WHERE o.project_id = r.project_id AND e.operation_id = o.id AND e.walk_visible = 1
   AND EXISTS (SELECT 1 FROM source_effect_authors a WHERE a.effect_id=e.id AND a.session_id=r.session_id AND a.turn=r.turn)
   AND (o.git_transition_id IS NOT NULL OR o.command_window_id IS NULL)
   AND (o.git_transition_id IS NULL OR (t.session_id = r.session_id AND t.turn = r.turn) OR (t.session_id = '' AND g.first_turn = r.turn AND g.last_turn = r.turn))
 UNION ALL
 SELECT r.id, e.file_id, 'command:' || c.id
 FROM requested r CROSS JOIN source_command_windows c CROSS JOIN source_operations o CROSS JOIN source_effects e
 WHERE c.project_id = r.project_id AND c.session_id = r.session_id AND c.turn = r.turn
   AND o.command_window_id = c.id AND o.git_transition_id IS NULL AND o.session_id = r.session_id
   AND e.operation_id = o.id AND e.walk_visible = 1
 UNION ALL
 SELECT r.id, NULL, 'git:' || t.id
 FROM requested r JOIN source_git_transitions t ON t.project_id=r.project_id
 WHERE t.session_id=r.session_id AND t.turn=r.turn
)
SELECT r.id, r.turn, COUNT(DISTINCT effects.step), COUNT(DISTINCT effects.file_id)
FROM requested r LEFT JOIN effects ON effects.message_id = r.id
GROUP BY r.id ORDER BY r.turn`
