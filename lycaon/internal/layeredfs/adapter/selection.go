package adapter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ValidateWorkspaceSelection validates an explicit WorkspaceSelection against attached RootGrants.
// Selection narrows the workspace and never confers authority. TaskPathHints are never used here.
func ValidateWorkspaceSelection(grants []RootGrant, sel WorkspaceSelection) ([]RootGrant, error) {
	if len(grants) == 0 {
		return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: "at least one attached RootGrant is required"}
	}
	byID := make(map[string]RootGrant, len(grants))
	for _, g := range grants {
		if g.RootID == "" || g.SourcePath == "" {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: "grant must have non-empty RootID and SourcePath"}
		}
		byID[g.RootID] = g
	}

	selectedIDs, err := resolveSelectedRootIDs(grants, byID, sel)
	if err != nil {
		return nil, err
	}

	narrowed := make([]RootGrant, 0, len(selectedIDs))
	hasPrimary := false
	for _, id := range selectedIDs {
		g, err := narrowGrantSubroot(byID[id], sel.Subroots[id])
		if err != nil {
			return nil, err
		}
		if g.IsPrimary {
			hasPrimary = true
		}
		narrowed = append(narrowed, g)
	}
	if !hasPrimary && len(narrowed) > 0 {
		narrowed[0].IsPrimary = true
	}
	return narrowed, nil
}

func resolveSelectedRootIDs(grants []RootGrant, byID map[string]RootGrant, sel WorkspaceSelection) ([]string, error) {
	for subRootID := range sel.Subroots {
		if _, ok := byID[subRootID]; !ok {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonUnauthorizedRoot, Diagnostic: fmt.Sprintf("subroot requested for unauthorized root %q", subRootID)}
		}
	}
	if len(sel.RootIDs) == 0 {
		ids := make([]string, 0, len(grants))
		for _, g := range grants {
			ids = append(ids, g.RootID)
		}
		return ids, nil
	}

	seen := make(map[string]bool, len(sel.RootIDs))
	ids := make([]string, 0, len(sel.RootIDs))
	for _, id := range sel.RootIDs {
		if _, ok := byID[id]; !ok {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonUnauthorizedRoot, Diagnostic: fmt.Sprintf("selected root %q is not in attached RootGrants", id)}
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for subRootID := range sel.Subroots {
		if !seen[subRootID] {
			return nil, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q specified for unselected root", subRootID)}
		}
	}
	return ids, nil
}

func narrowGrantSubroot(g RootGrant, reqSubroot string) (RootGrant, error) {
	baseSource := filepath.Clean(g.SourcePath)
	combinedSub := cleanSubrootRel(g.Subroot)
	if reqSubroot != "" {
		cleanReq, err := validateRelativeSubroot(reqSubroot)
		if err != nil {
			return RootGrant{}, err
		}
		if combinedSub == "" {
			combinedSub = cleanReq
		} else {
			combinedSub = filepath.ToSlash(filepath.Join(combinedSub, cleanReq))
		}
		g.Estimate = nil
	}
	if combinedSub != "" {
		targetPath := filepath.Join(baseSource, filepath.FromSlash(combinedSub))
		relCheck, relErr := filepath.Rel(baseSource, targetPath)
		if relErr != nil || strings.HasPrefix(relCheck, "..") {
			return RootGrant{}, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q escapes grant root %q", reqSubroot, g.RootID)}
		}
		st, err := os.Lstat(targetPath)
		if err != nil || !st.IsDir() {
			return RootGrant{}, &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q in root %q is not an accessible directory", combinedSub, g.RootID)}
		}
		g.SourcePath = targetPath
		g.Subroot = combinedSub
	}
	return g, nil
}

func cleanSubrootRel(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" || sub == "." {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(sub))
}

func validateRelativeSubroot(sub string) (string, error) {
	if filepath.IsAbs(sub) || strings.HasPrefix(sub, "/") || strings.HasPrefix(sub, "\\") {
		return "", &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q must be relative", sub)}
	}
	cleaned := filepath.ToSlash(filepath.Clean(sub))
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", &AdapterError{Op: "validate_selection", ReasonCode: ReasonInvalidSelection, Diagnostic: fmt.Sprintf("subroot %q escapes grant boundary", sub)}
	}
	if cleaned == "." {
		return "", nil
	}
	return cleaned, nil
}

// ConfinementEnforcer establishes that a mounted view is usable only through enforcement: the
// worker's processes reach the mount, and are denied its backing and direct source storage.
type ConfinementEnforcer interface {
	// Available reports whether enforcement can be applied in the probed context.
	Available(ctx context.Context, probeCfg ProbeConfig) error
	// VerifyView proves enforcement against one prepared view before it is published ready.
	VerifyView(ctx context.Context, view *PreparedView) error
}

// ConfinementFailureReason is the typed reason an enforcer's refusal carries.
func ConfinementFailureReason(err error) ReasonCode {
	var aErr *AdapterError
	if errors.As(err, &aErr) && aErr.ReasonCode != ReasonNone {
		return aErr.ReasonCode
	}
	return ReasonConfinementUnavailable
}

