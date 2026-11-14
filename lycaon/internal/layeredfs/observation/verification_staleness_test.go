package observation

import (
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCrossDirectoryAndCrossRootDependencyInvalidatesReceipt(t *testing.T) {
	t.Parallel()
	facts := LiveMountVisibilityFacts("fuse-linux", true, true, true, CompletenessComplete)
	fx := newTestFixture(t, facts)

	qpA := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-a", Rel: rel}
	}
	qpB := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-b", Rel: rel}
	}

	writeTrunkInPlace(t, fx.trunkA, "cmd/app/main.go", []byte("package main\n"))
	writeTrunkInPlace(t, fx.trunkA, "internal/shared/helper.go", []byte("package shared // v1\n"))
	writeTrunkInPlace(t, fx.trunkA, "docs/unrelated.md", []byte("# docs v1\n"))
	writeTrunkInPlace(t, fx.trunkB, "proto/schema.json", []byte(`{"version":1}`))

	// Worker mutates cmd/app/main.go, and verification reads helper.go (another directory)
	// and proto/schema.json (another root).
	_, err := fx.core.WriteFile(qpA("cmd/app/main.go"), []byte("package main // edited\n"), 0o644)
	testutil.FailErr(t, "WriteFile main.go", err)

	helperBytes, err := fx.core.ReadFile(qpA("internal/shared/helper.go"))
	testutil.FailErr(t, "ReadFile helper.go", err)
	helperAttr, err := fx.core.Stat(qpA("internal/shared/helper.go"))
	testutil.FailErr(t, "Stat helper.go", err)
	fx.tracker.RecordExactNativeObservation(qpA("internal/shared/helper.go"), helperBytes, helperAttr)

	schemaBytes, err := fx.core.ReadFile(qpB("proto/schema.json"))
	testutil.FailErr(t, "ReadFile schema.json", err)
	schemaAttr, err := fx.core.Stat(qpB("proto/schema.json"))
	testutil.FailErr(t, "Stat schema.json", err)
	fx.tracker.RecordExactNativeObservation(qpB("proto/schema.json"), schemaBytes, schemaAttr)

	binding := fx.tracker.BindVerificationReceipt("rcpt-1", "ckpt-100", []string{"root-a", "root-b"})
	if binding.ReadAttributionCompleteness != CompletenessComplete {
		t.Fatalf("expected complete attribution, got %s", binding.ReadAttributionCompleteness)
	}

	// 1. Unrelated trunk change outside observed set with complete attribution preserves receipt freshness.
	writeTrunkInPlace(t, fx.trunkA, "docs/unrelated.md", []byte("# docs v2\n"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qpA("docs/unrelated.md"), Kind: TrunkWriteInPlace})

	eval1 := fx.tracker.EvaluateReceiptFreshness(binding, "ckpt-100")
	if eval1.Stale {
		t.Fatalf("expected receipt to remain fresh after unrelated change outside complete observed set, got %+v", eval1)
	}

	// 2. Cross-directory dependency change (internal/shared/helper.go) invalidates receipt.
	writeTrunkInPlace(t, fx.trunkA, "internal/shared/helper.go", []byte("package shared // v2-breaking\n"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qpA("internal/shared/helper.go"), Kind: TrunkWriteInPlace})

	eval2 := fx.tracker.EvaluateReceiptFreshness(binding, "ckpt-100")
	if !eval2.Stale || eval2.StaleReason != StaleReasonObservedDependencyChanged {
		t.Fatalf("expected cross-directory observed dependency change to invalidate receipt, got %+v", eval2)
	}
	if len(eval2.InvalidatingPaths) != 1 || eval2.InvalidatingPaths[0] != qpA("internal/shared/helper.go") {
		t.Fatalf("unexpected invalidating paths: %+v", eval2.InvalidatingPaths)
	}

	// 3. Cross-root dependency change (root-b:proto/schema.json) also invalidates receipt.
	writeTrunkAtomicReplace(t, fx.trunkB, "proto/schema.json", []byte(`{"version":2}`))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qpB("proto/schema.json"), Kind: TrunkAtomicReplace})

	eval3 := fx.tracker.EvaluateReceiptFreshness(binding, "ckpt-100")
	if !eval3.Stale || len(eval3.InvalidatingPaths) != 2 {
		t.Fatalf("expected both cross-dir and cross-root invalidating paths, got %+v", eval3)
	}

	// 4. Checkpoint mismatch invalidates receipt.
	evalCkpt := fx.tracker.EvaluateReceiptFreshness(binding, "ckpt-101")
	if !evalCkpt.Stale || evalCkpt.StaleReason != StaleReasonCheckpointMismatch {
		t.Fatalf("expected checkpoint mismatch staleness, got %+v", evalCkpt)
	}
}

