// Package project manages chat-first project workspaces for the local sidecar.
package project

import (
	"context"
	"errors"
	"time"

	"github.com/lycaon/lycaon/internal/sourcebranch"
)

var (
	ErrNotFound           = errors.New("project not found")
	ErrRootNotFound       = errors.New("project root not found")
	ErrInvalidPath        = errors.New("invalid project path")
	ErrPathNotFound       = errors.New("project path does not exist")
	ErrNotDirectory       = errors.New("project path is not a directory")
	ErrDuplicateRoot      = errors.New("duplicate project root path")
	ErrInvalidName        = errors.New("invalid project name")
	ErrDraftRootImmutable = errors.New("draft workspace root is immutable")
	ErrRootBusy           = errors.New("project root has active dependents")
	ErrProjectBusy        = errors.New("project has active work")
)

// RootKind distinguishes the engine-managed draft workspace from attached folders.
type RootKind string

const (
	RootKindAttached RootKind = "attached"
	RootKindDraft    RootKind = "draft"
)

// Root is a folder attached to a project workspace.
type Root struct {
	ID            string
	ProjectID     string
	Path          string
	Label         string
	IsPrimary     bool
	GitRemoteHash string
	AddedAt       time.Time
	Kind          RootKind
}

// RootChange is the atomic result of one root-membership transition.
type RootChange struct {
	Before             *Project
	After              *Project
	Added              *Root
	Removed            *Root
	Changed            bool
	RootContextChanged bool
}

// PromotionPhase is the durable position of a draft-to-folder transition.
type PromotionPhase string

const (
	PromotionQueued    PromotionPhase = "queued"
	PromotionStaged    PromotionPhase = "staged"
	PromotionInstalled PromotionPhase = "installed"
	PromotionCommitted PromotionPhase = "committed"
)

// Promotion is a restart-safe draft-to-folder transition intent.
type Promotion struct {
	ProjectID       string
	RootID          string
	SourcePath      string
	DestinationPath string
	StagePath       string
	ReservationPath string
	InitGit         bool
	Phase           PromotionPhase
	SourceSHA256    string
	ManifestSHA256  string
	LastError       string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Project is a materialized chat workspace.
type Project struct {
	SourceBranch    sourcebranch.ID
	RootBranches    map[string]sourcebranch.ID
	ID              string
	Name            string // empty when unset (wire null)
	Roots           []Root
	RootsGeneration int
	// TrustEnabled is surface id → enabled. Missing key means on.
	TrustEnabled map[string]bool
	// TrustSeen holds read summaries; file bytes live in the trust baseline store.
	TrustSeen          map[string]SeenRecord
	SessionCount       int
	Starred            bool
	IsDraft            bool
	Promotion          *Promotion
	CoverArtifactID    string
	CoverRootSessionID string
	CoverSource        string
	CoverUpdatedAt     *time.Time
	LastActivityAt     *time.Time
	LastOpenedAt       time.Time
	CreatedAt          time.Time
}

// CreateParams materializes a project on first user submit.
type CreateParams struct {
	Name  string
	Roots []AttachRootParams
	// Draft marks the project as an unsaved draft until the user promotes it.
	Draft bool
}

// AttachRootParams attaches one folder root.
type AttachRootParams struct {
	Path      string
	Label     string
	IsPrimary *bool
}

// PatchRootParams updates an attached folder root.
type PatchRootParams struct {
	IsPrimary *bool
	Label     *string
}

// PatchParams applies one project metadata mutation set atomically.
type PatchParams struct {
	Name    *string
	Starred *bool
}

// Registry tracks persisted project workspaces.
type Registry interface {
	MutationEventsOutboxed() bool
	Create(ctx context.Context, params CreateParams) (*Project, error)
	Get(ctx context.Context, id string) (*Project, error)
	List(ctx context.Context) ([]Project, error)
	Patch(ctx context.Context, id string, params PatchParams) (*Project, error)
	UpdateNameIfUnset(ctx context.Context, id, name string) (bool, error)
	CreatePromotion(ctx context.Context, projectID, destPath string, initGit bool) (*Promotion, error)
	GetPromotion(ctx context.Context, projectID string) (*Promotion, error)
	ListPromotions(ctx context.Context) ([]Promotion, error)
	AdvancePromotion(ctx context.Context, projectID string, from, to PromotionPhase, sourceSHA256, manifestSHA256, lastError string) (*Promotion, error)
	CommitPromotion(ctx context.Context, projectID string) (*Project, error)
	DeletePromotion(ctx context.Context, projectID string) error
	CancelPromotion(ctx context.Context, projectID string) (*Project, error)
	TouchLastOpened(ctx context.Context, id string) error
	AttachRoot(ctx context.Context, id string, params AttachRootParams) (*RootChange, error)
	DetachRoot(ctx context.Context, id, rootID string) (*RootChange, error)
	PatchRoot(ctx context.Context, id, rootID string, params PatchRootParams) (*RootChange, error)
	Delete(ctx context.Context, id string) error
	// SetCover binds a rendered artifact to the project card.
	SetCover(ctx context.Context, id, artifactID, rootSessionID, source string, updatedAt time.Time) (*Project, error)
	ReadTrustBaseline(ctx context.Context, id string) (map[string]SeenRecord, error)
	// MarkTrustSeen replaces the complete read baseline if its captured state is still current.
	MarkTrustSeen(ctx context.Context, id string, expected *Project, seen map[string]SeenRecord) (*Project, error)
	// SetTrustEnabled merges partial surface id → enabled into the project map.
	SetTrustEnabled(ctx context.Context, id string, updates map[string]bool) (*Project, error)
}

// AttachedPrimaryPath returns the primary root path as the person attached it,
// whether or not agents work there.
func AttachedPrimaryPath(p *Project) string {
	if p == nil {
		return ""
	}
	for _, r := range p.Roots {
		if r.IsPrimary {
			return r.Path
		}
	}
	return ""
}

// PrimaryRootPath returns the agents' primary root path.
func PrimaryRootPath(p *Project) string {
	if roots := AdmittedRoots(p); len(roots) > 0 && roots[0].IsPrimary {
		return roots[0].Path
	}
	return ""
}

// FindByRootPath returns the most recently opened project containing a resolved root,
// preferring single-root matches over multi-root matches.
func FindByRootPath(list []Project, resolved string) *Project {
	matches := FindAllByRootPath(list, resolved)
	if len(matches) == 0 {
		return nil
	}
	for i := range list {
		if list[i].ID == matches[0].ID {
			return &list[i]
		}
	}
	return &matches[0]
}

func cloneProject(p *Project) *Project {
	if p == nil {
		return nil
	}
	out := *p
	if len(p.Roots) > 0 {
		out.Roots = append([]Root(nil), p.Roots...)
	}
	if len(p.TrustEnabled) > 0 {
		out.TrustEnabled = make(map[string]bool, len(p.TrustEnabled))
		for k, v := range p.TrustEnabled {
			out.TrustEnabled[k] = v
		}
	} else {
		out.TrustEnabled = nil
	}
	if len(p.TrustSeen) > 0 {
		out.TrustSeen = make(map[string]SeenRecord, len(p.TrustSeen))
		for k, v := range p.TrustSeen {
			out.TrustSeen[k] = cloneSeenRecord(v)
		}
	} else {
		out.TrustSeen = nil
	}
	if p.Promotion != nil {
		promotion := *p.Promotion
		out.Promotion = &promotion
	}
	return &out
}
