package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/curationctx"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFact21ToolOccurrenceUsesCallerAndActiveProfile(t *testing.T) {
	testutil.FailErr(t, "install catalogue", anchorcatalog.InstallBundled())
	dir := t.TempDir()
	document := `oar: '1.0'
id: CALLER_POLICY
kind: policy
anchor: tool.pre_invoke
requires:
  profiles: [session, tool]
when: principal == "person" && "owner" in principal_roles && permission_profile == "implement" && session_posture == "build"
effect: block
`
	testutil.FailErr(t, "write caller policy", os.WriteFile(filepath.Join(dir, "caller.yaml"), []byte(document), 0o600))
	loader, err := oar.NewLoader("")
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadDir(extpacks.OnDisk(dir))
	testutil.FailErr(t, "load caller policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, nil)
	pipeline.EnableAnchor(oar.AnchorToolPreInvoke)
	bp := &BlockPlane{Pipeline: pipeline}
	ctx := curationctx.WithSession(t.Context(), curationctx.Session{SessionID: "session", Agent: "agent-type", OwnerPersonID: "owner", Posture: "build"})
	ctx = people.WithCaller(ctx, people.Person{ID: "person", Role: api.PersonRoleOwner})
	if err := bp.Evaluate(ctx, oar.AnchorToolPreInvoke, "read", "implement", nil, nil); err == nil {
		t.Fatal("[OAR-FACT-21] caller-scoped policy did not see the real occurrence facts")
	}
	if err := bp.Evaluate(ctx, oar.AnchorToolPreInvoke, "read", "worker_readonly", nil, nil); err != nil {
		t.Fatalf("[OAR-SEL-1] policy crossed permission profiles: %v", err)
	}
}
