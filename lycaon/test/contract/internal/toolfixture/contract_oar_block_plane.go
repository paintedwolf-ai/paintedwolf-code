package toolfixture

import (
	"github.com/lycaon/lycaon/internal/toolfeedback"

	"path/filepath"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolhost"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

var (
	contractOAROnce     sync.Once
	contractOARPipeline *oar.GuardPipeline
	contractOARErr      error
)

// WireContractBlockPlane attaches stock OAR EvaluateBlock + Decision rendering to a
// toolhost runtime (same shape production uses after ApplyGuidanceRejects).
func WireContractBlockPlane(t *testing.T, rt *toolhost.Runtime, rejectFmt *guidance.StaticRejectFormatter) {
	t.Helper()
	if rt == nil || rt.Executor == nil {
		t.Fatal("wireContractBlockPlane: nil runtime executor")
	}
	p := contractStockOARPipeline(t)
	renderer := oar.NewRenderer(rejectFmt, nil)
	bp := &toolfeedback.BlockPlane{Pipeline: p, Renderer: renderer}
	rt.Executor.Rejections.SetBlockPlane(bp)
}

func contractStockOARPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	contractOAROnce.Do(func() {
		root := filepath.Join(contractcheck.RepoRoot(t), "lycaon")
		catalog := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "anchors", "catalog.yaml")
		profile := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
		if err := anchorcatalog.InstallFile(catalog); err != nil {
			contractOARErr = err
			return
		}
		if err := oar.InstallCapabilityDocumentFile(profile); err != nil {
			contractOARErr = err
			return
		}
		schemaDir := filepath.Join(filepath.Dir(root), "schemas")
		loader, err := oar.NewLoader(schemaDir)
		if err != nil {
			contractOARErr = err
			return
		}
		rs, err := loader.LoadEffectivePolicy()
		if err != nil {
			contractOARErr = err
			return
		}
		p := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
		p.EnableAnchor(oar.AnchorToolHandler)
		p.EnableAnchor(oar.AnchorToolRejected)
		p.EnableAnchor(oar.AnchorToolPreInvoke)
		p.EnableAnchor(oar.AnchorCoordinatorPreInvoke)
		p.EnableAnchor(oar.AnchorToolPost)
		contractOARPipeline = p
	})
	if contractOARPipeline == nil {
		testutil.FailErr(t, "contract stock OAR pipeline", contractOARErr)
	}
	return contractOARPipeline
}
