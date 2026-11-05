package contract

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestZZRenderDump(t *testing.T) {
	out := os.Getenv("ZZ_RENDER_OUT")
	if out == "" {
		out = "/tmp/zz-render"
	}
	bad := regexp.MustCompile(`[^A-Za-z0-9_.-]+`)
	corpus := filepath.Join(out, "corpus")
	_ = os.RemoveAll(corpus)
	_ = os.MkdirAll(corpus, 0o755)
	for _, o := range modelFacingOutputs(t) {
		_ = os.WriteFile(filepath.Join(corpus, bad.ReplaceAllString(o.name, "_")+".md"), []byte(o.text), 0o644)
	}
	root := contractcheck.RepoRoot(t)
	_, ids := coordinatorSurfaceIDs(t)
	for _, id := range ids {
		text := renderCoordinatorTripartiteForRunContext(t, root, api.CoordinatorRunContext{}, &api.Session{Posture: api.SessionPostureBuild}, surface.HostLoopWakeSentinel, nil, id)
		_ = os.WriteFile(filepath.Join(out, "coordinator-"+id+".md"), []byte(text), 0o644)
	}
	prompts.ResetPersonaContractCache()
	cfg, err := prompts.LoadPersonaContract()
	contractcheck.FailErr(t, "persona", err)
	engine := contractPersonaEngine(t)
	for id := range cfg.Agents {
		text, err := prompts.RenderPersona(context.Background(), engine, id, map[string]any{"profile_has_write_tools": true, "profile_has_verify": true})
		contractcheck.FailErr(t, id, err)
		_ = os.WriteFile(filepath.Join(out, "worker-"+id+".md"), []byte(text), 0o644)
	}
}
