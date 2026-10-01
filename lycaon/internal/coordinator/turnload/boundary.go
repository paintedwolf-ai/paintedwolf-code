package turnload

import "time"

// CacheState controls standing-prefix reuse from host facts, not confirmed hits.
type CacheState string

const (
	// CacheWarm means the prefix is probably still cached.
	CacheWarm CacheState = "warm"
	// CacheCold means recorded facts permit re-deciding the prefix.
	CacheCold CacheState = "cold"
)

// ColdReason is the host fact that made a boundary cold.
type ColdReason string

const (
	// ColdFirstTurn is the chat's first decided turn.
	ColdFirstTurn ColdReason = "first_turn"
	// ColdUncached is a route whose provider keeps nothing between requests.
	ColdUncached ColdReason = "uncached"
	// ColdIdle is an idle gap at least as long as the prefix's lifetime.
	ColdIdle ColdReason = "idle"
	// ColdModelChanged is a turn routed to a different provider or model than
	// the chat's last call.
	ColdModelChanged ColdReason = "model_changed"
	// ColdNotResident is a local runner that no longer holds the model.
	ColdNotResident ColdReason = "not_resident"
)

// Boundary is what the host knew about the provider cache when a turn
// opened.
type Boundary struct {
	Cache  CacheState `json:"cache"`
	Reason ColdReason `json:"reason,omitempty"`
	// IdleMS is the gap since the chat's last model call started, when one
	// exists.
	IdleMS int64 `json:"idle_ms,omitempty"`
	// ColdAfterMS is how long the route keeps an idle prefix; zero when the
	// provider documents no figure.
	ColdAfterMS int64 `json:"cold_after_ms,omitempty"`
}

// Cold reports whether the turn may re-decide its standing surface.
func (b Boundary) Cold() bool { return b.Cache != CacheWarm }

// BoundaryFacts are the structured inputs a boundary is read from.
type BoundaryFacts struct {
	// FirstTurn is true when the chat has no earlier decided turn.
	FirstTurn bool
	// Caches is true when the route's provider keeps prefixes between
	// requests.
	Caches bool
	// PolicyKnown distinguishes an uncached route from unavailable policy.
	PolicyKnown bool
	// LastCall reports whether the chat made a model call before, and on
	// which route and when.
	LastCall                 bool
	LastProvider, LastModel  string
	LastStarted              time.Time
	NextProvider, NextModel  string
	Now                      time.Time
	ColdAfter                time.Duration
	ResidencyKnown, Resident bool
}

// ReadBoundary applies the facts in a fixed order: a cause that makes the
// cache certainly cold outranks one that makes it probably cold.
func ReadBoundary(f BoundaryFacts) Boundary {
	out := Boundary{Cache: CacheCold, ColdAfterMS: f.ColdAfter.Milliseconds()}
	if f.LastCall && !f.LastStarted.IsZero() && f.Now.After(f.LastStarted) {
		out.IdleMS = f.Now.Sub(f.LastStarted).Milliseconds()
	}
	switch {
	case f.FirstTurn:
		out.Reason = ColdFirstTurn
	case f.PolicyKnown && !f.Caches:
		out.Reason = ColdUncached
	case f.LastCall && f.NextProvider != "" && f.NextModel != "" && (f.LastProvider != f.NextProvider || f.LastModel != f.NextModel):
		out.Reason = ColdModelChanged
	case f.ResidencyKnown && !f.Resident:
		out.Reason = ColdNotResident
	case f.LastCall && f.ColdAfter > 0 && time.Duration(out.IdleMS)*time.Millisecond >= f.ColdAfter:
		out.Reason = ColdIdle
	default:
		out.Cache = CacheWarm
	}
	return out
}
