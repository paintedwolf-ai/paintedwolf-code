// Package project manages chat-first project workspaces for the local sidecar.
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/pkg/api"
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
	// ErrRootRefused marks paths that cannot become write roots.
	ErrRootRefused = errors.New("project root refused")
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

func (*MemoryRegistry) MutationEventsOutboxed() bool { return false }

func PrimaryRootPath(p *Project) string {
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

// FindByRootPath returns the most recently opened project containing a resolved root.
func FindByRootPath(list []Project, resolved string) *Project {
	resolved = strings.TrimSpace(resolved)
	if resolved == "" {
		return nil
	}
	for i := range list {
		for _, r := range list[i].Roots {
			if r.Path == resolved {
				return &list[i]
			}
		}
	}
	return nil
}

// MemoryRegistry is an in-memory project registry for tests.
type MemoryRegistry struct {
	mu         sync.RWMutex
	projects   map[string]*Project
	promotions map[string]*Promotion
}

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		projects:   make(map[string]*Project),
		promotions: make(map[string]*Promotion),
	}
}

func (r *MemoryRegistry) Create(ctx context.Context, params CreateParams) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if params.Draft && len(params.Roots) > 0 {
		return nil, ErrDraftRootImmutable
	}
	now := time.Now().UTC()
	p := &Project{
		ID:              uuid.NewString(),
		Name:            DefaultNameForCreate(params),
		Roots:           nil,
		RootsGeneration: 0,
		IsDraft:         params.Draft,
		LastOpenedAt:    now,
		CreatedAt:       now,
	}
	for _, rootIn := range params.Roots {
		if _, err := r.attachRootLocked(ctx, p, rootIn, RootKindAttached); err != nil {
			return nil, err
		}
	}
	if params.Draft && len(params.Roots) == 0 {
		if err := ensureDraftScratchRoot(ctx, p.ID, func(in AttachRootParams) (Root, error) {
			return r.attachRootLocked(ctx, p, in, RootKindDraft)
		}); err != nil {
			return nil, err
		}
	}
	r.mu.Lock()
	r.projects[p.ID] = cloneProject(p)
	r.mu.Unlock()
	return r.Get(ctx, p.ID)
}

func (r *MemoryRegistry) Get(ctx context.Context, id string) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	out := cloneProject(p)
	return out, ValidateLifecycle(out)
}

func (r *MemoryRegistry) List(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Project, 0, len(r.projects))
	for _, p := range r.projects {
		cloned := cloneProject(p)
		if err := ValidateLifecycle(cloned); err != nil {
			return nil, err
		}
		out = append(out, *cloned)
	}
	sortProjectsRecentFirst(out)
	return out, nil
}

func (r *MemoryRegistry) UpdateNameIfUnset(ctx context.Context, id, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, nil
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return false, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if strings.TrimSpace(p.Name) != "" {
		return false, nil
	}
	p.Name = name
	return true, nil
}

func (r *MemoryRegistry) Patch(ctx context.Context, id string, params PatchParams) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var name *string
	if params.Name != nil {
		trimmed := strings.TrimSpace(*params.Name)
		if trimmed == "" {
			return nil, ErrInvalidName
		}
		name = &trimmed
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if name != nil {
		p.Name = *name
	}
	if params.Starred != nil {
		p.Starred = *params.Starred
	}
	return cloneProject(p), nil
}

func (r *MemoryRegistry) TouchLastOpened(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	p.LastOpenedAt = time.Now().UTC()
	return nil
}

func (r *MemoryRegistry) AttachRoot(ctx context.Context, id string, params AttachRootParams) (*RootChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if p.IsDraft {
		return nil, ErrDraftRootImmutable
	}
	before := cloneProject(p)
	added, err := r.attachRootLocked(ctx, p, params, RootKindAttached)
	if err != nil {
		return nil, err
	}
	p.RootsGeneration++
	after := cloneProject(p)
	return &RootChange{Before: before, After: after, Added: &added, Changed: true, RootContextChanged: true}, nil
}

func (r *MemoryRegistry) DetachRoot(ctx context.Context, id, rootID string) (*RootChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if p.IsDraft {
		return nil, ErrDraftRootImmutable
	}
	idx := -1
	for i, root := range p.Roots {
		if root.ID == rootID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%w: %s", ErrRootNotFound, rootID)
	}
	before := cloneProject(p)
	removed := p.Roots[idx]
	wasPrimary := removed.IsPrimary
	p.Roots = append(p.Roots[:idx], p.Roots[idx+1:]...)
	if wasPrimary {
		promoteEarliestPrimary(p.Roots)
	}
	p.RootsGeneration++
	after := cloneProject(p)
	return &RootChange{Before: before, After: after, Removed: &removed, Changed: true, RootContextChanged: true}, nil
}

