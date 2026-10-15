package editordoc

import (
	"context"
	"database/sql"
	"strings"
	"sync"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectsource"
	"github.com/lycaon/lycaon/internal/sourcebranch"
)

const readDocumentBytes = 64 << 20
const readDocumentCount = 64

// trimReadDocuments evicts clean, unjoined documents past the read budget, least
// recently opened first; joined documents keep their identities for disconnected replicas.
func (e agentEditPlanner) trimReadDocuments(ctx context.Context, keep string) error {
	s := e.s
	s.ops.Lock()
	defer s.ops.Unlock()
	rows, err := s.store.db.QueryContext(ctx, `SELECT d.id,
 length(CAST(d.draft AS BLOB))+length(CAST(d.base_content AS BLOB))+length(h.checkpoint)+length(h.published_checkpoint)
 +COALESCE((SELECT SUM(length(payload)) FROM editor_document_snapshots WHERE document_id=d.id),0)
 FROM editor_documents d JOIN editor_replica_heads h ON h.document_id=d.id
 WHERE d.dirty=0 AND d.diverged=0 AND d.held_agent_version_id=''
 AND NOT EXISTS (SELECT 1 FROM editor_document_changes c WHERE c.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_agent_receipts a WHERE a.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_replica_receipts r WHERE r.document_id=d.id AND r.command_history IS NOT NULL)
 AND NOT EXISTS (SELECT 1 FROM source_text_contributions c WHERE c.document_id=d.id AND c.revision>1 AND c.origin!='external')
 AND NOT EXISTS (SELECT 1 FROM editor_document_retention t WHERE t.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_replicas r WHERE r.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_save_pins p WHERE p.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM editor_mutations m WHERE m.document_id=d.id)
 AND NOT EXISTS (SELECT 1 FROM source_version_text_states v WHERE v.document_id=d.id)
 ORDER BY (d.id=?) DESC,d.opened_at DESC,d.id`, keep)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var victims []string
	var bytes, count int64
	for rows.Next() {
		var id string
		var size int64
		if err := rows.Scan(&id, &size); err != nil {
			return err
		}
		s.presenceMu.Lock()
		active := len(s.participants[id]) > 0
		s.presenceMu.Unlock()
		if active {
			continue
		}
		bytes += size
		count++
		if id != keep && (bytes > readDocumentBytes || count > readDocumentCount) {
			victims = append(victims, id)
		}
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	for _, id := range victims {
		if err := s.store.Tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM source_text_contributions WHERE document_id=?`, id); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `DELETE FROM editor_documents WHERE id=?`, id)
			return err
		}); err != nil {
			return err
		}
		s.replicas.mu.Lock()
		s.replicas.evict(ctx, id)
		s.replicas.mu.Unlock()
		s.agentReads.mu.Lock()
		for key := range s.agentReads.bases {
			if key.document == id {
				delete(s.agentReads.bases, key)
			}
		}
		s.agentReads.mu.Unlock()
	}
	return nil
}

func (o documentOpener) reserveDocumentSource(ctx context.Context, p *project.Project, d *Document) (func(), error) {
	s := o.s
	var pending bool
	err := s.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM editor_retargets r JOIN source_mutations m ON m.id=r.source_operation_id WHERE r.project_id=? AND r.branch_id=? AND r.root_id=? AND (?=r.from_path OR substr(?,1,length(r.from_path)+1)=r.from_path||'/') AND (m.status IN ('prepared','file_applied','committed') OR COALESCE(json_extract(m.plan_json,'$.hold_started'),0)=1))`, d.ProjectID, d.BranchID, d.RootID, d.Path, d.Path).Scan(&pending)
	if err != nil {
		return nil, err
	}
	if pending {
		return nil, projectsource.ErrSourceBusy
	}
	return s.sourcePaths.ReserveSourcePath(rootPath(p, d.RootID), d.Path)
}

// keyedLocks hands out one mutex per live key.
type keyedLocks struct {
	mu    sync.Mutex
	locks map[string]*keyedLock
}

type keyedLock struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedLocks) lock(key string) (unlock func()) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = make(map[string]*keyedLock)
	}
	l := k.locks[key]
	if l == nil {
		l = &keyedLock{}
		k.locks[key] = l
	}
	l.refs++
	k.mu.Unlock()
	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		k.mu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(k.locks, key)
		}
		k.mu.Unlock()
	}
}

// Distinct, sorted document IDs give transactions a consistent lock order.
func (o documentOpener) lockDocuments(ids []string) func() {
	s := o.s
	release := make([]func(), 0, len(ids))
	for _, id := range ids {
		release = append(release, s.docLocks.lock(documentLockKey(id)))
	}
	return func() {
		for i := len(release) - 1; i >= 0; i-- {
			release[i]()
		}
	}
}

func documentLockKey(id string) string { return "doc\x00" + strings.TrimSpace(id) }

func identityLockKey(projectID, rootID, path string) string {
	return "path\x00" + projectID + "\x00" + rootID + "\x00" + path
}

func (o documentOpener) liveRootPath(ctx context.Context, projectID string, branch sourcebranch.ID, rootID string) (string, error) {
	s := o.s
	if s.roots == nil {
		return "", ErrNotFound
	}
	p, err := s.projectBranch(ctx, projectID, branch)
	if err != nil {
		return "", err
	}
	path := rootPath(p, rootID)
	if path == "" {
		return "", ErrRootDetached
	}
	return path, nil
}

func (o documentOpener) checked(ctx context.Context, id, projectID string) (*Document, error) {
	d, err := o.s.store.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return nil, err
	}
	if d.ProjectID != strings.TrimSpace(projectID) {
		return nil, ErrNotFound
	}
	return d, nil
}

func (o documentOpener) checkedRevision(ctx context.Context, id, projectID, clientID string, expected int64) (*Document, error) {
	d, err := o.s.checked(ctx, id, projectID)
	if err != nil {
		return nil, err
	}
	if d.Revision != expected {
		return nil, ErrRevisionConflict
	}
	if strings.TrimSpace(clientID) == "" {
		return nil, ErrReplicaIdentity
	}
	return d, nil
}

func (o documentOpener) changed(ctx context.Context, d *Document, contentChanged bool) {
	s := o.s
	if s.onChange != nil {
		s.projectWorkspacePresentation(ctx, d)
		s.onChange(ctx, Change{Document: s.withParticipants(d), ContentChanged: contentChanged})
	}
}

