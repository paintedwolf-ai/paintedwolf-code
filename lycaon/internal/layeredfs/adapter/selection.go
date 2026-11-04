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

// Selector chooses one workspace provider per worker from typed facts: the cost of materializing
// its roots, the recorded budget for that cost, and whether a mount can actually serve it.
type Selector struct {
	TargetOS        string
	LinuxAdapter    Adapter
	DarwinAdapter   Adapter
	FallbackAdapter Adapter
	Registry        *ActiveViewRegistry
	// BudgetFor reads the recorded budgets. Fixtures replace it to force a side of the rule; no
	// setting or request reaches it.
	BudgetFor func(BudgetClass) BudgetFact
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
		BudgetFor:       RecordedBudget,
	}
}

// ChooseRequest carries the facts one worker's selection reads.
type ChooseRequest struct {
	Probe  ProbeConfig
	Grants []RootGrant
	// CloneAvailable answers for each root; every root counts as copy-only without it.
	CloneAvailable CloneAvailability
	// Disk reads free space on the volume that would hold materialized trees.
	Disk func() (DiskSpace, error)
	// Overrun is the copy that just passed its budget, which moves selection past materializing.
	Overrun *BudgetOverrunError
}

// Choice is one worker's provider and the facts that chose it.
type Choice struct {
	Adapter Adapter
	Probe   ProbeResult
	Facts   SelectionFacts
	// CopyBudget bounds a materialization; nil is the declared cannot-mount case and every mount.
	CopyBudget *CopyBudget
}

// Choose applies the selection rule. Under the budget it materializes. Over it, a mount serves when
// its probe succeeds. A lasting reason a mount is unavailable materializes with the cost counted in
// full first, and a transient one never starts a large copy. Only a disk that cannot hold the result
// refuses.
func (s *Selector) Choose(ctx context.Context, req ChooseRequest) (Choice, error) {
	roots, anyCopyOnly, err := classifyRootClones(ctx, req)
	if err != nil {
		return Choice{}, err
	}
	fact := s.budgetFact(anyCopyOnly)
	facts := SelectionFacts{CloneAvailable: !anyCopyOnly, Budget: fact, CopyPassedBudget: req.Overrun != nil}

	var cost MaterializeCost
	if req.Overrun != nil {
		// What the estimate said is what the copy just disproved.
		cost = overrunCost(req.Overrun, MaterializeCost{Partial: true})
		for i := range roots {
			roots[i].grant.Estimate = nil
		}
	} else if cost, err = countMaterializeCost(ctx, roots, fact, true); err != nil {
		return Choice{}, err
	}
	facts.DeclaredCost = cost

	if req.Overrun == nil && !cost.Partial && cost.Total(fact) <= fact.Limit {
		facts.Reason = SelectionUnderBudget
		return s.chooseMaterialized(ctx, req, facts, ReasonNone, ProbeResult{}, &CopyBudget{Limit: fact.Limit, EntryCost: fact.EntryCost})
	}

	primary := s.primaryForOS()
	mountReason, probe, err := s.probeMount(ctx, primary, req.Probe)
	if err != nil {
		return Choice{Probe: probe}, err
	}
	if mountReason == ReasonNone {
		facts.Provider, facts.Reason = primary.ID(), SelectionOverBudgetMounted
		return Choice{Adapter: primary, Probe: probe, Facts: facts}, nil
	}

	if cost.Partial || req.Overrun != nil {
		cost, err = countMaterializeCost(ctx, roots, fact, false)
		if err != nil {
			return Choice{Probe: probe}, err
		}
		if req.Overrun != nil {
			cost = overrunCost(req.Overrun, cost)
		}
		facts.DeclaredCost = cost
	}
	facts.Reason, facts.MountUnavailable = SelectionOverBudgetMaterial, mountReason
	return s.chooseMaterialized(ctx, req, facts, mountReason, probe, nil)
}

func (s *Selector) chooseMaterialized(ctx context.Context, req ChooseRequest, facts SelectionFacts, fallback ReasonCode, probe ProbeResult, budget *CopyBudget) (Choice, error) {
	if err := ctx.Err(); err != nil {
		return Choice{Probe: probe}, err
	}
	if err := checkCapacity(req, facts); err != nil {
		return Choice{Probe: probe}, err
	}
	adp := s.withFallbackReason(fallback)
	facts.Provider = adp.ID()
	return Choice{Adapter: adp, Probe: probe, Facts: facts, CopyBudget: budget}, nil
}

func checkCapacity(req ChooseRequest, facts SelectionFacts) error {
	if req.Disk == nil {
		return nil
	}
	space, err := req.Disk()
	if err != nil {
		return err
	}
	required := uint64(max(facts.DeclaredCost.Total(facts.Budget), 0))
	if !space.Known || space.Available >= required {
		return nil
	}
	return &WorkspaceCapacityError{
		Path: space.Path, Required: required, Available: space.Available,
		SelectedRoots: collectGrantIDs(req.Grants), SuggestedSubroots: suggestSubroots(req.Grants),
	}
}

func (s *Selector) budgetFact(anyCopyOnly bool) BudgetFact {
	class := BudgetClass{Platform: s.TargetOS, Filesystem: FilesystemClonable}
	if anyCopyOnly {
		class.Filesystem = FilesystemCopyOnly
	}
	if s.BudgetFor == nil {
		return RecordedBudget(class)
	}
	return s.BudgetFor(class)
}

// probeMount reports ReasonNone for a usable mount or a lasting reason for an unusable one. A
// transient failure retries once, and what remains comes back as an error rather than a reason.
func (s *Selector) probeMount(ctx context.Context, primary Adapter, cfg ProbeConfig) (ReasonCode, ProbeResult, error) {
	if primary == nil {
		return ReasonUnsupportedPlatform, ProbeResult{
			AdapterID: AdapterEagerCopy, ReasonCode: ReasonUnsupportedPlatform,
			Diagnostic: fmt.Sprintf("no mounted adapter for OS %q", s.TargetOS),
		}, nil
	}
	if s.RequireConfinement {
		if diag := s.confinementGap(ctx, cfg); diag != "" {
			return ReasonConfinementUnavailable, ProbeResult{
				AdapterID: primary.ID(), ReasonCode: ReasonConfinementUnavailable, Diagnostic: diag,
			}, nil
		}
	}
	res, err := primary.Probe(ctx, cfg)
	attempts := 1
	if (!res.Supported || err != nil) && shouldRetryTransient(res, err) {
		res, err = primary.Probe(ctx, cfg)
		attempts = 2
	}
	res.Attempts = attempts
	if err == nil && res.Supported {
		return ReasonNone, res, nil
	}
	if cause := ctx.Err(); cause != nil {
		return ReasonContextCanceled, res, cause
	}
	reason := res.ReasonCode
	if reason == ReasonNone {
		var aErr *AdapterError
		if errors.As(err, &aErr) {
			reason = aErr.ReasonCode
		}
	}
	res.ReasonCode, res.Supported = reason, false
	if IsLastingMountReason(reason) {
		return reason, res, nil
	}
	return reason, res, &AdapterError{
		AdapterID: primary.ID(), Op: "probe", ReasonCode: ReasonTransientStartupFailure, Cause: err,
		Diagnostic: fmt.Sprintf("mount probe failed without a lasting reason (%s), so no large copy starts: %s", reason, res.Diagnostic),
	}
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
