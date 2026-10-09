package session

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProf10PostTurnGatesCompletedTurnOnce(t *testing.T) {
	testutil.FailErr(t, "install anchor catalogue", anchorcatalog.InstallBundled())
	dir := t.TempDir()
	document := "oar: '1.0'\nid: TURN_POLICY\nkind: policy\nanchor: agent.post_turn\nrequires:\n  profiles: [content]\nwhen: content_length > 0\neffect: block\non_fire: [increment_counter]\n"
	testutil.FailErr(t, "write turn policy", os.WriteFile(filepath.Join(dir, "turn.yaml"), []byte(document), 0o600))
	loader, err := oar.NewLoader("")
	testutil.FailErr(t, "create loader", err)
	rules, err := loader.LoadDir(extpacks.OnDisk(dir))
	testutil.FailErr(t, "load turn policy", err)
	pipeline := oar.NewGuardPipeline(rules, loader, nil)
	pipeline.EnableAnchor(oar.AnchorCoordinatorPostTurn)
	mgr, _ := newTestManager(t)
	mgr.SetOARPipeline(pipeline, nil)
	sess := &api.Session{ID: "child", ParentSessionID: "parent"}
	reject, blocked := mgr.Coordinator.Guards.BeforeFinish(t.Context(), sess, nil, "", "assembled prose", "", true, nil, true)
	if !blocked || reject == nil || reject.Code() != "TURN_POLICY" {
		t.Fatalf("[OAR-PROF-10] completed turn bypassed policy: blocked=%v reject=%v", blocked, reject)
	}
	if count := pipeline.Counters().Get(sess.ID, "TURN_POLICY", oar.CounterFire); count != 1 {
		t.Fatalf("[OAR-FIRE-6] completed-turn occurrence fired %d times", count)
	}
}
