package historyretention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/visual"
	"github.com/lycaon/lycaon/pkg/api"
)

const previewBatch = 128

var (
	ErrPreviewChanged     = errors.New("history or policy changed; review a new pruning preview")
	ErrProtectionNotFound = errors.New("history protection not found")
)

type plan struct {
	request    api.HistoryRetentionRequest
	projectID  string
	generation int64
	created    time.Time
	items      []candidate
	preview    api.HistoryRetentionPreview
	spool      *os.File
	remaining  int64
}

// Service coordinates reviewed policies and bounded body cleanup.
type Service struct {
	Database  db.Handle
	DataDir   string
	StorePath string
	Artifacts visual.Store
	Now       func() time.Time
	mu        sync.Mutex
	plans     map[string]plan
	lanes     []api.HistoryStorageLane
}

func New(database db.Handle, storePath string, artifacts visual.Store) *Service {
	return &Service{Database: database, DataDir: filepath.Dir(storePath), StorePath: storePath, Artifacts: artifacts, Now: time.Now, plans: make(map[string]plan)}
}

func (s *Service) Status(ctx context.Context) (api.HistoryStorageStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	policy, err := readPolicy(s.DataDir)
	if err != nil {
		return api.HistoryStorageStatus{}, err
	}
	out := api.HistoryStorageStatus{Policy: policy, Lanes: append([]api.HistoryStorageLane{}, s.lanes...), Protections: []api.HistoryProtection{}}
	rows, err := s.Database.QueryContext(ctx, `SELECT owner_id,session_id IS NOT NULL FROM history_protections WHERE protected = 1 ORDER BY owner_id`)
	if err != nil {
		return out, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var session bool
		if err := rows.Scan(&id, &session); err != nil {
			return out, err
		}
		kind := "project"
		if session {
			kind = "session"
		}
		out.Protections = append(out.Protections, api.HistoryProtection{ScopeType: kind, ScopeID: id, Protected: true})
	}
	return out, rows.Err()
}

func (s *Service) generation(ctx context.Context) (int64, error) {
	var generation int64
	err := s.Database.QueryRowContext(ctx, `SELECT generation FROM history_storage_clock WHERE id = 1`).Scan(&generation)
	return generation, err
}

func (s *Service) Preview(ctx context.Context, request api.HistoryRetentionRequest, projectID string) (api.HistoryRetentionPreview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preview(ctx, request, projectID)
}

func (s *Service) preview(ctx context.Context, request api.HistoryRetentionRequest, projectID string) (api.HistoryRetentionPreview, error) {
	if err := validatePolicy(request.Policy); err != nil {
		return api.HistoryRetentionPreview{}, err
	}
	current, err := readPolicy(s.DataDir)
	if err != nil {
		return api.HistoryRetentionPreview{}, err
	}
	if request.Policy.Revision != current.Revision {
		return api.HistoryRetentionPreview{}, ErrPreviewChanged
	}
	generation, err := s.generation(ctx)
	if err != nil {
		return api.HistoryRetentionPreview{}, err
	}
	now := s.Now().UTC()
	spool, err := newPlanSpool()
	if err != nil {
		return api.HistoryRetentionPreview{}, err
	}
	keepSpool := false
	defer func() {
		if !keepSpool {
			_ = spool.Close()
		}
	}()
	encoder := json.NewEncoder(spool)
	p := plan{spool: spool, request: request, projectID: projectID, generation: generation, created: now, preview: api.HistoryRetentionPreview{Token: uuid.NewString(), PolicyRevision: current.Revision, Candidates: []api.HistoryPruneCandidate{}}}
	for i, rule := range rules(request.Policy) {
		if rule.Mode == "forever" {
			continue
		}
		before := now.Add(time.Nanosecond)
		if rule.Mode == "max_age" {
			before = now.AddDate(0, 0, -int(rule.MaxAgeDays))
		}
		remaining, err := s.classBytes(ctx, classes[i], projectID)
		if err != nil {
			return p.preview, err
		}
		retainedAtStart := remaining
		var plannedBytes int64
		var afterTime, afterID string
		for {
			if err := ctx.Err(); err != nil {
				return p.preview, err
			}
			items, err := listCandidates(ctx, s.Database, classes[i], projectID, db.FormatTime(before), afterTime, afterID)
			if err != nil {
				return p.preview, err
			}
			if len(items) == 0 {
				break
			}
			for _, item := range items {
				item, err = s.groupCandidate(ctx, item, projectID, db.FormatTime(before))
				if err != nil {
					return p.preview, err
				}
				if item.ReclaimableBytes == 0 {
					continue
				}
				if rule.Mode == "max_bytes" && remaining <= rule.MaxBytes {
					continue
				}
				remaining -= item.ReclaimableBytes
				plannedBytes += item.ReclaimableBytes
				p.preview.EligibleCount += ownerCount(item)
				p.preview.ReclaimableBytes += item.ReclaimableBytes
				if len(p.items) < previewBatch {
					p.items = append(p.items, item)
					if len(item.Members) == 0 {
						if len(p.preview.Candidates) < previewBatch {
							p.preview.Candidates = append(p.preview.Candidates, item.HistoryPruneCandidate)
						}
					} else {
						for _, member := range item.Members {
							if len(p.preview.Candidates) < previewBatch {
								p.preview.Candidates = append(p.preview.Candidates, member.HistoryPruneCandidate)
							}
						}
					}
				} else {
					if err := encoder.Encode(item); err != nil {
						return p.preview, err
					}
					p.remaining++
				}
			}
			last := items[len(items)-1]
			afterTime = last.RawCreatedAt
			afterID = last.ID
		}
		p.preview.SharedProtectedBytes += max(0, retainedAtStart-plannedBytes)
	}
	end, err := s.generation(ctx)
	if err != nil {
		return p.preview, err
	}
	if generation != end {
		return p.preview, ErrPreviewChanged
	}
	p.preview.Complete = p.remaining == 0
	for token, old := range s.plans {
		if now.Sub(old.created) > 10*time.Minute {
			_ = old.spool.Close()
			delete(s.plans, token)
		}
	}
	if len(s.plans) >= 32 {
		var oldest string
		var at time.Time
		for token, old := range s.plans {
			if oldest == "" || old.created.Before(at) {
				oldest = token
				at = old.created
			}
		}
		_ = s.plans[oldest].spool.Close()
		delete(s.plans, oldest)
	}
	if _, err := spool.Seek(0, 0); err != nil {
		return p.preview, err
	}
	keepSpool = true
	s.plans[p.preview.Token] = p
	return p.preview, nil
}

