package workerworkspace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/observation"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

func TestWorkspaceTrackerUnboundDefaultIsConservative(t *testing.T) {
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

	var dispatched []observation.InvalidationEvent
	tr.RegisterInvalidationSink(observation.InvalidationFunc(func(ev observation.InvalidationEvent) {
		dispatched = append(dispatched, ev)
	}))

	facts := tr.Facts()
	if facts.Mode != observation.VisibilityModeUnknown || facts.UntouchedVisibility != observation.VisibilityUnknown {
		t.Fatalf("expected unbound tracker Mode=VisibilityModeUnknown, got %+v", facts)
	}
	if facts.LiveVisible || facts.SupportsInvalidation || facts.SupportsInvalidationUpcall || facts.InvalidationVerified {
		t.Fatalf("expected unbound tracker not to claim live visibility or invalidation support, got %+v", facts)
	}
	if facts.Completeness != observation.CompletenessUnknown || facts.AttributionCompleteness != observation.CompletenessUnknown {
		t.Fatalf("expected unbound tracker Completeness=CompletenessUnknown, got %+v", facts)
	}

	// Even with a native read SHA256 digest, an unbound tracker must not claim CompletenessComplete.
	obs, ok := svc.ObserveReadWithFacts("job-unbound", "sess-unbound", trunkDir, branchDir, roots, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "pkg/service.go",
		SHA256:   "deadbeef1234",
		Size:     32,
		Complete: true,
	})
	if !ok {
		t.Fatal("expected ObserveReadWithFacts to succeed")
	}
	if obs.Completeness != observation.CompletenessUnknown {
		t.Fatalf("expected observation on unbound tracker to report CompletenessUnknown, got %+v", obs)
	}

	binding, ok := svc.BindVerificationReceipt("job-unbound", "sess-unbound", trunkDir, branchDir, "chk-unbound", "ckpt-1", []string{"primary"})
	if !ok {
		t.Fatal("expected BindVerificationReceipt to succeed")
	}
	if binding.ReadAttributionCompleteness != observation.CompletenessUnknown {
		t.Fatalf("expected binding completeness to remain CompletenessUnknown, got %s", binding.ReadAttributionCompleteness)
	}

	// Trunk change on an unobserved file in the scope advances generation and invalidates the receipt without emitting live invalidation upcalls.
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(trunkDir, "unrelated.go"), []byte("package main\n"), 0o644))
	repochange.Notify(context.Background(), repochange.Event{
		ProjectDir: trunkDir,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: "unrelated.go", Kind: repochange.WorktreeChangeContent}},
		Source:     repochange.SourceWatcher,
	})
	if len(dispatched) != 0 {
		t.Fatalf("expected unbound tracker to dispatch 0 invalidations, got %+v", dispatched)
	}
	fresh := tr.EvaluateReceiptFreshness(binding, "ckpt-1")
	if !fresh.Stale || fresh.StaleReason != observation.StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected unbound tracker receipt to be stale on scope advance, got %+v", fresh)
	}
}

