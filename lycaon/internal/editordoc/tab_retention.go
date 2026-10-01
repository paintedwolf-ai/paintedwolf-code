package editordoc

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
)

// ClientLive reports whether a client holds a live event stream; nil treats every client as gone.
type ClientLive func(clientID string) bool

// ReplaceRetention records tab references without creating collaboration presence.
// A window inventory sweeps the caller's namespace, but a client whose stream is
// live keeps its marks even when a stale inventory omits it.
func (s *Service) ReplaceRetention(ctx context.Context, projectID, clientID string, ids []string, retainedClients []string, live ClientLive) error {
	if strings.TrimSpace(clientID) == "" || len(clientID) > 256 || len(ids) > 16384 {
		return ErrReplicaIdentity
	}
	actor, err := s.personActor(ctx, actorUser, clientID)
	if err != nil {
		return err
	}
	if err := s.bindClientPerson(clientID, actor.personID); err != nil {
		return err
	}
	if len(retainedClients) > 16384 {
		return ErrReplicaIdentity
	}
	for _, client := range retainedClients {
		if len(client) == 0 || len(client) > 256 {
			return ErrReplicaIdentity
		}
	}
	namespace := "window:"
	if strings.HasPrefix(clientID, "browser:") {
		namespace = "browser:"
	}
	if retainedClients != nil && (!strings.HasPrefix(clientID, namespace) || !slices.Contains(retainedClients, clientID)) {
		return ErrReplicaIdentity
	}
	clients, err := json.Marshal(retainedClients)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	s.ops.RLock()
	defer s.ops.RUnlock()
	return s.store.Tx(ctx, func(tx *sql.Tx) error {
		if retainedClients != nil {
			orphans, err := orphanedRetentionClients(ctx, tx, actor.personID, namespace, string(clients), live)
			if err != nil {
				return err
			}
			for _, orphan := range orphans {
				if _, err := tx.ExecContext(ctx, `DELETE FROM editor_document_retention WHERE person_id=? AND client_id=?`, actor.personID, orphan); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM editor_document_retention WHERE client_id=? AND person_id=?
		 AND document_id IN (SELECT id FROM editor_documents WHERE project_id=?)`, clientID, actor.personID, projectID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO editor_document_retention(document_id,client_id,person_id)
		 SELECT id,?,? FROM editor_documents WHERE project_id=? AND id IN (SELECT value FROM json_each(?))`, clientID, actor.personID, projectID, string(encoded))
		return err
	})
}

// orphanedRetentionClients lists the person's clients in the namespace that the
// inventory omits and that hold no live stream.
func orphanedRetentionClients(ctx context.Context, tx *sql.Tx, personID, namespace, inventory string, live ClientLive) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT client_id FROM editor_document_retention WHERE person_id=? AND substr(client_id,1,?)=?
	 AND client_id NOT IN (SELECT value FROM json_each(?))`, personID, len(namespace), namespace, inventory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var orphans []string
	for rows.Next() {
		var client string
		if err := rows.Scan(&client); err != nil {
			return nil, err
		}
		if live != nil && live(client) {
			continue
		}
		orphans = append(orphans, client)
	}
	return orphans, rows.Err()
}

// LifecycleDependents reads identities only, including tabs outside presence.
func (s *Service) LifecycleDependents(ctx context.Context, projectID, rootID string) ([]*Document, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	s.presenceMu.Lock()
	ids := make([]string, 0, len(s.participants))
	for id, participants := range s.participants {
		if len(participants) > 0 {
			ids = append(ids, id)
		}
	}
	s.presenceMu.Unlock()
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.db.QueryContext(ctx, `SELECT d.id,d.path,d.dirty FROM editor_documents d WHERE d.project_id=?
 AND (?='' OR d.root_id=?) AND (d.dirty=1 OR d.id IN (SELECT value FROM json_each(?))
 OR EXISTS (SELECT 1 FROM editor_document_retention t WHERE t.document_id=d.id)) ORDER BY d.path,d.id`, strings.TrimSpace(projectID), strings.TrimSpace(rootID), strings.TrimSpace(rootID), string(encoded))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []*Document
	for rows.Next() {
		d := new(Document)
		if err := rows.Scan(&d.ID, &d.Path, &d.Dirty); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, rows.Err()
}
