//go:build darwin || linux

package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/authzcontext"
	"github.com/lycaon/lycaon/internal/detectionpack"
	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

const (
	sigmaWiringEgressRuleID = "6a0f3c52-8f4e-4c1b-9d7e-2b5a1c9e7f30"
	sigmaWiringEgressHost   = "detect-fixture.example.test"
)

// sigmaWiringInstallDetections writes a device detection pack with one egress
// rule and republishes the matcher.
func sigmaWiringInstallDetections(t *testing.T, s *commandProgramSession) {
	t.Helper()
	dir := filepath.Join(detectionpack.DevicePacksDir(filepath.Dir(s.h.DatabasePath())), "wiring-fixture")
	testutil.FailErr(t, "create detection pack", os.MkdirAll(filepath.Join(dir, "rules"), 0o700))
	files := map[string]string{
		"pack.yaml": "id: wiring-fixture\nlabel: Wiring fixture\ndescription: Fixture rules for wiring tests.\n",
		"rules/egress.yml": "title: Fixture egress\nid: " + sigmaWiringEgressRuleID + "\ndescription: d\n" +
			"logsource: {product: lycaon, service: egress_observed}\nlevel: critical\n" +
			"detection:\n  sel:\n    DestinationHostname: '" + sigmaWiringEgressHost + "'\n  condition: sel\n",
	}
	for name, body := range files {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
	}
	s.h.Server.Admin.Scan.ReloadDetectionPacks(t.Context())
}

func sigmaWiringActions(rows []visibilityAuthzRow) map[authzcontext.EventAction]int {
	out := map[authzcontext.EventAction]int{}
	for _, row := range rows {
		out[row.Action]++
	}
	return out
}

// A mediated HTTP request to a host an egress rule names raises the detection
// card before any dial: the host does not resolve, so only the pre-dial match
// can raise it.
func TestNonHTTPContractHTTPSocksStillReachPreDialMatch(t *testing.T) {
	s := newProgramHost(t).program(t, "curl -s --max-time 5 http://"+sigmaWiringEgressHost+"/", "")
	sigmaWiringInstallDetections(t, s)
	cards := runCommandWithApprovals(t, s, "decline")
	detection := cards[0].ToolApproval.Plan.Presentation.Detection
	if detection == nil || detection.PackID != "wiring-fixture" || detection.RuleID != sigmaWiringEgressRuleID {
		t.Fatalf("card does not cite the egress rule: %+v", cards[0].ToolApproval.Plan.Presentation)
	}
}

// Detection resolution and capability grants are separate ledger records.
func TestNonHTTPContractCapabilityAndDetectionRemainSeparateRecords(t *testing.T) {
	detected := newProgramHost(t).program(t, "curl -s --max-time 5 http://"+sigmaWiringEgressHost+"/", "")
	sigmaWiringInstallDetections(t, detected)
	cards := runCommandWithApprovals(t, detected, "recommended")
	if d := cards[0].ToolApproval.Plan.Presentation.Detection; d == nil || d.RuleID != sigmaWiringEgressRuleID {
		t.Fatalf("card does not cite the egress rule: %+v", cards[0].ToolApproval.Plan.Presentation)
	}
	actions := sigmaWiringActions(visibilityAuthzRows(t, detected))
	if actions[authzcontext.EventActionDetectionResolved] != 1 ||
		actions[authzcontext.EventActionCapabilityGranted] != 0 || actions[authzcontext.EventActionCapabilityApplied] != 0 {
		t.Fatalf("detection session actions = %v, want one detection_resolved and no capability rows", actions)
	}

	path, _ := serveDaemonSocket(t)
	granted := newProgramHost(t).programWithArgs(t, map[string]any{
		"command": "curl -s --max-time 5 --unix-socket '" + path + "' http://localhost/version",
	}, wire.CreateSessionRequest{}, "")
	runCommandWithApprovals(t, granted, "recommended")
	actions = sigmaWiringActions(visibilityAuthzRows(t, granted))
	if actions[authzcontext.EventActionCapabilityGranted] != 1 || actions[authzcontext.EventActionDetectionResolved] != 0 {
		t.Fatalf("capability session actions = %v, want one capability_granted and no detection_resolved", actions)
	}
}
