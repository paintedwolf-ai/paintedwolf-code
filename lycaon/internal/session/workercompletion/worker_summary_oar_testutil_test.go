package workercompletion_test

import (
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/testutil"
)

var (
	workerTestPipelineOnce sync.Once
	workerTestPipeline     *oar.GuardPipeline
	workerTestPipelineErr  error
)

func workerTestOARPipeline(t *testing.T) *oar.GuardPipeline {
	t.Helper()
	workerTestPipelineOnce.Do(func() {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			workerTestPipelineErr = errString("runtime.Caller failed")
			return
		}
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
		profile := filepath.Join(root, "config", "packs", "painted-wolf", "platform", "host", "anchors", "oar-profile.yaml")
		if err := anchorcatalog.InstallBundled(); err != nil {
			workerTestPipelineErr = err
			return
		}
		if err := oar.InstallCapabilityDocumentFile(profile); err != nil {
			workerTestPipelineErr = err
			return
		}
		schemaDir := filepath.Join(filepath.Dir(root), "schemas")
		loader, err := oar.NewLoader(schemaDir)
		if err != nil {
			workerTestPipelineErr = err
			return
		}
		rs, err := loader.LoadEffectivePolicy()
		if err != nil {
			workerTestPipelineErr = err
			return
		}
		p := oar.NewGuardPipeline(rs, loader, oar.NewCounterStore())
		p.EnableAnchor(oar.AnchorWorkerReportCheck)
		workerTestPipeline = p
	})
	if workerTestPipeline == nil {
		testutil.FailErr(t, "worker test OAR pipeline", workerTestPipelineErr)
	}
	return workerTestPipeline
}

type errString string

func (e errString) Error() string { return string(e) }

func withWorkerPipeline(t *testing.T, in workercompletion.WorkerSummaryEvalInput) workercompletion.WorkerSummaryEvalInput {
	t.Helper()
	in.Pipeline = workerTestOARPipeline(t)
	return in
}