func TestWorkspaceTrackerEagerCopyMaterializedSeedProductionPath(t *testing.T) {
	t.Parallel()
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	trunkDir := t.TempDir()
	branchDir := t.TempDir()
	testutil.FailErr(t, "MkdirAll pkg", os.MkdirAll(filepath.Join(trunkDir, "pkg"), 0o755))
	testutil.FailErr(t, "WriteFile lib v1", os.WriteFile(filepath.Join(trunkDir, "pkg", "lib.go"), []byte("package pkg // v1\n"), 0o644))
	testutil.FailErr(t, "WriteFile other v1", os.WriteFile(filepath.Join(trunkDir, "pkg", "other.go"), []byte("package pkg // other v1\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "primary", Path: trunkDir, IsPrimary: true}}

	// Bind eagercopy materialized-seed capabilities on the production registry before EnsureTracker.
	_, err := svc.BindViewCapabilities(observation.EagerCopyViewCapabilities("job-eager"), "job-eager")
	testutil.FailErr(t, "BindViewCapabilities eagercopy", err)

	tr := svc.EnsureTracker("job-eager", "sess-eager", trunkDir, branchDir, roots)
	if tr == nil {
		t.Fatal("expected EnsureTracker to return a tracker")
	}

	var dispatched []observation.InvalidationEvent
	tr.RegisterInvalidationSink(observation.InvalidationFunc(func(ev observation.InvalidationEvent) {
		dispatched = append(dispatched, ev)
	}))

	facts := tr.Facts()
	if facts.AdapterName != "eagercopy" {
		t.Fatalf("expected AdapterName=eagercopy, got %q", facts.AdapterName)
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

	obs, ok := svc.ObserveReadWithFacts("job-eager", "sess-eager", trunkDir, branchDir, roots, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "pkg/lib.go",
		SHA256:   "sha-lib-v1-seeded",
		Size:     18,
		Complete: true,
	})
	if !ok || obs.Completeness != observation.CompletenessMaterializedSnapshot {
		t.Fatalf("expected materialized seed observation to preserve CompletenessMaterializedSnapshot, got %+v (ok=%v)", obs, ok)
	}

	binding1, ok := svc.BindVerificationReceipt("job-eager", "sess-eager", trunkDir, branchDir, "chk-eager-1", "ckpt-eager", []string{"primary"})
	if !ok || binding1.ReadAttributionCompleteness != observation.CompletenessMaterializedSnapshot {
		t.Fatalf("expected binding completeness CompletenessMaterializedSnapshot, got %+v (ok=%v)", binding1, ok)
	}

	// 1. Trunk change to an unlogged file in the same scope advances generation under fixed-seed semantics:
	// zero live-mount invalidation upcalls are dispatched, and the receipt is invalidated because attribution is a materialized snapshot.
	testutil.FailErr(t, "WriteFile other v2", os.WriteFile(filepath.Join(trunkDir, "pkg", "other.go"), []byte("package pkg // other v2\n"), 0o644))
	repochange.Notify(context.Background(), repochange.Event{
		ProjectDir: trunkDir,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: "pkg/other.go", Kind: repochange.WorktreeChangeContent}},
		Source:     repochange.SourceWatcher,
	})
	if len(dispatched) != 0 {
		t.Fatalf("expected eagercopy fixed-seed view to dispatch 0 live mount invalidations, got %+v", dispatched)
	}
	eval1 := tr.EvaluateReceiptFreshness(binding1, "ckpt-eager")
	if !eval1.Stale || eval1.StaleReason != observation.StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected materialized snapshot receipt to be stale when enclosing scope advances, got %+v", eval1)
	}

	// 2. Trunk change to the observed file pkg/lib.go marks the observation stale and queues trunk.changed without live invalidation upcalls.
	binding2, _ := svc.BindVerificationReceipt("job-eager", "sess-eager", trunkDir, branchDir, "chk-eager-2", "ckpt-eager", []string{"primary"})
	testutil.FailErr(t, "WriteFile lib v2", os.WriteFile(filepath.Join(trunkDir, "pkg", "lib.go"), []byte("package pkg // v2-trunk\n"), 0o644))
	repochange.Notify(context.Background(), repochange.Event{
		ProjectDir: trunkDir,
		Kind:       repochange.WorktreeChanged,
		Changes:    []repochange.WorktreeChange{{Path: "pkg/lib.go", Kind: repochange.WorktreeChangeContent}},
		Source:     repochange.SourceWatcher,
	})
	if len(dispatched) != 0 {
		t.Fatalf("expected 0 invalidation upcalls for eagercopy on observed path change, got %+v", dispatched)
	}
	eval2 := tr.EvaluateReceiptFreshness(binding2, "ckpt-eager")
	if !eval2.Stale || eval2.StaleReason != observation.StaleReasonObservedDependencyChanged {
		t.Fatalf("expected observed dependency change staleness on eagercopy, got %+v", eval2)
	}
	notice, formatted := svc.DrainTrunkChangedNotice("job-eager")
	if notice == nil || formatted == "" || notice.GrantsAuthority || notice.EstablishesMergeBase {
		t.Fatalf("expected non-authoritative trunk.changed notice for eagercopy, got notice=%+v formatted=%q", notice, formatted)
	}
}

