// Package adaptertest provides configured adapters backed by real layered filesystem state.
package adaptertest

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
)

type MountedConfig struct {
	AdapterID                  adapter.AdapterID
	TargetOS                   string
	InvalidationVerified       bool
	NegativeLookupInvalidation bool
	DirCookieInvalidation      bool
	AttributionCompleteness    string
	MountProbeFn               func(context.Context, string, string) (adapter.ReasonCode, string, error)
	VerifyBackingDenialFn      func(context.Context, string) (bool, error)
	MountViewFn                func(context.Context, string, []adapter.PreparedRoot) error
	UnmountViewFn              func(context.Context, string, string) error
	FlushClientFn              func(context.Context, string) error
}

// Mounted exposes Core-backed directories without a kernel mount.
type Mounted struct {
	cfg   MountedConfig
	facts adapter.AdapterFacts
	mu    sync.RWMutex
	views map[string]*mountedView
}

type mountedView struct {
	view           *adapter.PreparedView
	deny           []string
	quiesce, flush func(context.Context, string) error
	access         atomic.Int64
}

func NewMounted(cfg MountedConfig) *Mounted {
	completeness := cfg.AttributionCompleteness
	if completeness == "" {
		completeness = "kernel_cached_partial"
	}
	return &Mounted{cfg: cfg, views: make(map[string]*mountedView), facts: adapter.AdapterFacts{
		AdapterID: cfg.AdapterID, PreparationKind: adapter.PreparationMounted,
		UntouchedSourceVisibility: adapter.VisibilityLive,
		CacheRevalidation: adapter.CacheRevalidation{
			Mode: "strict_generation_revalidation", ExplicitInvalidation: true,
			InvalidationVerified: cfg.InvalidationVerified, NegativeLookupInvalidation: cfg.NegativeLookupInvalidation,
			DirCookieInvalidation: cfg.DirCookieInvalidation,
		},
		AttributionCompleteness: completeness,
		MetadataCapabilities: adapter.MetadataCapabilities{
			SupportsSymlinks: true, SupportsHardLinks: true, SupportsXattrs: true, SupportsChmod: true,
			SupportsTimestamps: true, SupportsPOSIXLocks: true, SupportsFlock: true, SupportsFIFOOpen: true,
		},
		SupportsPrivateGit: true, SupportsCheckpointBarrier: true,
		BarrierHoldBound: layeredfs.DefaultBarrierHoldBound, OtherUserAccess: adapter.OtherUserAccessNone,
	}}
}

func (a *Mounted) ID() adapter.AdapterID       { return a.cfg.AdapterID }
func (a *Mounted) Facts() adapter.AdapterFacts { return a.facts }

// ZeroBudget forces selection to evaluate the mounted adapter.
func ZeroBudget(class adapter.BudgetClass) adapter.BudgetFact {
	return adapter.BudgetFact{Class: class, Limit: -1, EntryCost: 1}
}
