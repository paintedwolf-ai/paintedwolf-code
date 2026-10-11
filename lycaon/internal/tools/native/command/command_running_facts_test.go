package command

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
)

// Promoted results retain observed network facts.
func TestPromotedCommandStatesWhatItReachedAndUnderWhatBoundary(t *testing.T) {
	outcome := RunOutcome{
		Handle:         "h-1",
		Boundary:       confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
		NetworkPosture: "observe",
		Network: []confine.EgressHost{
			{Host: "api.osv.dev", Port: 443, Allowed: true, Attempts: 1},
			{Host: "api.github.com", Port: 443, Allowed: false, Attempts: 1},
		},
	}

	encoded, err := EncodeCommandRunning(tools.ToolContext{}, outcome, 30*time.Second)
	if err != nil {
		testutil.FailErr(t, "encode running result", err)
	}
	var result hostcmd.CommandRunningResult
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		testutil.FailErr(t, "unmarshal running result", err)
	}

	if !result.Running || result.Handle != "h-1" || result.WaitedMs != 30000 {
		t.Fatalf("promotion facts changed: %#v", result)
	}
	if len(result.Network) != 2 {
		t.Fatalf("network verdicts = %#v", result.Network)
	}
	var refused bool
	for _, host := range result.Network {
		if host.Host == "api.github.com" && !host.Allowed {
			refused = true
		}
	}
	if !refused {
		t.Fatalf("a refused destination did not survive promotion: %#v", result.Network)
	}
	if !result.Confined || result.NetworkMode == "" {
		t.Fatalf("confinement report missing from a promoted run: %#v", result.Report)
	}
}

// Every command result states its boundary.
func TestEveryCommandResultShapeStatesItsBoundary(t *testing.T) {
	outcome := RunOutcome{
		Boundary:       confine.Boundary{Applied: true, Network: confine.NetworkProxyOnly},
		NetworkPosture: "observe",
	}
	report := CommandConfinementReport(outcome.Boundary, outcome.NetworkPosture, confine.LocalNetworkGrant{}, nil)
	if !report.Confined || report.NetworkMode == "" || report.NetworkPosture != "observe" {
		t.Fatalf("shared report is incomplete: %#v", report)
	}

	finished := CommandResultFromOutcome(outcome, confine.LocalNetworkGrant{}, nil)
	if !reflect.DeepEqual(finished.Report, report) {
		t.Fatalf("finished result report = %#v, want %#v", finished.Report, report)
	}
}

func TestCommandToolRequiresRunner(t *testing.T) {
	tool := &CommandTool{}
	_, err := tool.Run(context.Background(), map[string]any{"command": "echo hi"}, tools.ToolContext{})
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("err = %v", err)
	}
}
