package editordoc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/lycaon/lycaon/internal/documentcore"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/textfile"
)

// ResolveSourceRewind applies retained CRDT inverses to a clone, preserving later edits.
func (s *Service) ResolveSourceRewind(ctx context.Context, p *project.Project, sessionID string, anchors []string, plan *sourceledger.RewindPlan) error {
	s.ops.RLock()
	defer s.ops.RUnlock()
	ledger := s.history
	if ledger == nil {
		return nil
	}
	docs, err := s.store.ListProject(ctx, p.ID, "")
	if err != nil {
		return err
	}
	for i := range plan.Files {
		file := &plan.Files[i]
		if file.Expected.State != "content" || file.Target.State != "content" || file.Expected.Path != file.Target.Path {
			continue
		}
		needsSemantic := false
		for _, issue := range plan.Issues {
			if issue.RootID == file.Expected.RootID && issue.Path == file.Expected.Path && semanticRewindIssue(issue.Code) {
				needsSemantic = true
			}
		}
		if !needsSemantic {
			continue
		}
		for _, d := range docs {
			if d.FileID != file.FileID || d.BranchID != file.BranchID || d.Dirty || d.Diverged {
				continue
			}
			unlock := s.docLocks.lock(documentLockKey(d.ID))
			current, err := s.store.Get(ctx, d.ID)
			if err != nil {
				unlock()
				return err
			}
			head, err := ledger.ResolveHeadByFile(ctx, p.ID, file.BranchID, file.FileID)
			if err != nil {
				unlock()
				return err
			}
			version, err := ledger.ReadRestorableVersion(ctx, p.ID, head.VersionID)
			if err != nil {
				unlock()
				return err
			}
			if current.Dirty || current.Diverged || current.BaseSHA256 != version.SHA256 {
				unlock()
				continue
			}
			target, err := s.semanticRewindContent(ctx, current, sessionID, anchors)
			unlock()
			if err != nil {
				return err
			}
			if target == nil {
				continue
			}
			file.Expected = version
			file.Target.Content = target.content
			file.DocumentCheckpoint = target.checkpoint
			file.Target.SHA256 = textfile.SHA256(target.content)
			file.DocumentID = current.ID
			file.DocumentRevision = current.Revision
			kept := plan.Issues[:0]
			for _, issue := range plan.Issues {
				if issue.RootID == file.Expected.RootID && issue.Path == file.Expected.Path && semanticRewindIssue(issue.Code) {
					continue
				}
				kept = append(kept, issue)
			}
			plan.Issues = kept
			break
		}
	}
	return nil
}

func semanticRewindIssue(code string) bool {
	return code == "shared_contribution" || code == "later_change" || code == "intervening_change"
}

type semanticRewindUndo struct {
	epoch    int64
	bytes    []byte
	reverted string
}

type semanticRewindTarget struct{ content, checkpoint []byte }

func (s *Service) semanticRewindContent(ctx context.Context, d *Document, sessionID string, anchors []string) (*semanticRewindTarget, error) {
	replicaHead, err := s.store.replicaHead(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	undos, err := s.semanticRewindUndos(ctx, d, sessionID, anchors, replicaHead.Epoch)
	if err != nil {
		return nil, err
	}
	if len(undos) == 0 {
		return nil, nil
	}
	s.replicas.mu.Lock()
	defer s.replicas.mu.Unlock()
	entry, err := s.loadReplica(ctx, d)
	if err != nil {
		return nil, err
	}
	s.replicas.next++
	handle := s.replicas.next
	defer s.replicas.drop(context.WithoutCancel(ctx), handle)
	snapshot, err := s.replicas.engine.Call(ctx, documentcore.Request{Action: "open", Handle: handle, Client: entry.head.HostClient, Update: entry.head.PublishedCheckpoint})
	if err != nil {
		return nil, err
	}
	if snapshot.Text != d.BaseContent {
		return nil, nil
	}
	for _, u := range undos {
		snapshot, err = s.replicas.engine.Call(ctx, documentcore.Request{Action: "undo", Handle: handle, Client: entry.head.HostClient, Undo: u.bytes, Checkpoint: true})
		if err != nil {
			var rejected *documentcore.Rejected
			if errors.As(err, &rejected) {
				return nil, nil
			}
			return nil, err
		}
	}
	content, err := textfile.EncodeBounded(serializeEOL(snapshot.Text, d.EOL), d.Encoding, textfile.LimitsForRaw(project.SourceWriteMaxBytes))
	if err != nil {
		return nil, err
	}
	return &semanticRewindTarget{content: content, checkpoint: snapshot.Checkpoint}, nil
}

func (s *Service) semanticRewindUndos(ctx context.Context, d *Document, sessionID string, anchors []string, epoch int64) ([]semanticRewindUndo, error) {
	encoded, err := json.Marshal(anchors)
	if err != nil {
		return nil, err
	}
	// A missing inverse leaves the selected history incomplete.
	rows, err := s.store.db.QueryContext(ctx, `WITH selected AS (
  SELECT session_id,turn FROM session_source_turns WHERE session_id=? AND opening_message_id IN (SELECT value FROM json_each(?))
 )
 SELECT DISTINCT COALESCE(h.epoch,0),COALESCE(h.undo_bytes,X''),COALESCE(h.reverted_by,''),COALESCE(h.revision,0)
 FROM source_effects e JOIN source_effect_authors a ON a.effect_id=e.id
 JOIN selected t ON t.session_id=a.session_id AND t.turn=a.turn
 LEFT JOIN source_text_contributions c ON c.id=a.contribution_id
 LEFT JOIN editor_document_changes h ON h.document_id=c.document_id AND h.operation_id=c.operation_id AND h.document_id=?
 WHERE e.project_id=? AND e.file_id=? AND a.origin='agent' ORDER BY 4 DESC`, sessionID, string(encoded), d.ID, d.ProjectID, d.FileID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	undos := []semanticRewindUndo{}
	available := true
	for rows.Next() {
		var u semanticRewindUndo
		var revision int64
		if err := rows.Scan(&u.epoch, &u.bytes, &u.reverted, &revision); err != nil {
			return nil, err
		}
		if revision == 0 || u.epoch != epoch || (u.reverted == "" && len(u.bytes) == 0) {
			available = false
		}
		if u.reverted == "" {
			undos = append(undos, u)
		}
	}
	err = rows.Err()
	if err != nil {
		return nil, err
	}
	if !available || len(undos) == 0 {
		return nil, nil
	}
	return undos, nil
}
