package workerworkspace_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/platform"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestWorkspaceTrackerKeepsUnboundAndUnverifiedFactsConservative(t *testing.T) {
	t.Parallel()
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	trunkDir := t.TempDir()
	branchDir := t.TempDir()
	roots := []projectroot.RootRef{{ID: "primary", Label: "primary", Path: trunkDir, IsPrimary: true}}

	tr := svc.EnsureTracker("job-unbound", "sess-unbound", trunkDir, branchDir, roots)
	if tr == nil {
		t.Fatal("expected EnsureTracker to return a tracker")
	}
	if facts := tr.Facts(); facts.UntouchedVisibility != observation.VisibilityUnknown || facts.SupportsInvalidation ||
		facts.InvalidationVerified || facts.AttributionCompleteness != observation.CompletenessUnknown {
		t.Fatalf("expected unbound tracker to remain conservative unknown/unverified, got %+v", facts)
	}

	// Undeclared visibility, and invalidation claimed for a view that is not live, are refused.
	for name, facts := range map[string]observation.VisibilityFacts{
		"undeclared": {AdapterName: "fuselinux"},
		"fixed with invalidation": {
			AdapterName: "eagercopy", UntouchedVisibility: observation.VisibilityFixed,
			SupportsInvalidation: true, AttributionCompleteness: observation.CompletenessMaterializedSnapshot,
		},
	} {
		desc := observation.ViewCapabilityDescriptor{ViewID: "job-invalid", Facts: facts}
		if _, err := svc.BindViewCapabilities(desc, "job-invalid"); !errors.Is(err, observation.ErrInvalidVisibilityFacts) {
			t.Fatalf("%s: BindViewCapabilities error = %v, want ErrInvalidVisibilityFacts", name, err)
		}
	}

	// Invalidation support without verification stays unverified once bound.
	liveTr, err := svc.BindViewCapabilities(observation.ViewCapabilityDescriptor{ViewID: "job-live-unverified", Facts: observation.VisibilityFacts{
		AdapterName:             "fuselinux",
		UntouchedVisibility:     observation.VisibilityLive,
		SupportsInvalidation:    true,
		AttributionCompleteness: observation.CompletenessPartialKernelCached,
	}}, "job-live-unverified")
	testutil.FailErr(t, "BindViewCapabilities job-live-unverified", err)
	if facts := liveTr.Facts(); !facts.Live() || !facts.SupportsInvalidation || facts.InvalidationVerified {
		t.Fatalf("expected live, invalidating and unverified facts, got %+v", facts)
	}

	// Even with a native read SHA256 digest, an unbound tracker must not claim CompletenessComplete.
	obs, ok := svc.ObserveReadWithFacts("job-unbound", "sess-unbound", trunkDir, branchDir, roots, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "pkg/service.go",
		SHA256:   "deadbeef1234",
		Size:     32,
		Complete: true,
	})
	if !ok || obs.Completeness != observation.CompletenessUnknown {
		t.Fatalf("expected observation on unbound tracker to report CompletenessUnknown, got %+v (ok=%v)", obs, ok)
	}
}