// Selector chooses one workspace adapter from structured probe capabilities and manages fallback.
type Selector struct {
	TargetOS        string
	LinuxAdapter    Adapter
	DarwinAdapter   Adapter
	FallbackAdapter Adapter
	Registry        *ActiveViewRegistry
	// RequireConfinement refuses a mounted adapter unless Confinement is present and available.
	RequireConfinement bool
	Confinement        ConfinementEnforcer
}

// NewSelector constructs a capability-based Selector with an ActiveViewRegistry.
func NewSelector(targetOS string, linuxAdp, darwinAdp, fallbackAdp Adapter) *Selector {
	if targetOS == "" {
		targetOS = runtime.GOOS
	}
	if fallbackAdp == nil {
		fallbackAdp = NewEagerCopyAdapter(ReasonNone)
	}
	return &Selector{
		TargetOS:        targetOS,
		LinuxAdapter:    linuxAdp,
		DarwinAdapter:   darwinAdp,
		FallbackAdapter: fallbackAdp,
		Registry:        NewActiveViewRegistry(),
	}
}

// Select probes the platform primary adapter (retrying transient failure once) or selects fallback.
func (s *Selector) Select(ctx context.Context, probeCfg ProbeConfig) (Adapter, ProbeResult, error) {
	primary := s.primaryForOS()
	if primary == nil {
		fb := s.withFallbackReason(ReasonUnsupportedPlatform)
		return fb, ProbeResult{
			AdapterID:  fb.ID(),
			Supported:  false,
			ReasonCode: ReasonUnsupportedPlatform,
			Diagnostic: fmt.Sprintf("no mounted adapter for OS %q; using %s", s.TargetOS, fb.ID()),
		}, nil
	}

	if s.RequireConfinement {
		if diag := s.confinementGap(ctx, probeCfg); diag != "" {
			return s.withFallbackReason(ReasonConfinementUnavailable), ProbeResult{
				AdapterID:  primary.ID(),
				ReasonCode: ReasonConfinementUnavailable,
				Diagnostic: diag,
			}, nil
		}
	}

	res, err := primary.Probe(ctx, probeCfg)
	attempts := 1
	if (!res.Supported || err != nil) && shouldRetryTransient(res, err) {
		res, err = primary.Probe(ctx, probeCfg)
		attempts = 2
	}
	res.Attempts = attempts
	if err == nil && res.Supported {
		return primary, res, nil
	}

	reason := res.ReasonCode
	if reason == ReasonNone {
		var aErr *AdapterError
		if errors.As(err, &aErr) && aErr.ReasonCode != ReasonNone {
			reason = aErr.ReasonCode
		} else {
			reason = ReasonUnsupportedKernel
		}
	}
	res.ReasonCode = reason
	res.Supported = false
	return s.withFallbackReason(reason), res, nil
}

// SelectAndPrepare selects an adapter (or reuses the fixed adapter for an existing active view) and prepares it.
func (s *Selector) SelectAndPrepare(ctx context.Context, probeCfg ProbeConfig, req PrepareRequest) (Adapter, *PreparedView, ProbeResult, error) {
	if existingAdp, existingView, ok := s.Registry.AdapterForView(req.ViewID); ok {
		return existingAdp, existingView, ProbeResult{AdapterID: existingAdp.ID(), Supported: true}, nil
	}
	adp, probeRes, err := s.Select(ctx, probeCfg)
	if err != nil {
		return nil, nil, probeRes, err
	}
	view, err := adp.Prepare(ctx, req)
	if err != nil {
		return adp, nil, probeRes, err
	}
	s.Registry.Register(view, adp)
	return adp, view, probeRes, nil
}

func (s *Selector) confinementGap(ctx context.Context, probeCfg ProbeConfig) string {
	if s.Confinement == nil {
		return "no confinement enforcement is available, so a mounted view cannot deny direct backing access"
	}
	if err := s.Confinement.Available(ctx, probeCfg); err != nil {
		return err.Error()
	}
	return ""
}

// VerifyViewConfinement proves enforcement for view, refusing when none is configured.
func (s *Selector) VerifyViewConfinement(ctx context.Context, view *PreparedView) error {
	if s.Confinement == nil {
		return &AdapterError{Op: "verify_confinement", ReasonCode: ReasonConfinementUnavailable, Diagnostic: "no confinement enforcement is configured"}
	}
	return s.Confinement.VerifyView(ctx, view)
}

func (s *Selector) primaryForOS() Adapter {
	switch s.TargetOS {
	case "linux":
		return s.LinuxAdapter
	case "darwin":
		return s.DarwinAdapter
	default:
		return nil
	}
}

func (s *Selector) withFallbackReason(reason ReasonCode) Adapter {
	if ec, ok := s.FallbackAdapter.(*EagerCopyAdapter); ok {
		return ec.WithFallbackReason(reason)
	}
	return s.FallbackAdapter
}

func shouldRetryTransient(res ProbeResult, err error) bool {
	if res.ReasonCode == ReasonTransientStartupFailure {
		return true
	}
	var aErr *AdapterError
	return errors.As(err, &aErr) && aErr.ReasonCode == ReasonTransientStartupFailure
}