func (r *MemoryRegistry) PatchRoot(ctx context.Context, id, rootID string, params PatchRootParams) (*RootChange, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if p.IsDraft {
		return nil, ErrDraftRootImmutable
	}
	idx := -1
	for i, root := range p.Roots {
		if root.ID == rootID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%w: %s", ErrRootNotFound, rootID)
	}
	before := cloneProject(p)
	changed := false
	rootContextChanged := false
	if params.Label != nil {
		label, err := NormalizeRootDisplayLabel(*params.Label)
		if err != nil {
			return nil, err
		}
		if rootLabelTaken(p.Roots, label, rootID) {
			return nil, fmt.Errorf("%w: %s", ErrDuplicateRootLabel, label)
		}
		if p.Roots[idx].Label != label {
			p.Roots[idx].Label = label
			changed = true
		}
	}
	if params.IsPrimary != nil && *params.IsPrimary {
		if !p.Roots[idx].IsPrimary {
			for i := range p.Roots {
				p.Roots[i].IsPrimary = i == idx
			}
			changed = true
			rootContextChanged = true
		}
	}
	if !changed {
		return &RootChange{Before: before, After: cloneProject(p)}, nil
	}
	if rootContextChanged {
		p.RootsGeneration++
	}
	return &RootChange{
		Before: before, After: cloneProject(p), Changed: true, RootContextChanged: rootContextChanged,
	}, nil
}

func (r *MemoryRegistry) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.projects[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(r.projects, id)
	delete(r.promotions, id)
	return nil
}

func (r *MemoryRegistry) SetCover(ctx context.Context, id, artifactID, rootSessionID, source string, updatedAt time.Time) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	p.CoverArtifactID = strings.TrimSpace(artifactID)
	p.CoverRootSessionID = strings.TrimSpace(rootSessionID)
	p.CoverSource = strings.TrimSpace(source)
	t := updatedAt.UTC()
	p.CoverUpdatedAt = &t
	return cloneProject(p), nil
}

func (r *MemoryRegistry) MarkTrustSeen(ctx context.Context, id string, expected *Project, seen map[string]SeenRecord) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if expected == nil || p.RootsGeneration != expected.RootsGeneration || !sameSeenRecords(p.TrustSeen, expected.TrustSeen) {
		return nil, ErrTrustReviewChanged
	}
	p.TrustSeen = cloneTrustReadBaseline(seen)
	return cloneProject(p), nil
}

func (r *MemoryRegistry) SetTrustEnabled(ctx context.Context, id string, updates map[string]bool) (*Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	p.TrustEnabled = MergeTrustEnabled(p.TrustEnabled, updates)
	return cloneProject(p), nil
}

func (r *MemoryRegistry) attachRootLocked(ctx context.Context, p *Project, params AttachRootParams, kind RootKind) (Root, error) {
	abs, err := ResolveExistingDir(params.Path)
	if err != nil {
		return Root{}, err
	}
	if err := defaultOpenPolicy.ValidateOpenPath(abs); err != nil {
		return Root{}, err
	}
	for _, existing := range p.Roots {
		if existing.Path == abs {
			return Root{}, fmt.Errorf("%w: %s", ErrDuplicateRoot, abs)
		}
	}
	wantPrimary := len(p.Roots) == 0 || params.IsPrimary != nil && *params.IsPrimary
	if wantPrimary {
		for i := range p.Roots {
			p.Roots[i].IsPrimary = false
		}
	}
	now := time.Now().UTC()
	label := strings.TrimSpace(params.Label)
	if label == "" {
		existing := make([]string, 0, len(p.Roots))
		for _, r := range p.Roots {
			existing = append(existing, r.Label)
		}
		label = deriveUniqueRootLabel(existing, abs)
	} else {
		normalized, err := NormalizeRootDisplayLabel(label)
		if err != nil {
			return Root{}, err
		}
		if rootLabelTaken(p.Roots, normalized, "") {
			return Root{}, fmt.Errorf("%w: %s", ErrDuplicateRootLabel, normalized)
		}
		label = normalized
	}
	root := Root{
		ID:            uuid.NewString(),
		ProjectID:     p.ID,
		Path:          abs,
		Label:         label,
		IsPrimary:     wantPrimary,
		GitRemoteHash: gitRemoteHash(ctx, abs),
		AddedAt:       now,
		Kind:          kind,
	}
	p.Roots = append(p.Roots, root)
	return root, nil
}

