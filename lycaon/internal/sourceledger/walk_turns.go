package sourceledger

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

func (s *Walk) walkTurns(ctx context.Context, projectID string, effects []Effect, movements []GitTransition) ([]api.SourceWalkTurn, error) {
	type turnKey struct {
		session string
		turn    int
	}
	seen := make(map[turnKey]bool)
	keys := make([][2]any, 0)
	for _, effect := range effects {
		authors := effect.Contributors
		if len(authors) == 0 {
			authors = []Contributor{{SessionID: effect.SessionID, Turn: effect.Turn}}
		}
		for _, author := range authors {
			key := turnKey{author.SessionID, author.Turn}
			if key.session == "" || key.turn < 1 || seen[key] {
				continue
			}
			seen[key] = true
			keys = append(keys, [2]any{key.session, key.turn})
		}
	}
	for _, movement := range movements {
		key := turnKey{movement.SessionID, movement.Turn}
		if key.session != "" && key.turn > 0 && !seen[key] {
			seen[key] = true
			keys = append(keys, [2]any{key.session, key.turn})
		}
	}
	out := make([]api.SourceWalkTurn, 0, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	raw, err := jsonArray(keys)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSourceWalkTurns(ctx, db.ListSourceWalkTurnsParams{ProjectID: projectID, TurnKeys: raw})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		ts, err := db.ParseTime(row.Ts)
		if err != nil {
			return nil, err
		}
		out = append(out, api.SourceWalkTurn{
			SessionID: row.SessionID, Turn: int(row.Turn), MessageID: row.MessageID,
			Prompt: strings.Join(strings.Fields(row.Prompt), " "), ObservedAt: ts,
		})
	}
	return out, nil
}
