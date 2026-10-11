package sourceledger

import (
	"context"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/pkg/api"
)

// Contributor identifies an author, independently of who published the file.
// PersonID names the person behind user contributions.
type Contributor struct {
	Origin                                                       api.SourceChangeOrigin
	SessionID, PersonID, ActorLabel, ToolCallID, ToolName, JobID string
	Turn                                                         int
}

func (s *Walk) hydrateEffectAuthors(ctx context.Context, projectID string, effects []Effect) error {
	if len(effects) == 0 {
		return nil
	}
	ids := make([]string, len(effects))
	indexes := make(map[string]int, len(effects))
	for i := range effects {
		ids[i], indexes[effects[i].ID] = effects[i].ID, i
		effects[i].Contributors = []Contributor{}
	}
	raw, err := jsonArray(ids)
	if err != nil {
		return err
	}
	rows, err := s.queries.ListSourceEffectAuthors(ctx, db.ListSourceEffectAuthorsParams{ProjectID: projectID, EffectIds: raw})
	if err != nil {
		return err
	}
	type key struct {
		effect      string
		contributor Contributor
	}
	seen := make(map[key]bool)
	for _, row := range rows {
		c := Contributor{Origin: api.SourceChangeOrigin(row.Origin), SessionID: row.SessionID,
			PersonID: row.PersonID, ActorLabel: row.ActorLabel, ToolCallID: row.ToolCallID,
			ToolName: row.ToolName, JobID: row.JobID, Turn: int(row.Turn)}
		k := key{row.EffectID, c}
		if seen[k] {
			continue
		}
		seen[k] = true
		i := indexes[row.EffectID]
		effects[i].Contributors = append(effects[i].Contributors, c)
	}
	return nil
}

func (e Effect) onlyUserContributions() bool {
	if len(e.Contributors) == 0 {
		return e.Origin == api.SourceChangeOriginUser
	}
	for _, c := range e.Contributors {
		if c.Origin != api.SourceChangeOriginUser {
			return false
		}
	}
	return true
}

func (s *Walk) hydrateVersionAuthors(ctx context.Context, projectID string, versions []Version) error {
	effects := make([]Effect, len(versions))
	for i := range versions {
		effects[i].ID = versions[i].EffectID
	}
	if err := s.hydrateEffectAuthors(ctx, projectID, effects); err != nil {
		return err
	}
	for i := range versions {
		versions[i].Contributors = effects[i].Contributors
	}
	return nil
}
