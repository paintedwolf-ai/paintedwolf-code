package editordoc

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

const observationPageSize = 64

type observationScope struct {
	Root   string          `json:"root"`
	Branch sourcebranch.ID `json:"branch"`
	Path   string          `json:"path"`
}

type observationQuery struct {
	project, resident, scopes string
}
type observedDocument struct{ ID string }

func (s *Service) observationQuery(p *project.Project, selection []PathRef) (observationQuery, error) {
	ids := make(map[string]bool)
	s.replicas.mu.Lock()
	for id := range s.replicas.entries {
		ids[id] = true
	}
	s.replicas.mu.Unlock()
	s.presenceMu.Lock()
	for id, participants := range s.participants {
		if len(participants) > 0 {
			ids[id] = true
		}
	}
	s.presenceMu.Unlock()
	keys := make([]string, 0, len(ids))
	for id := range ids {
		keys = append(keys, id)
	}
	scopes := make([]observationScope, 0, len(selection))
	if len(selection) == 0 {
		for _, root := range p.Roots {
			scopes = append(scopes, observationScope{root.ID, p.BranchForRoot(root.ID), "."})
		}
	} else {
		for _, ref := range selection {
			root, path := strings.TrimSpace(ref.RootID), strings.TrimSpace(ref.Path)
			scopes = append(scopes, observationScope{root, p.BranchForRoot(root), path})
		}
	}
	resident, err := json.Marshal(keys)
	if err != nil {
		return observationQuery{}, err
	}
	scope, err := json.Marshal(scopes)
	return observationQuery{p.ID, string(resident), string(scope)}, err
}

// Pages contain identities only; bodies are loaded under the document lock.
func (s *Store) observedDocumentPage(ctx context.Context, q observationQuery, after string) ([]observedDocument, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id FROM editor_documents d
 WHERE d.project_id=? AND d.id>?
 AND (d.dirty=1 OR d.diverged=1 OR d.held_agent_version_id!='' OR d.id IN (SELECT value FROM json_each(?))
 OR EXISTS (SELECT 1 FROM editor_document_retention t WHERE t.document_id=d.id))
 AND EXISTS (SELECT 1 FROM json_each(?) scope
 WHERE d.root_id=json_extract(scope.value,'$.root') AND d.branch_id=json_extract(scope.value,'$.branch')
 AND (json_extract(scope.value,'$.path')='.' OR d.path=json_extract(scope.value,'$.path')
 OR substr(d.path,1,length(json_extract(scope.value,'$.path'))+1)=json_extract(scope.value,'$.path')||'/'))
 ORDER BY d.id LIMIT ?`, q.project, after, q.resident, q.scopes, observationPageSize)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	page := make([]observedDocument, 0, observationPageSize)
	for rows.Next() {
		var d observedDocument
		if err := rows.Scan(&d.ID); err != nil {
			return nil, err
		}
		page = append(page, d)
	}
	return page, rows.Err()
}
