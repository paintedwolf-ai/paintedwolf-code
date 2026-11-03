package workerworkspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestWorkspaceTrackerUnboundAndNameOnlyDescriptorsRemainUnknownAndUnverified(t *testing.T) {
	t.Parallel()
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	trunkDir := t.TempDir()
	branchDir := t.TempDir()
	roots := []projectroot.RootRef{{ID: "primary", Path: trunkDir, IsPrimary: true}}

	tr := svc.EnsureTracker("job-unbound", "sess-unbound", trunkDir, branchDir, roots)
	if tr == nil {
		t.Fatal("expected EnsureTracker to return a tracker")
	}
	facts := tr.VisibilityFacts()
	if facts.Mode != observation.VisibilityModeUnknown || facts.UntouchedVisibility != observation.VisibilityUnknown ||
		facts.LiveVisible || facts.SupportsInvalidation || facts.InvalidationVerified ||
		facts.Completeness != observation.CompletenessUnknown {
		t.Fatalf("expected unbound tracker to remain conservative unknown/unverified, got %+v", facts)
	}

	// Name-only descriptors must never infer visibility or completeness from adapter-name spelling.
	for _, name := range []string{"fuselinux", "nfsdarwin", "fskit-macos", "eagercopy", "materialized-fallback"} {
		desc := observation.ViewCapabilityDescriptor{AdapterName: name}
		got := observation.VisibilityFactsFromDescriptor(desc)
		if got.UntouchedVisibility != observation.VisibilityUnknown || got.Mode != observation.VisibilityModeUnknown ||
			got.LiveVisible || got.SupportsInvalidation || got.InvalidationVerified ||
			got.Completeness != observation.CompletenessUnknown || got.AttributionCompleteness != observation.CompletenessUnknown {
			t.Fatalf("adapter name %q alone must not infer visibility or completeness, got %+v", name, got)
		}
		boundTr, err := svc.BindViewCapabilities(desc, "job-name-only-"+name)
		testutil.FailErr(t, "BindViewCapabilities "+name, err)
		bf := boundTr.VisibilityFacts()
		if bf.UntouchedVisibility != observation.VisibilityUnknown || bf.Completeness != observation.CompletenessUnknown || bf.LiveVisible || bf.InvalidationVerified {
			t.Fatalf("bound name-only descriptor %q must remain unknown/unverified, got %+v", name, bf)
		}
	}

	// Declared-live + SupportsInvalidation=true + InvalidationVerified=false must preserve InvalidationVerified=false.
	unverifiedLiveDesc := observation.ViewCapabilityDescriptor{
		ViewID:               "job-live-unverified",
		AdapterName:          "fuselinux",
		Mode:                 observation.VisibilityModeLiveMount,
		UntouchedVisibility:  observation.VisibilityLive,
		LiveVisible:          true,
		SupportsInvalidation: true,
		InvalidationVerified: false,
		Completeness:         observation.CompletenessPartialKernelCached,
	}
	liveFacts := observation.VisibilityFactsFromDescriptor(unverifiedLiveDesc)
	if !liveFacts.LiveVisible || !liveFacts.SupportsInvalidation || liveFacts.InvalidationVerified {
		t.Fatalf("expected LiveVisible=true, SupportsInvalidation=true, InvalidationVerified=false, got %+v", liveFacts)
	}
	liveTr, err := svc.BindVisibilityFacts(observation.VisibilityFacts{
		AdapterName:             "fuselinux",
		UntouchedVisibility:     observation.VisibilityLive,
		SupportsInvalidation:    true,
		InvalidationVerified:    false,
		AttributionCompleteness: observation.CompletenessPartialKernelCached,
	}, "job-live-unverified")
	testutil.FailErr(t, "BindVisibilityFacts job-live-unverified", err)
	if liveTr.VisibilityFacts().InvalidationVerified {
		t.Fatalf("expected InvalidationVerified=false to be preserved on tracker, got %+v", liveTr.VisibilityFacts())
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
	wsMgr := workspace.NewManager(filepath.Join(t.TempDir(), "branches"), filepath.Join(t.TempDir(), "seeds"))
	binding, layout, err := wsMgr.CreateWorkerWorkspaceFromSources(
		ctx,
		[]projectroot.RootRef{{ID: "primary", Path: trunkDir, IsPrimary: true}},
		[]projectroot.RootRef{{ID: "primary", Path: trunkDir, IsPrimary: true}},
		"primary",
		"job-prod-eager",
	)
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	t.Cleanup(func() { _ = wsMgr.DestroyWorkerWorkspace(binding) })

	// Construct production workerworkspace.Service and call EnsureTracker / ObserveReadWithFacts
	// with ZERO test-only calls to BindViewCapabilities, BindVisibilityFacts, or SetViewCapabilitiesResolver.
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

	facts := tr.VisibilityFacts()
	if facts.AdapterName != string(adapter.AdapterEagerCopy) {
		t.Fatalf("expected AdapterName=eagercopy from prepared workspace view, got %q", facts.AdapterName)
	}
	if facts.Mode != observation.VisibilityModeMaterializedSeed || facts.UntouchedVisibility != observation.VisibilityFixed {
		t.Fatalf("expected VisibilityModeMaterializedSeed / VisibilityFixed, got %+v", facts)
	}
	if facts.LiveVisible || facts.SupportsInvalidation || facts.SupportsInvalidationUpcall || facts.InvalidationVerified {
		t.Fatalf("expected LiveVisible=false and SupportsInvalidation=false for eagercopy, got %+v", facts)
	}
	if facts.Completeness != observation.CompletenessMaterializedSnapshot || facts.AttributionCompleteness != observation.CompletenessMaterializedSnapshot {
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
	wsMgr := workspace.NewManager(filepath.Join(t.TempDir(), "branches"), filepath.Join(t.TempDir(), "seeds")).
		WithSelector(adapter.NewSelector("darwin", adapter.NewFUSELinuxAdapter(), mountedAdp, adapter.NewEagerCopyAdapter(adapter.ReasonNone)))

	binding, layout, err := wsMgr.CreateWorkerWorkspaceFromSources(
		ctx,
		[]projectroot.RootRef{{ID: "primary", Path: trunkDir, IsPrimary: true}},
		[]projectroot.RootRef{{ID: "primary", Path: trunkDir, IsPrimary: true}},
		"primary",
		"job-prod-nfs",
	)
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources mounted", err)
	t.Cleanup(func() { _ = wsMgr.DestroyWorkerWorkspace(binding) })

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

	facts := tr.VisibilityFacts()
	if facts.AdapterName != string(adapter.AdapterNFSDarwin) || facts.Mode != observation.VisibilityModeLiveMount || !facts.LiveVisible {
		t.Fatalf("expected live mount facts for nfsdarwin from prepared view, got %+v", facts)
	}
	if !facts.SupportsInvalidation || facts.SupportsNegativeLookupInvalidation || !facts.SupportsDirCookieInvalidation || facts.InvalidationVerified {
		t.Fatalf("expected adapter's exact invalidation and unverified flags to be preserved, got %+v", facts)
	}
	if facts.Completeness != observation.CompletenessPartialKernelCached {
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