func sortProjectsRecentFirst(projects []Project) {
	for i := 1; i < len(projects); i++ {
		j := i
		for j > 0 && projects[j].LastOpenedAt.After(projects[j-1].LastOpenedAt) {
			projects[j], projects[j-1] = projects[j-1], projects[j]
			j--
		}
	}
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

func promoteEarliestPrimary(roots []Root) {
	if len(roots) == 0 {
		return
	}
	best := 0
	for i := range roots {
		roots[i].IsPrimary = false
		if roots[i].AddedAt.Before(roots[best].AddedAt) {
			best = i
		}
	}
	roots[best].IsPrimary = true
}

// CreateWithRoot creates a project with one primary root.
func CreateWithRoot(ctx context.Context, reg Registry, path string) (*Project, error) {
	return reg.Create(ctx, CreateParams{
		Roots: []AttachRootParams{{Path: path}},
	})
}

func ToAPI(p *Project) api.Project {
	if p == nil {
		return api.Project{}
	}
	out := api.Project{
		ID:              p.ID,
		Roots:           make([]api.ProjectRoot, 0, len(p.Roots)),
		RootsGeneration: p.RootsGeneration,
		SessionCount:    p.SessionCount,
		Starred:         p.Starred,
		IsDraft:         p.IsDraft,
		LastOpenedAt:    p.LastOpenedAt,
		CreatedAt:       p.CreatedAt,
	}
	if p.Promotion != nil {
		out.Promotion = &api.ProjectPromotion{
			DestinationPath: p.Promotion.DestinationPath,
			InitGit:         p.Promotion.InitGit,
			Phase:           string(p.Promotion.Phase),
			LastError:       p.Promotion.LastError,
			UpdatedAt:       p.Promotion.UpdatedAt,
		}
	}
	if name := strings.TrimSpace(p.Name); name != "" {
		out.Name = &name
	}
	if p.LastActivityAt != nil {
		t := *p.LastActivityAt
		out.LastActivityAt = &t
	}
	if id := strings.TrimSpace(p.CoverArtifactID); id != "" {
		out.CoverArtifactID = &id
		if root := strings.TrimSpace(p.CoverRootSessionID); root != "" {
			out.CoverRootSessionID = &root
		}
		if src := strings.TrimSpace(p.CoverSource); src != "" {
			out.CoverSource = &src
		}
		if p.CoverUpdatedAt != nil {
			t := *p.CoverUpdatedAt
			out.CoverUpdatedAt = &t
		}
	}
	for _, r := range p.Roots {
		wr := api.ProjectRoot{
			ID:        r.ID,
			Path:      r.Path,
			Label:     r.Label,
			IsPrimary: r.IsPrimary,
			AddedAt:   r.AddedAt,
			Kind:      string(r.Kind),
		}
		if hash := strings.TrimSpace(r.GitRemoteHash); hash != "" {
			wr.GitRemoteHash = &hash
		}
		out.Roots = append(out.Roots, wr)
	}
	return out
}

// ResolveExistingDir returns the absolute path when path exists and is a directory.
func ResolveExistingDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ErrInvalidPath
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPath, err)
	}
	abs = filepath.Clean(abs)

	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrPathNotFound, abs)
		}
		return "", err
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%w: %s", ErrPathNotFound, resolved)
		}
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrNotDirectory, resolved)
	}
	if refused, code := confine.AttachedWriteRootRefused(resolved); refused {
		return "", &RootRefusedError{Code: code, Path: resolved}
	}
	return resolved, nil
}

// RootRefusedError reports a refused attached root.
type RootRefusedError struct {
	Code string
	Path string
}

func (e *RootRefusedError) Error() string {
	if e == nil {
		return ErrRootRefused.Error()
	}
	if e.Code == "" {
		return fmt.Sprintf("%s: %s", ErrRootRefused.Error(), e.Path)
	}
	return fmt.Sprintf("%s: %s", ErrRootRefused.Error(), e.Code)
}

func (e *RootRefusedError) Unwrap() error { return ErrRootRefused }

// RootRefusedCode extracts the machine code from an ErrRootRefused chain.
func RootRefusedCode(err error) (string, bool) {
	var refused *RootRefusedError
	if errors.As(err, &refused) && refused.Code != "" {
		return refused.Code, true
	}
	return "", false
}

const gitRemoteHashTimeout = 3 * time.Second

// gitRemoteHash identifies origin's repository under a bounded timeout, so ssh and https
// spellings of one repository hash alike.
func gitRemoteHash(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, gitRemoteHashTimeout)
	defer cancel()
	out, code, err := gitexec.Run(ctx, dir, []string{"remote", "get-url", "origin"}, gitexec.Opts{
		Profile: gitexec.ProfileHermetic,
		Timeout: gitRemoteHashTimeout,
	})
	if err != nil || code != 0 {
		return ""
	}
	url := strings.TrimSpace(string(out))
	if repo := SourceRemoteRepo(url); repo != "" {
		url = repo
	}
	if url == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

func (r *MemoryRegistry) ReadTrustBaseline(ctx context.Context, id string) (map[string]SeenRecord, error) {
	p, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return p.TrustSeen, nil
}