func TestWorkspaceTrackerMountedAdapterPartialAttributionProductionPath(t *testing.T) {
	t.Parallel()
	svc := workerworkspace.New(nil, nil, nil)
	t.Cleanup(svc.Close)

	trunkDir := t.TempDir()
	branchDir := t.TempDir()
	testutil.FailErr(t, "MkdirAll pkg", os.MkdirAll(filepath.Join(trunkDir, "pkg"), 0o755))
	testutil.FailErr(t, "WriteFile api.go", os.WriteFile(filepath.Join(trunkDir, "pkg", "api.go"), []byte("package pkg // v1\n"), 0o644))
	testutil.FailErr(t, "WriteFile cached.go", os.WriteFile(filepath.Join(trunkDir, "pkg", "cached.go"), []byte("package pkg // cached v1\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "primary", Path: trunkDir, IsPrimary: true}}

	// Use the resolver hook on Service/WorkspaceTrackerRegistry to supply mounted NFS adapter capabilities
	// declaring CompletenessPartialKernelCached and explicit invalidation flags.
	svc.SetViewCapabilitiesResolver(func(jobID, _, _, _ string) (workerworkspace.ViewCapabilityDescriptor, bool) {
		if jobID == "job-nfs-partial" {
			return observation.MountedAdapterViewCapabilities(
				jobID,
				"nfsdarwin",
				true,  // supportsInvalidation
				false, // negativeLookupInvalidation
				true,  // dirCookieInvalidation
				observation.CompletenessPartialKernelCached,
			), true
		}
		return workerworkspace.ViewCapabilityDescriptor{}, false
	})

	obs, ok := svc.ObserveReadWithFacts("job-nfs-partial", "sess-nfs", trunkDir, branchDir, roots, tools.WorkerFileObservationFacts{
		RootID:   "primary",
		RelPath:  "pkg/api.go",
		SHA256:   "sha-api-v1",
		Size:     18,
		Complete: true,
	})
	if !ok {
		t.Fatal("expected ObserveReadWithFacts to succeed")
	}
	if obs.Completeness != observation.CompletenessPartialKernelCached {
		t.Fatalf("expected observation to preserve adapter's CompletenessPartialKernelCached despite read digest, got %+v", obs)
	}

	tr := svc.TrackerFor("job-nfs-partial")
	if tr == nil {
		t.Fatal("expected tracker for job-nfs-partial")
	}
	var dispatched []observation.InvalidationEvent
	tr.RegisterInvalidationSink(observation.InvalidationFunc(func(ev observation.InvalidationEvent) {
		dispatched = append(dispatched, ev)
	}))

	facts := tr.Facts()
	if facts.AdapterName != "nfsdarwin" || facts.Mode != observation.VisibilityModeLiveMount || !facts.LiveVisible {
		t.Fatalf("expected live mount facts for nfsdarwin, got %+v", facts)
	}
	if !facts.SupportsInvalidation || !facts.SupportsInvalidationUpcall || facts.SupportsNegativeLookupInvalidation || !facts.SupportsDirCookieInvalidation {
		t.Fatalf("expected adapter's exact invalidation support flags to be preserved, got %+v", facts)
	}
	if facts.Completeness != observation.CompletenessPartialKernelCached || facts.AttributionCompleteness != observation.CompletenessPartialKernelCached {
		t.Fatalf("expected CompletenessPartialKernelCached on tracker facts, got %+v", facts)
	}

	// Record a negative lookup so we can verify SupportsNegativeLookupInvalidation=false is respected.
	tr.RecordNegativeLookup(layeredfs.QualifiedPath{RootID: "primary", Rel: "pkg/newfile.go"})

	binding, ok := svc.BindVerificationReceipt("job-nfs-partial", "sess-nfs", trunkDir, branchDir, "chk-nfs-1", "ckpt-nfs", []string{"primary"})
	if !ok || binding.ReadAttributionCompleteness != observation.CompletenessPartialKernelCached {
		t.Fatalf("expected binding to preserve CompletenessPartialKernelCached, got %+v (ok=%v)", binding, ok)
	}

	// Create pkg/newfile.go on trunk (unlogged by server reads) and notify repochange.
	testutil.FailErr(t, "WriteFile newfile.go", os.WriteFile(filepath.Join(trunkDir, "pkg", "newfile.go"), []byte("package pkg\n"), 0o644))
	repochange.Notify(context.Background(), repochange.Event{
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

	fresh := tr.EvaluateReceiptFreshness(binding, "ckpt-nfs")
	if !fresh.Stale || fresh.StaleReason != observation.StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected partial-attribution mount receipt to be invalidated when scope generation advances, got %+v", fresh)
	}
}
