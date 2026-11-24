// Package worktrees maintains verified repository checkout inventory and incarnation identity.
package worktrees

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/git"
	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/timelayout"
)

var (
	ErrCheckoutNotFound    = errors.New("checkout registration not found")
	ErrCheckoutRetired     = errors.New("checkout registration retired")
	ErrCheckoutUnavailable = errors.New("checkout directory unavailable")
)

// Checkout is one durable checkout registration incarnation for a project repository.
type Checkout struct {
	ID               string
	ProjectID        string
	RepoID           string
	Toplevel         string
	WorktreePath     string
	Branch           string
	BaseBranch       string
	State            string
	Kind             gitrepo.CheckoutKind
	Available        bool
	Managed          bool
	CommonDir        string
	AdminDir         string
	CommonIdentity   string
	AdminIdentity    string
	WorktreeIdentity string
	HeadSHA          string
	Detached         bool
	Locked           bool
	LockReason       string
	Prunable         bool
	PruneReason      string
	SupersededByID   string
	CreatedAt        time.Time
	UpdatedAt        time.Time
	RetiredAt        time.Time
}

// IsProjectCheckout reports whether this checkout is the repository's attached project directory.
func (c Checkout) IsProjectCheckout() bool {
	return canonicalPath(c.WorktreePath) != "" && canonicalPath(c.WorktreePath) == canonicalPath(c.Toplevel)
}

// SourceBranch returns the durable source-ledger and editor branch identity for this checkout.
func (c Checkout) SourceBranch() sourcebranch.ID {
	if c.IsProjectCheckout() {
		return sourcebranch.Trunk
	}
	return sourcebranch.ForWorktree(c.ID)
}

// RepoSpec identifies a project repository for inventory reconciliation.
type RepoSpec struct {
	ProjectID string
	RepoID    string
	Toplevel  string
}

// MoveReceipt records a host-verified worktree move.
type MoveReceipt struct {
	ProjectID string
	RepoID    string
	ID        string
	FromPath  string
	ToPath    string
}

// Lister lists porcelain worktree entries for a repository.
type Lister interface {
	ListWorktrees(ctx context.Context, toplevel string) ([]git.WorktreeEntry, error)
}

// Inspector classifies one porcelain worktree entry against the repository's common directory.
type Inspector func(commonDir, listedPath string) gitrepo.RegisteredCheckout

// Options configures an Inventory service.
type Options struct {
	Git       Lister
	Inspector Inspector
	Now       func() time.Time
}

// Inventory coordinates coalesced repository checkout discovery and incarnation persistence.
type Inventory struct {
	database  db.Handle
	git       Lister
	inspector Inspector
	now       func() time.Time

	mu         sync.Mutex
	slots      map[repoKey]*repoSlot
	witnesses  map[repoKey]map[string]liveWitness
	moves      map[repoKey][]MoveReceipt
}

type liveWitness struct {
	worktreePath     string
	adminDir         string
	adminIdentity    string
	worktreeIdentity string
	available        bool
}

// MarkManaged records that a checkout registration was created by app-managed lifecycle controls.
func (inv *Inventory) MarkManaged(ctx context.Context, checkoutID, baseBranch string) error {
	if inv == nil || inv.database == nil {
		return nil
	}
	checkoutID = strings.TrimSpace(checkoutID)
	if checkoutID == "" {
		return nil
	}
	_, err := inv.database.ExecContext(ctx,
		`UPDATE source_worktrees SET managed = 1, base_branch = CASE WHEN ? != '' THEN ? ELSE base_branch END WHERE id = ?`,
		strings.TrimSpace(baseBranch), strings.TrimSpace(baseBranch), checkoutID)
	return err
}

