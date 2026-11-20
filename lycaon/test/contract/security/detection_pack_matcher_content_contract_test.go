package contract

import (
	"testing"

	"github.com/lycaon/lycaon/internal/detectionpack"
)

// TestDetectionPackRetainedPreciseRulesMatch pins the point-rule table.
func TestDetectionPackRetainedPreciseRulesMatch(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	m := detectionpack.NewMatcher(cat)
	cases := []struct {
		cmd, pack, slug string
		level           detectionpack.Level
	}{
		{"aws organizations leave-organization", "aws-cli", "organizations-leave", detectionpack.LevelCritical},
		{"aws s3 rb s3://b", "aws-cli", "s3-remove-bucket", detectionpack.LevelHigh},
		{"az group delete -n my-rg", "azure-cli", "group-delete", detectionpack.LevelCritical},
		{"gcloud projects delete my-old-project", "gcloud-cli", "project-delete", detectionpack.LevelCritical},
		{"terraform destroy -auto-approve", "terraform-cli", "destroy", detectionpack.LevelCritical},
		{"git push --force origin main", "publish-release", "git-force-push", detectionpack.LevelHigh},
	}
	for _, tc := range cases {
		hit, ok := m.Match(contractToolEvent("command", tc.cmd, "/p", true, "proxy", "s"))
		if !ok {
			t.Fatalf("%q: no match", tc.cmd)
		}
		if hit.PackID != tc.pack || hit.Level != tc.level {
			t.Fatalf("%q: hit=%+v want pack=%s level=%s", tc.cmd, hit, tc.pack, tc.level)
		}
		p, _ := cat.PackByID(tc.pack)
		var found bool
		for _, r := range p.Rules {
			if r.Slug == tc.slug && r.Matches(contractToolEvent("command", tc.cmd, "/p", true, "proxy", "s")) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%q: expected slug %s to match", tc.cmd, tc.slug)
		}
	}
}

// TestDetectionPackReadOnlySilent re-pins the silent read-only corpus across provider CLIs.
func TestDetectionPackReadOnlySilent(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	m := detectionpack.NewMatcher(cat)
	cmds := []string{
		"aws ec2 describe-instances",
		"aws s3 ls",
		"aws sts get-caller-identity",
		"aws iam list-users",
		"aws rds describe-db-instances",
		"aws dynamodb list-tables",
		"aws logs describe-log-groups",
		"aws lambda list-functions",
		"aws eks describe-cluster --name x",
		"aws ecs list-clusters",
		"aws --version",
		"aws help",
		"az vm list",
		"az group list",
		"az account show",
		"az aks list",
		"az storage account list",
		"az webapp list",
		"az network vnet list",
		"az sql server list",
		"az cosmosdb list",
		"az monitor metrics list --resource x",
		"az --version",
		"gcloud compute instances list",
		"gcloud container clusters list",
		"gcloud sql instances list",
		"gcloud storage buckets list",
		"gcloud functions list",
		"gcloud run services list",
		"gcloud spanner instances list",
		"gcloud pubsub topics list",
		"gcloud logging sinks list",
		"gcloud projects describe my-proj",
		"gcloud --version",
		"kubectl get pods",
		"kubectl get nodes",
		"kubectl describe deployment x",
		"kubectl logs pod/x",
		"kubectl top nodes",
		"kubectl explain pod",
		"kubectl get svc",
		"kubectl get ns",
		"oc get pods",
		"kubecolor get pods",
		"heroku apps",
		"fly status",
		"vercel ls",
		"stripe products list",
		"terraform plan",
		"aws s3api list-buckets",
		"az policy assignment list",
	}
	if len(cmds) < 40 {
		t.Fatalf("need ≥40 read-only cmds, got %d", len(cmds))
	}
	for _, cmd := range cmds {
		if hit, ok := m.Match(contractToolEvent("command", cmd, "/p", true, "proxy", "s")); ok {
			t.Errorf("read-only %q matched %+v", cmd, hit)
		}
	}
}

// TestDetectionPackMultiStageMatchesSingleStage pins per-stage image/ApiAction resolution.
func TestDetectionPackMultiStageMatchesSingleStage(t *testing.T) {
	t.Parallel()
	cat := loadBundledDetectionCatalog(t)
	m := detectionpack.NewMatcher(cat)

	single := "terraform destroy -auto-approve"
	multi := "cd infra && terraform destroy -auto-approve"
	h1, ok1 := m.Match(contractToolEvent("command", single, "/p", true, "proxy", "s"))
	h2, ok2 := m.Match(contractToolEvent("command", multi, "/p", true, "proxy", "s"))
	if !ok1 || !ok2 || h1.Level != h2.Level || h1.RuleID != h2.RuleID {
		t.Fatalf("terraform multi-stage: single=%+v/%v multi=%+v/%v", h1, ok1, h2, ok2)
	}

	single2 := "aws s3 rb s3://b"
	multi2 := "cd infra && aws s3 rb s3://b"
	a1, ok1 := m.Match(contractToolEvent("command", single2, "/p", true, "proxy", "s"))
	a2, ok2 := m.Match(contractToolEvent("command", multi2, "/p", true, "proxy", "s"))
	if !ok1 || !ok2 || a1.Level != a2.Level || a1.RuleID != a2.RuleID {
		t.Fatalf("aws multi-stage: single=%+v/%v multi=%+v/%v", a1, ok1, a2, ok2)
	}
}
