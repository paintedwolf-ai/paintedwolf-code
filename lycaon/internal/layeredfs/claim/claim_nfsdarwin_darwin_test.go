//go:build darwin

package claim

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter/nfsdarwin"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRealNFSDarwinMountedClaimZeroLowerDirReads(t *testing.T) {
	tmp := t.TempDir()
	root1 := filepath.Join(tmp, "r1")
	root2 := filepath.Join(tmp, "r2")
	for _, d := range []string{root1, root2} {
		testutil.FailErr(t, "mkdir", os.MkdirAll(d, 0o755))
	}
	for i := 0; i < 200; i++ {
		testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(root1, fmt.Sprintf("f_%03d.txt", i)), []byte("hello\n"), 0o644))
	}
	testutil.FailErr(t, "write r2", os.WriteFile(filepath.Join(root2, "doc.md"), []byte("doc\n"), 0o644))

	nfsAdp := nfsdarwin.New(nfsdarwin.Config{
		ConfinementCheckFn: func(context.Context, adapter.ProbeConfig) error { return nil },
	})
	sel := adapter.NewSelector("darwin", nil, nfsAdp, adapter.NewEagerCopyAdapter(adapter.ReasonNone))
	coord := NewCoordinator(sel)
	ret := newTestRetentionRegistrar()

	cv, snap, err := coord.ClaimWorkerView(t.Context(), ClaimRequest{
		Identity: ClaimIdentity{WorkerID: "w-nfs", ProjectID: "proj-nfs", ViewID: "v-nfs", SourceGeneration: 1},
		Storage:  ClaimStorage{RuntimeDir: filepath.Join(tmp, "rt"), BackingDir: filepath.Join(tmp, "bk")},
		Grants: []adapter.RootGrant{
			{RootID: "primary", Label: "primary", SourcePath: root1, IsPrimary: true},
			{RootID: "docs", Label: "docs", SourcePath: root2},
		},
		Hooks: ClaimHooks{Retention: ret, VerifyConfinement: passingConfinement},
	})
	testutil.FailErr(t, "ClaimWorkerView real nfsdarwin", err)
	defer func() { _ = coord.ReleaseWorkerView(context.Background(), "v-nfs", ret) }()

	if snap.Adapter.AdapterID != adapter.AdapterNFSDarwin || snap.Readiness != StateReady || snap.GitInit != GitInitLazyPending {
		t.Fatalf("expected ready nfsdarwin claim with lazy git init, got %+v", snap)
	}
	if st := cv.Prepared.Core.Stats(); st.LowerDirReads != 0 {
		t.Fatalf("expected 0 LowerDirReads during real nfsdarwin claim, got %+v", st)
	}
}