// New constructs an Inventory backed by database.
func New(database db.Handle, opts Options) *Inventory {
	gitLister := opts.Git
	if gitLister == nil {
		gitLister = git.NewManager()
	}
	inspector := opts.Inspector
	if inspector == nil {
		inspector = gitrepo.InspectListedCheckout
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	return &Inventory{
		database:  database,
		git:       gitLister,
		inspector: inspector,
		now:       nowFn,
		slots:     make(map[repoKey]*repoSlot),
		witnesses: make(map[repoKey]map[string]liveWitness),
		moves:     make(map[repoKey][]MoveReceipt),
	}
}

// RecordVerifiedMove registers a verified move receipt for the next reconciliation.
func (inv *Inventory) RecordVerifiedMove(receipt MoveReceipt) {
	key := repoKey{
		projectID: strings.TrimSpace(receipt.ProjectID),
		repoID:    strings.TrimSpace(receipt.RepoID),
	}
	if key.projectID == "" || key.repoID == "" || strings.TrimSpace(receipt.ID) == "" {
		return
	}
	receipt.FromPath = canonicalPath(receipt.FromPath)
	receipt.ToPath = canonicalPath(receipt.ToPath)
	inv.mu.Lock()
	inv.moves[key] = append(inv.moves[key], receipt)
	slot := inv.slotLocked(key)
	slot.mu.Lock()
	slot.desiredGen++
	slot.mu.Unlock()
	inv.mu.Unlock()
}

// Get returns a stored checkout by ID without triggering a Git refresh.
func (inv *Inventory) Get(ctx context.Context, projectID, checkoutID string) (Checkout, error) {
	row, err := db.New(inv.database).GetSourceWorktreeByID(ctx, db.GetSourceWorktreeByIDParams{
		ProjectID: strings.TrimSpace(projectID),
		ID:        strings.TrimSpace(checkoutID),
	})
	if err != nil {
		if db.IsNoRows(err) {
			return Checkout{}, ErrCheckoutNotFound
		}
		return Checkout{}, err
	}
	return checkoutFromRow(row), nil
}

// ListStored returns currently active stored checkouts for a repository without refreshing Git.
func (inv *Inventory) ListStored(ctx context.Context, projectID, repoID string) ([]Checkout, error) {
	rows, err := db.New(inv.database).ListActiveSourceWorktreesByRepo(ctx, db.ListActiveSourceWorktreesByRepoParams{
		ProjectID: strings.TrimSpace(projectID),
		RepoID:    strings.TrimSpace(repoID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Checkout, 0, len(rows))
	for _, row := range rows {
		out = append(out, checkoutFromRow(row))
	}
	return out, nil
}

// ListSuperseded returns retired incarnations replaced by checkoutID.
func (inv *Inventory) ListSuperseded(ctx context.Context, projectID, repoID, checkoutID string) ([]Checkout, error) {
	rows, err := db.New(inv.database).ListSupersededSourceWorktrees(ctx, db.ListSupersededSourceWorktreesParams{
		ProjectID:      strings.TrimSpace(projectID),
		RepoID:         strings.TrimSpace(repoID),
		SupersededByID: strings.TrimSpace(checkoutID),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Checkout, 0, len(rows))
	for _, row := range rows {
		out = append(out, checkoutFromRow(row))
	}
	return out, nil
}

// VerifyLive loads checkoutID and verifies its live registration and incarnation continuity on disk.
func (inv *Inventory) VerifyLive(ctx context.Context, projectID, checkoutID string) (Checkout, error) {
	if inv == nil || inv.database == nil {
		return Checkout{}, ErrCheckoutNotFound
	}
	return VerifyLiveCheckout(ctx, inv.database, projectID, checkoutID)
}

// IsBoundToAnySession reports whether any session_worktrees binding targets checkoutID.
func (inv *Inventory) IsBoundToAnySession(ctx context.Context, projectID, checkoutID string) (bool, error) {
	if inv == nil || inv.database == nil {
		return false, nil
	}
	n, err := db.New(inv.database).CountSessionWorktreesByWorktreeID(ctx, db.CountSessionWorktreesByWorktreeIDParams{
		ProjectID:  strings.TrimSpace(projectID),
		WorktreeID: strings.TrimSpace(checkoutID),
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// VerifyLiveCheckout loads checkoutID and verifies its live registration and incarnation continuity on disk.
func VerifyLiveCheckout(ctx context.Context, database db.Handle, projectID, checkoutID string) (Checkout, error) {
	row, err := db.New(database).GetSourceWorktreeByID(ctx, db.GetSourceWorktreeByIDParams{
		ProjectID: strings.TrimSpace(projectID),
		ID:        strings.TrimSpace(checkoutID),
	})
	if err != nil {
		if db.IsNoRows(err) {
			return Checkout{}, ErrCheckoutNotFound
		}
		return Checkout{}, err
	}
	c := checkoutFromRow(row)
	if c.State != "active" {
		return c, ErrCheckoutRetired
	}
	if !c.Available {
		return c, ErrCheckoutUnavailable
	}
	commonDir := c.CommonDir
	if commonDir == "" {
		var ok bool
		commonDir, _, _, ok = gitrepo.CommonRepository(c.Toplevel)
		if !ok {
			return c, ErrCheckoutUnavailable
		}
	}
	verified, ok := gitrepo.VerifyRegisteredCheckout(commonDir, c.WorktreePath)
	if !ok {
		return c, ErrCheckoutUnavailable
	}
	// If registration evidence was recorded, verify incarnation continuity before serving reads or saves.
	if c.AdminDir != "" && verified.AdminDir != canonicalPath(c.AdminDir) {
		return c, ErrCheckoutRetired
	}
	if c.AdminIdentity != "" && !matchesLiveIdentity(c.AdminIdentity, verified.AdminIdentity) {
		return c, ErrCheckoutRetired
	}
	if c.WorktreeIdentity != "" && !matchesLiveIdentity(c.WorktreeIdentity, verified.WorktreeIdentity) {
		return c, ErrCheckoutRetired
	}
	return c, nil
}

func matchesLiveIdentity(recorded, current string) bool {
	if fspath.HasCreationDiscriminator(recorded) || fspath.HasCreationDiscriminator(current) {
		return fspath.SameIncarnation(recorded, current)
	}
	return recorded == current
}

func checkoutFromRow(row db.SourceWorktrees) Checkout {
	createdAt, _ := db.ParseTime(row.CreatedAt)
	updatedAt, _ := db.ParseTime(row.UpdatedAt)
	retiredAt, _ := db.ParseTime(row.RetiredAt)
	kind := gitrepo.CheckoutLinked
	if row.Kind == string(gitrepo.CheckoutPrimary) {
		kind = gitrepo.CheckoutPrimary
	}
	return Checkout{
		ID:               row.ID,
		ProjectID:        row.ProjectID,
		RepoID:           row.RepoID,
		Toplevel:         row.Toplevel,
		WorktreePath:     row.WorktreePath,
		Branch:           row.Branch,
		BaseBranch:       row.BaseBranch,
		State:            row.State,
		Kind:             kind,
		Available:        row.Available != 0,
		Managed:          row.Managed != 0,
		CommonDir:        row.CommonDir,
		AdminDir:         row.AdminDir,
		CommonIdentity:   row.CommonIdentity,
		AdminIdentity:    row.AdminIdentity,
		WorktreeIdentity: row.WorktreeIdentity,
		HeadSHA:          row.HeadSha,
		Detached:         row.Detached != 0,
		Locked:           row.Locked != 0,
		LockReason:       row.LockReason,
		Prunable:         row.Prunable != 0,
		PruneReason:      row.PruneReason,
		SupersededByID:   row.SupersededByID,
		CreatedAt:        createdAt,
		UpdatedAt:        updatedAt,
		RetiredAt:        retiredAt,
	}
}

func canonicalPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	if canon := fspath.CanonicalPath(abs); canon != "" {
		return filepath.FromSlash(canon)
	}
	return abs
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return timelayout.Format(t.UTC())
}

func boolInt(v bool) int64 {
	if v {
		return 1
	}
	return 0
}