func TestOrdinaryWorkspacePreparationSuppliesEagerCopyFactsWithoutTestInjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	trunkDir := t.TempDir()
	testutil.FailErr(t, "MkdirAll pkg", os.MkdirAll(filepath.Join(trunkDir, "pkg"), 0o755))
	testutil.FailErr(t, "WriteFile lib v1", os.WriteFile(filepath.Join(trunkDir, "pkg", "lib.go"), []byte("package pkg // v1\n"), 0o644))
	testutil.FailErr(t, "WriteFile other v1", os.WriteFile(filepath.Join(trunkDir, "pkg", "other.go"), []byte("package pkg // other v1\n"), 0o644))

	// Prepare workspace through ordinary workspace.Manager (selects eagercopy fallback when no mounted helper is active).
	wsMgr := workspace.NewManager(filepath.Join(t.TempDir(), "branches"), platform.NewSelector(platform.Config{}), nil)
	binding, layout, err := wsMgr.CreateWorkerBranch(
		ctx,
		[]projectroot.RootRef{{ID: "primary", Label: "primary", Path: trunkDir, IsPrimary: true}},
		"primary",
		"job-prod-eager",
	)
	testutil.FailErr(t, "CreateWorkerBranch", err)
	t.Cleanup(func() { _ = wsMgr.DestroyWorkerWorkspace(t.Context(), binding) })

	// The service finds the prepared view's facts itself; nothing binds capabilities by hand.
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	tr := svc.EnsureTracker("job-prod-eager", "sess-eager", trunkDir, binding.Root, layout.Roots)
	if tr == nil {
		t.Fatal("expected EnsureTracker to return a tracker")
	}

	var dispatched []observation.InvalidationEvent
	tr.RegisterInvalidationSink(observation.InvalidationFunc(func(ev observation.InvalidationEvent) {
		dispatched = append(dispatched, ev)
	}))

	facts := tr.Facts()
	if facts.AdapterName != string(adapter.AdapterEagerCopy) {
		t.Fatalf("expected AdapterName=eagercopy from prepared workspace view, got %q", facts.AdapterName)
	}
	if facts.UntouchedVisibility != observation.VisibilityFixed || facts.SupportsInvalidation || facts.InvalidationVerified {
		t.Fatalf("expected fixed, non-invalidating facts for eagercopy, got %+v", facts)
	}
	if facts.AttributionCompleteness != observation.CompletenessMaterializedSnapshot {
		t.Fatalf("expected CompletenessMaterializedSnapshot for eagercopy, got %+v", facts)
	}

	obs, ok := svc.ObserveReadWithFacts("job-prod-eager", "sess-eager", trunkDir, binding.Root, layout.Roots, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "pkg/lib.go",
		SHA256:   "sha-lib-v1-seeded",
		Size:     18,
		Complete: true,
	})
	if !ok || obs.Completeness != observation.CompletenessMaterializedSnapshot {
		t.Fatalf("expected materialized seed observation to preserve CompletenessMaterializedSnapshot, got %+v (ok=%v)", obs, ok)
	}

	receipt1, ok := svc.BindVerificationReceipt("job-prod-eager", "sess-eager", trunkDir, binding.Root, "chk-eager-1", "ckpt-eager", []string{"primary"})
	if !ok || receipt1.ReadAttributionCompleteness != observation.CompletenessMaterializedSnapshot {
		t.Fatalf("expected binding completeness CompletenessMaterializedSnapshot, got %+v (ok=%v)", receipt1, ok)
	}

	// Trunk change to an unlogged file in the same scope advances generation under fixed-seed semantics:
	// zero live-mount invalidation upcalls are dispatched, and the receipt is invalidated.
	testutil.FailErr(t, "WriteFile other v2", os.WriteFile(filepath.Join(trunkDir, "pkg", "other.go"), []byte("package pkg // other v2\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: trunkDir,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: "pkg/other.go", Kind: repochange.WorktreeChangeContent}},
		Source:     repochange.SourceWatcher,
	})
	if len(dispatched) != 0 {
		t.Fatalf("expected eagercopy fixed-seed view to dispatch 0 live mount invalidations, got %+v", dispatched)
	}
	eval1 := tr.EvaluateReceiptFreshness(receipt1, "ckpt-eager")
	if !eval1.Stale || eval1.StaleReason != observation.StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected materialized snapshot receipt to be stale when enclosing scope advances, got %+v", eval1)
	}
}

