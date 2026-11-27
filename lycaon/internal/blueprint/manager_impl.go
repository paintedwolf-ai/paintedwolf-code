// Package blueprint defines the blueprint document lifecycle for workflows.
package blueprint

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/blueprintfile"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/pkg/api"
)

// Manager handles blueprint CRUD and approval.
type Manager struct {
	Store     Store
	Projects  project.Registry
	Approvals *ApprovalStore
	// ActiveRun avoids a workflow import cycle.
	ActiveRun func(ctx context.Context, projectID, path string) (string, bool, error)
	// BeforeRetarget captures rewind pre-images before a provisional file moves.
	BeforeRetarget BeforeRetarget
	// AfterRetarget rewrites host bindings after a provisional file moves.
	AfterRetarget AfterRetarget
}

// NewManager creates a blueprint manager.
func NewManager(store Store) *Manager {
	return &Manager{Store: store}
}

// SetProjects wires project root lookup for path-keyed ops that only have a path.
func (m *Manager) SetProjects(reg project.Registry) {
	if m != nil {
		m.Projects = reg
	}
}

// Create inserts a draft blueprint under the active convention.
func (m *Manager) Create(ctx context.Context, projectID, title, path, sourceWorkflowID, content string) (*api.Blueprint, error) {
	projectID = strings.TrimSpace(projectID)
	title = strings.TrimSpace(title)
	if projectID == "" || title == "" {
		return nil, fmt.Errorf("project_id and title are required")
	}
	path = strings.TrimSpace(path)
	if path != "" {
		if err := ValidateConventionPath(path); err != nil {
			return nil, err
		}
	}
	bp := &api.Blueprint{
		ProjectID:        projectID,
		Title:            title,
		Path:             path,
		SourceWorkflowID: strings.TrimSpace(sourceWorkflowID),
		Content:          content,
		Status:           api.BlueprintStatusDraft,
		Version:          1,
	}
	if err := m.Store.Create(ctx, bp); err != nil {
		return nil, err
	}
	return bp, nil
}

// PathForID returns the convention path of the blueprint whose id is id.
func (m *Manager) PathForID(ctx context.Context, projectID, id string) (string, error) {
	summaries, err := m.Store.List(ctx, projectID)
	if err != nil {
		return "", err
	}
	for _, s := range summaries {
		if s.ID == id {
			return s.Path, nil
		}
	}
	return "", ErrNotFound
}

// Get returns a blueprint by project-relative path.
func (m *Manager) Get(ctx context.Context, projectID, path string) (*api.Blueprint, error) {
	bp, err := m.Store.Get(ctx, projectID, path)
	if err != nil {
		return nil, err
	}
	bp.Status = api.BlueprintStatusDraft
	if m != nil && m.Approvals != nil {
		approved, err := m.Approvals.Approved(ctx, projectID, path, ContentDigest(bp.Content))
		if err != nil {
			return nil, err
		}
		if approved {
			bp.Status = api.BlueprintStatusApproved
		}
	}
	return bp, nil
}

// Update writes content and/or title. A provisional path retargets once when
// the resulting document first declares a real title; later retitles stay put.
func (m *Manager) Update(ctx context.Context, projectID, path string, content, title *string) (*api.Blueprint, error) {
	if content == nil && title == nil {
		return nil, fmt.Errorf("content or title is required")
	}
	bp, err := m.Get(ctx, projectID, path)
	if err != nil {
		return nil, err
	}
	next := bp.Content
	if content != nil {
		next = *content
		if title == nil {
			if ParseTitleFrontmatter(next) == "" {
				existing := ParseTitleFrontmatter(bp.Content)
				if existing == "" {
					existing = strings.TrimSpace(bp.Title)
				}
				if existing != "" {
					next = EnsureTitleFrontmatter(next, existing)
				}
			}
		}
	}
	if title != nil {
		t, err := NormalizeBlueprintDisplayTitle(*title)
		if err != nil {
			return nil, err
		}
		next = EnsureTitleFrontmatter(next, t)
	}
	// The grant ends before the bytes move: if the supersede fails after the
	// write, restoring the reviewed bytes re-arms an approval nobody granted.
	if m.Approvals != nil {
		if err := m.Approvals.Supersede(ctx, projectID, path); err != nil {
			return nil, err
		}
	}
	updated, err := m.Store.UpdateContent(ctx, projectID, path, next, ContentDigest(bp.Content))
	if err != nil {
		return nil, err
	}
	updated.Status = api.BlueprintStatusDraft
	if declared, ok := blueprintfile.DeclaredTitle(updated.Content); ok && IsProvisionalPath(path) {
		moved, err := m.RetargetToTitle(ctx, projectID, path, declared)
		if err != nil {
			return nil, err
		}
		if moved != path {
			updated, err = m.Get(ctx, projectID, moved)
			if err != nil {
				return nil, err
			}
			updated.Status = api.BlueprintStatusDraft
		}
	}
	return updated, nil
}

// Delete refuses active blueprints and revokes their grants before unlinking.
func (m *Manager) Delete(ctx context.Context, projectID, path string) error {
	if m.ActiveRun != nil {
		runID, active, err := m.ActiveRun(ctx, projectID, path)
		if err != nil {
			return err
		}
		if active {
			return fmt.Errorf("%w: run %s", ErrRunActive, runID)
		}
	}
	if m.Approvals != nil {
		if err := m.Approvals.Revoke(ctx, projectID, path); err != nil {
			return err
		}
	}
	if err := m.Store.Delete(ctx, projectID, path); err != nil {
		return err
	}
	return nil
}

// IsApproved reports whether a blueprint path refers to an approved file.
func (m *Manager) IsApproved(ctx context.Context, projectID, path string) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	bp, err := m.Get(ctx, projectID, path)
	if err != nil {
		return false, err
	}
	return bp.Status == api.BlueprintStatusApproved, nil
}

// List returns the newest MAX_PROJECT_BLUEPRINTS summaries for a project and
// whether older ones were cut.
func (m *Manager) List(ctx context.Context, projectID string) ([]api.BlueprintSummary, bool, error) {
	items, err := m.Store.List(ctx, projectID)
	if err != nil {
		return nil, false, err
	}
	truncated := len(items) > MAX_PROJECT_BLUEPRINTS
	if truncated {
		items = items[:MAX_PROJECT_BLUEPRINTS]
	}
	for i := range items {
		bp, getErr := m.Get(ctx, projectID, items[i].Path)
		if getErr != nil {
			return nil, false, getErr
		}
		items[i].Status = bp.Status
	}
	return items, truncated, nil
}

// Tasks extracts decomposable tasks from blueprint content.
func (m *Manager) Tasks(ctx context.Context, projectID, path string) ([]Task, error) {
	bp, err := m.Store.Get(ctx, projectID, path)
	if err != nil {
		return nil, err
	}
	return ExtractTasks(bp.Content), nil
}