func TestIncompleteAttributionAndScopeGenerationValidity(t *testing.T) {
	t.Parallel()
	// NFS or kernel-cached mount where server request log is not a complete read set.
	facts := LiveMountVisibilityFacts("nfs-loopback", false, false, false, CompletenessPartialKernelCached)
	fx := newTestFixture(t, facts)

	qpA := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-a", Rel: rel}
	}
	qpB := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-b", Rel: rel}
	}

	writeTrunkInPlace(t, fx.trunkA, "pkg/observed.go", []byte("package pkg\n"))
	writeTrunkInPlace(t, fx.trunkA, "pkg/kernel_cached_unlogged.go", []byte("package pkg // cached\n"))
	writeTrunkInPlace(t, fx.trunkB, "other/external.go", []byte("package other\n"))

	_, err := fx.core.ReadFile(qpA("pkg/observed.go"))
	testutil.FailErr(t, "ReadFile observed.go", err)

	// Bind receipt scoped to root-a only.
	binding := fx.tracker.BindVerificationReceipt("rcpt-partial", "ckpt-200", []string{"root-a"})
	if binding.ReadAttributionCompleteness != CompletenessPartialKernelCached {
		t.Fatalf("expected CompletenessPartialKernelCached, got %s", binding.ReadAttributionCompleteness)
	}

	// Change in root-b (outside declared EnclosingScopeRoots) does not invalidate root-a receipt.
	writeTrunkInPlace(t, fx.trunkB, "other/external.go", []byte("package other // v2\n"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qpB("other/external.go"), Kind: TrunkWriteInPlace})

	evalOutside := fx.tracker.EvaluateReceiptFreshness(binding, "ckpt-200")
	if evalOutside.Stale {
		t.Fatalf("expected change outside declared scope root-a to preserve freshness, got %+v", evalOutside)
	}

	// Change to an unlogged file inside root-a advances root-a's ScopeSourceGeneration.
	// Because attribution is incomplete, EvaluateReceiptFreshness must NOT certify freshness!
	writeTrunkInPlace(t, fx.trunkA, "pkg/kernel_cached_unlogged.go", []byte("package pkg // cached v2\n"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qpA("pkg/kernel_cached_unlogged.go"), Kind: TrunkWriteInPlace})

	evalInside := fx.tracker.EvaluateReceiptFreshness(binding, "ckpt-200")
	if !evalInside.Stale || evalInside.StaleReason != StaleReasonIncompleteAttributionScopeAdvanced {
		t.Fatalf("expected incomplete attribution with advanced scope generation to mark stale, got %+v", evalInside)
	}
}

func TestTrunkChangedNoticeAndPromotionPreviewHintsContract(t *testing.T) {
	t.Parallel()
	facts := LiveMountVisibilityFacts("fuse-linux", true, true, true, CompletenessComplete)
	fx := newTestFixture(t, facts)
	qpA := func(rel string) layeredfs.QualifiedPath {
		return layeredfs.QualifiedPath{RootID: "root-a", Rel: rel}
	}

	writeTrunkInPlace(t, fx.trunkA, "api/spec.yaml", []byte("version: 1\n"))
	v1Digest := sha256Hex([]byte("version: 1\n"))
	_, err := fx.core.ReadFile(qpA("api/spec.yaml"))
	testutil.FailErr(t, "ReadFile api/spec.yaml", err)

	binding := fx.tracker.BindVerificationReceipt("rcpt-preview", "ckpt-300", []string{"root-a"})

	writeTrunkInPlace(t, fx.trunkA, "api/spec.yaml", []byte("version: 2\n"))
	v2Digest := sha256Hex([]byte("version: 2\n"))
	fx.tracker.RecordTrunkChange(TrunkChangeEvent{Path: qpA("api/spec.yaml"), Kind: TrunkWriteInPlace})

	notice, ok := fx.tracker.ConsumeTrunkChangedNotice()
	if !ok || notice == nil {
		t.Fatalf("expected pending trunk.changed notice")
	}
	if notice.EventType != TrunkChangedEventType {
		t.Fatalf("expected EventType %q, got %q", TrunkChangedEventType, notice.EventType)
	}
	if notice.GrantsAuthority || notice.EstablishesMergeBase {
		t.Fatalf("trunk.changed notice must never grant authority or establish merge base: %+v", notice)
	}
	if len(notice.AffectedPaths) != 1 || notice.AffectedPaths[0] != qpA("api/spec.yaml") || len(notice.ObservationRefs) == 0 {
		t.Fatalf("unexpected affected paths or refs in notice: %+v", notice)
	}
	if _, stillPending := fx.tracker.PendingTrunkChangedNotice(); stillPending {
		t.Fatalf("expected ConsumeTrunkChangedNotice to clear pending notice")
	}

	hints := fx.tracker.BuildPromotionPreviewHintsForBinding(binding)
	if len(hints) == 0 {
		t.Fatalf("expected stale observation promotion preview hints")
	}
	for _, h := range hints {
		if h.SubstitutesExactBaseCheck {
			t.Fatalf("promotion preview hint must never substitute exact base conflict check: %+v", h)
		}
		if h.Path != qpA("api/spec.yaml") || h.ObservedSHA256 != v1Digest || h.CurrentSHA256 != v2Digest {
			t.Fatalf("unexpected hint content: %+v", h)
		}
	}
}
