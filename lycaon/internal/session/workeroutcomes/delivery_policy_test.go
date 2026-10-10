package workeroutcomes_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"github.com/lycaon/lycaon/internal/extpacks"
	"github.com/lycaon/lycaon/internal/oar"
	"github.com/lycaon/lycaon/internal/people"
	"github.com/lycaon/lycaon/internal/session/policyfacts"
	"github.com/lycaon/lycaon/internal/session/policyfeedback"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestProf10WorkerFinalizationGatesSuccessfulAndFailedEnvelopes(t *testing.T) {
	testutil.FailErr(t, "install anchor catalogue", anchorcatalog.InstallBundled())
	for _, effect := range []string{"block", "warn"} {
		for _, status := range []string{"complete", "partial", "failed"} {
			t.Run(effect+"/"+status, func(t *testing.T) {
				dir := t.TempDir()
				document := "oar: '1.0'\nid: DELIVERY_POLICY\nkind: policy\nanchor: agent.finalize\nrequires:\n  profiles: [content, session]\nwhen: content_length > 0 && principal == \"person\"\neffect: " + effect + "\non_fire: [increment_counter]\n"
				testutil.FailErr(t, "write delivery policy", os.WriteFile(filepath.Join(dir, "arbitrary.yaml"), []byte(document), 0o600))
				loader, err := oar.NewLoader("")
				testutil.FailErr(t, "create loader", err)
				rules, err := loader.LoadDir(extpacks.OnDisk(dir))
				testutil.FailErr(t, "load finalization policy", err)
				pipeline := oar.NewGuardPipeline(rules, loader, nil)
				pipeline.EnableAnchor(oar.AnchorWorkerFinalize)
				deliveryPolicy := workeroutcomes.NewDeliveryPolicy(workeroutcomes.DeliveryPolicyPorts{Pipeline: pipeline, Facts: policyfacts.New(nil), Feedback: policyfeedback.New()})
				env := workercompletion.WorkerCompletionEnvelope{
					JobID: "job", ChildSessionID: "child", AgentType: "implement", State: status,
					Summary: "original-secret-summary", Body: "original-secret-body", Digest: "original-secret-digest",
					Report: workercompletion.WorkerCompletionReport{Brief: "original-secret-report"},
					Proof:  workercompletion.WorkerCompletionProof{ChangedPaths: []string{"original-secret-path"}},
				}
				ctx := people.WithCaller(t.Context(), people.Person{ID: "person", Role: api.PersonRoleOwner})
				delivery, err := deliveryPolicy.Evaluate(ctx, env)
				testutil.FailErr(t, "evaluate worker delivery", err)
				body := workercompletion.FormatWorkerCompletionEnvelope(delivery.Envelope)
				if !strings.Contains(body, "DELIVERY_POLICY") {
					t.Fatalf("[OAR-PROF-10] policy feedback missing: %s", body)
				}
				if effect == "block" {
					if !delivery.Blocked || strings.Contains(body, "original-secret") || delivery.Envelope.State != "partial" {
						t.Fatalf("[OAR-PROF-10] blocked content delivered: %+v", delivery)
					}
				} else if delivery.Blocked || !strings.Contains(body, "original-secret-report") || delivery.Envelope.State != status {
					t.Fatalf("[OAR-EVAL-8] advisory altered delivery: %+v", delivery)
				}
				if count := pipeline.Counters().Get("child", "DELIVERY_POLICY", oar.CounterFire); count != 1 {
					t.Fatalf("[OAR-FIRE-6] finalization fired %d times", count)
				}
			})
		}
	}
}