func (s *Service) reviewed(ctx context.Context, request api.HistoryRetentionRequest, projectID string) (plan, error) {
	p, ok := s.plans[request.PreviewToken]
	if !ok || s.Now().Sub(p.created) > 10*time.Minute {
		return p, ErrPreviewChanged
	}
	if p.request.Policy != request.Policy || p.projectID != projectID {
		return p, ErrPreviewChanged
	}
	generation, err := s.generation(ctx)
	if err != nil {
		return p, err
	}
	if generation != p.generation {
		return p, ErrPreviewChanged
	}
	current, err := readPolicy(s.DataDir)
	if err != nil {
		return p, err
	}
	if current.Revision != request.Policy.Revision {
		return p, ErrPreviewChanged
	}
	return p, nil
}

func (s *Service) SetPolicy(ctx context.Context, request api.HistoryRetentionRequest, projectID string) (api.HistoryRetentionPolicy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.reviewed(ctx, request, projectID); err != nil {
		return request.Policy, err
	}
	policy := request.Policy
	policy.Revision++
	policy.Suspended = false
	if err := writePolicy(s.DataDir, policy); err != nil {
		return policy, err
	}
	s.closePlans()
	return policy, nil
}

func (s *Service) Protect(ctx context.Context, protection api.HistoryProtection) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var query string
	switch protection.ScopeType {
	case "project":
		query = `INSERT INTO history_protections(project_id,session_id,owner_id,protected) SELECT id,NULL,id,? FROM projects WHERE id=? ON CONFLICT(owner_id) DO UPDATE SET protected=excluded.protected`
	case "session":
		query = `INSERT INTO history_protections(project_id,session_id,owner_id,protected) SELECT project_id,id,id,? FROM sessions WHERE id=? ON CONFLICT(owner_id) DO UPDATE SET protected=excluded.protected`
	default:
		return fmt.Errorf("%w: unknown history protection owner", ErrInvalidPolicy)
	}
	result, err := s.Database.ExecContext(ctx, query, protection.Protected, protection.ScopeID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count == 0 {
		return fmt.Errorf("%w: history protection owner not found", ErrInvalidPolicy)
	}
	return err
}

func (s *Service) DeleteProtection(ctx context.Context, protectionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	result, err := s.Database.ExecContext(ctx, `DELETE FROM history_protections WHERE owner_id=?`, protectionID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrProtectionNotFound
	}
	return nil
}

// Run starts only after host construction; the default policy never prunes.
func (s *Service) Run(ctx context.Context) error {
	defer func() { s.mu.Lock(); defer s.mu.Unlock(); s.closePlans() }()
	if s.Artifacts != nil {
		if err := s.Artifacts.CollectGarbage(ctx); err != nil {
			slog.WarnContext(ctx, "Artifact cleanup deferred", "error", err)
		}
	}
	if err := s.refreshUsage(ctx); err != nil {
		slog.WarnContext(ctx, "History storage measurement failed", "error", err)
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	var lastPrune time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		if s.Artifacts != nil {
			if err := s.Artifacts.CollectGarbage(ctx); err != nil {
				slog.WarnContext(ctx, "Artifact cleanup deferred", "error", err)
			}
		}
		if err := s.refreshUsage(ctx); err != nil {
			slog.WarnContext(ctx, "History storage measurement failed", "error", err)
		}
		if s.Now().Sub(lastPrune) < 24*time.Hour {
			continue
		}
		policy, err := readPolicy(s.DataDir)
		if err != nil {
			continue
		}
		if policy.Suspended {
			continue
		}
		request := api.HistoryRetentionRequest{Policy: policy}
		preview, err := s.Preview(ctx, request, "")
		if err != nil {
			continue
		}
		request.PreviewToken = preview.Token
		for {
			result, err := s.Prune(ctx, request, "")
			if err != nil || result.Complete {
				break
			}
			request.PreviewToken = result.PreviewToken
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		lastPrune = s.Now()
	}
}