func TestOrdinaryWorkspacePreparationSuppliesMountedPartialAttributionFactsWithoutTestInjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	trunkDir := t.TempDir()
	testutil.FailErr(t, "MkdirAll pkg", os.MkdirAll(filepath.Join(trunkDir, "pkg"), 0o755))
	testutil.FailErr(t, "WriteFile api.go", os.WriteFile(filepath.Join(trunkDir, "pkg", "api.go"), []byte("package pkg // v1\n"), 0o644))

	// Configure workspace.Manager with a mounted adapter declaring partial kernel-cached attribution,
	// NegativeLookupInvalidation=false, DirCookieInvalidation=true, InvalidationVerified=false.
	mountedAdp := adapter.NewMountedAdapter(adapter.MountedAdapterConfig{
		AdapterID:                  adapter.AdapterNFSDarwin,
		InvalidationVerified:       false,
		NegativeLookupInvalidation: false,
		DirCookieInvalidation:      true,
		AttributionCompleteness:    string(observation.CompletenessPartialKernelCached),
		MountProbeFn: func(context.Context, string, string) (adapter.ReasonCode, string, error) {
			return adapter.ReasonNone, "", nil
		},
		VerifyBackingDenialFn: func(context.Context, string) (bool, error) {
			return true, nil
		},
		MountViewFn: func(context.Context, string, []adapter.PreparedRoot) error {
			return nil
		},
	})
	selector := adapter.NewSelector("darwin", nil, mountedAdp, adapter.NewEagerCopyAdapter())
	selector.BudgetFor = adapter.ZeroBudget
	selector.Confinement = fixtureConfinement{}
	wsMgr := workspace.NewManager(filepath.Join(t.TempDir(), "branches"), selector, nil)

	binding, layout, err := wsMgr.CreateWorkerBranch(
		ctx,
		[]projectroot.RootRef{{ID: "primary", Label: "primary", Path: trunkDir, IsPrimary: true}},
		"primary",
		"job-prod-nfs",
	)
	testutil.FailErr(t, "CreateWorkerBranch mounted", err)
	t.Cleanup(func() { _ = wsMgr.DestroyWorkerWorkspace(t.Context(), binding) })

	// Construct production workerworkspace.Service and record observations WITHOUT any test-only capability injection.
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	obs, ok := svc.ObserveReadWithFacts("job-prod-nfs", "sess-nfs", trunkDir, binding.Root, layout.Roots, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "pkg/api.go",
		SHA256:   "sha-api-v1",
		Size:     18,
		Complete: true,
	})
	if !ok || obs.Completeness != observation.CompletenessPartialKernelCached {
		t.Fatalf("expected observation to preserve adapter's CompletenessPartialKernelCached despite read digest, got %+v (ok=%v)", obs, ok)
	}

	tr := svc.TrackerFor("job-prod-nfs")
	if tr == nil {
		t.Fatal("expected tracker for job-prod-nfs")
	}
	var dispatched []observation.InvalidationEvent
	tr.RegisterInvalidationSink(observation.InvalidationFunc(func(ev observation.InvalidationEvent) {
		dispatched = append(dispatched, ev)
	}))

	facts := tr.Facts()
	if facts.AdapterName != string(adapter.AdapterNFSDarwin) || !facts.Live() {
		t.Fatalf("expected live mount facts for nfsdarwin from prepared view, got %+v", facts)
	}
	if !facts.SupportsInvalidation || facts.SupportsNegativeLookupInvalidation || !facts.SupportsDirCookieInvalidation || facts.InvalidationVerified {
		t.Fatalf("expected adapter's exact invalidation and unverified flags to be preserved, got %+v", facts)
	}
	if facts.AttributionCompleteness != observation.CompletenessPartialKernelCached {
		t.Fatalf("expected CompletenessPartialKernelCached on tracker facts, got %+v", facts)
	}

	tr.RecordNegativeLookup(layeredfs.QualifiedPath{RootID: "primary", Rel: "pkg/newfile.go"})
	receipt, ok := svc.BindVerificationReceipt("job-prod-nfs", "sess-nfs", trunkDir, binding.Root, "chk-nfs-1", "ckpt-nfs", []string{"primary"})
	if !ok || receipt.ReadAttributionCompleteness != observation.CompletenessPartialKernelCached {
		t.Fatalf("expected binding to preserve CompletenessPartialKernelCached, got %+v (ok=%v)", receipt, ok)
	}

	testutil.FailErr(t, "WriteFile newfile.go", os.WriteFile(filepath.Join(trunkDir, "pkg", "newfile.go"), []byte("package pkg\n"), 0o644))
	repochange.Notify(ctx, repochange.Event{
		ProjectDir: trunkDir,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: "pkg/newfile.go", Kind: repochange.WorktreeChangeStructural}},
		Source:     repochange.SourceWatcher,
	})
	if len(dispatched) == 0 {
		t.Fatal("expected mounted adapter with SupportsInvalidation=true to receive invalidations")
	}
	for _, ev := range dispatched {
		if ev.Kind == observation.InvalidateNegativeName {
			t.Fatalf("expected SupportsNegativeLookupInvalidation=false to suppress InvalidateNegativeName, got %+v", ev)
		}
	}
	fresh := tr.EvaluateReceiptFreshness(receipt, "ckpt-nfs")
	if !fresh.Stale || fresh.StaleReason != observation.StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected partial-attribution mount receipt to be invalidated when scope generation advances, got %+v", fresh)
	}
}

// fixtureConfinement stands in for enforcement around the fixture MountedAdapter.
type fixtureConfinement struct{}

func (fixtureConfinement) Available(context.Context, adapter.ProbeConfig) error    { return nil }
func (fixtureConfinement) VerifyView(context.Context, *adapter.PreparedView) error { return nil }
