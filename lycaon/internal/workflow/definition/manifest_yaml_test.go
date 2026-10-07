package definition

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestParseManifestYAMLAgentToolAccess(t *testing.T) {
	data := []byte(`
id: hotfix
version: 1.0.0
agents:
  - { id: implementer, tools: all }
  - { id: code-reviewer, tools: profile }
phases:
  - id: fix
    activity_label: Test phase
`)
	m, err := ParseManifestYAML(data)
	testutil.FailErr(t, "ParseManifestYAML failed", err)
	want := []string{"implementer", "code-reviewer"}
	if len(m.AllowedAgents) != len(want) {
		t.Fatalf("allowed_agents = %v want %v", m.AllowedAgents, want)
	}
	for i, id := range want {
		if m.AllowedAgents[i] != id {
			t.Fatalf("allowed_agents[%d] = %q want %q", i, m.AllowedAgents[i], id)
		}
	}
	if m.AgentToolAccess["implementer"] != "all" || m.AgentToolAccess["code-reviewer"] != "profile" {
		t.Fatalf("agent tool access = %v", m.AgentToolAccess)
	}
}

func TestParseManifestYAMLRejectsUnknownFields(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
unexpected: true
`))
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("err = %v", err)
	}
}

func TestParseManifestYAMLRequiresCanonicalSemanticVersion(t *testing.T) {
	_, err := ParseManifestYAML([]byte("id: bad\nversion: next\n"))
	if err == nil || !strings.Contains(err.Error(), "canonical semantic version") {
		t.Fatalf("err = %v, want semantic version rejection", err)
	}
}

func TestParseManifestYAMLRequest(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: security
version: 1.0.0
request:
  question: What should the security review focus on?
  default: Review the project according to its threat model.
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	if m.Request == nil {
		t.Fatal("request is nil")
	}
	if m.Request.Cadence != RequestCadenceOnce {
		t.Fatalf("cadence = %q want once", m.Request.Cadence)
	}
	if m.Request.Question != "What should the security review focus on?" {
		t.Fatalf("question = %q", m.Request.Question)
	}
	if m.Request.Default != "Review the project according to its threat model." {
		t.Fatalf("default = %q", m.Request.Default)
	}

	raw, err := MarshalManifestYAML(m)
	testutil.FailErr(t, "MarshalManifestYAML", err)
	reloaded, err := ParseManifestYAML([]byte(raw))
	testutil.FailErr(t, "ParseManifestYAML round trip", err)
	if reloaded.Request == nil || *reloaded.Request != *m.Request {
		t.Fatalf("round-trip request = %#v want %#v\nyaml:\n%s", reloaded.Request, m.Request, raw)
	}
}

func TestParseManifestYAMLRequestEachTurn(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: implement
version: 1.0.0
request:
  cadence: each_turn
  question: What would you like to work on?
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	if m.Request == nil || m.Request.Cadence != RequestCadenceEachTurn {
		t.Fatalf("request = %#v", m.Request)
	}
}

func TestParseManifestYAMLRejectsInvalidRequest(t *testing.T) {
	for name, tt := range map[string]struct {
		request string
		want    string
	}{
		"missing question": {request: "cadence: once", want: "request.question required"},
		"invalid cadence":  {request: "cadence: sometimes\n  question: What next?", want: "request.cadence must be once or each_turn"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifestYAML([]byte("id: bad\nversion: 1.0.0\nrequest:\n  " + tt.request + "\n"))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v want containing %q", err, tt.want)
			}
		})
	}
}

func TestResolveAllManifestsRequiresRequestWithoutTrigger(t *testing.T) {
	manifest, err := ParseManifestYAML([]byte(`
id: direct-start
version: 1.0.0
phases:
  - id: done
    activity_label: Done
    terminal: true
    complete_when: orchestration_complete
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	_, err = ResolveAllManifests(map[string]Manifest{"direct-start@1.0.0": manifest})
	if err == nil || !strings.Contains(err.Error(), "request required") {
		t.Fatalf("err = %v want request requirement", err)
	}
}

func TestParseManifestYAMLValidatesParameterDefinitions(t *testing.T) {
	for name, parameter := range map[string]string{
		"unsupported type": "type: string\n    default: value",
		"invalid depth":    "type: depth\n    default: extreme",
		"invalid boolean":  "type: boolean\n    default: sometimes",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifestYAML([]byte("id: bad\nversion: 1.0.0\nparameters:\n  mode:\n    " + parameter + "\n"))
			if err == nil || !strings.Contains(err.Error(), "parameter \"mode\"") {
				t.Fatalf("err = %v, want parameter validation error", err)
			}
		})
	}
}

func TestParseManifestYAMLRequiresExplicitAgentTools(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
agents:
  - id: implementer
`))
	if err == nil || !strings.Contains(err.Error(), "tools required") {
		t.Fatalf("err = %v, want explicit tools rejection", err)
	}
}

func TestParseManifestYAMLRequiresActivityLabel(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: work
`))
	if err == nil || !strings.Contains(err.Error(), "activity_label required") {
		t.Fatalf("err = %v, want activity_label rejection", err)
	}
}

func TestParseManifestYAMLOnReenter(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: implement
version: 1.0.0
phases:
  - id: work
    activity_label: Test phase
    on_reenter:
      inject_kick: worker.task.finished
      reenter_leg: "implement-work:{session_id}"
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	work, ok := m.PhaseByID("work")
	if !ok {
		t.Fatal("missing work phase")
	}
	if work.OnReenter.InjectKick != "worker.task.finished" {
		t.Fatalf("inject_kick = %q", work.OnReenter.InjectKick)
	}
}

func TestParseManifestYAMLPhaseAdvanceHost(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: implement
version: 1.0.0
controls:
  phase_advance: host
phases:
  - id: boot
    activity_label: Test phase
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	if m.Controls.PhaseAdvance != PhaseAdvanceHost {
		t.Fatalf("phase_advance = %q want host", m.Controls.PhaseAdvance)
	}
}

func TestParseManifestYAMLReportEnabled(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: survey
version: 1.0.0
controls:
  report:
    enabled: true
phases:
  - id: boot
    activity_label: Test phase
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	if !m.ReportEnabled() {
		t.Fatal("report is disabled")
	}
	if m.Controls.Report == nil || !m.Controls.Report.Enabled {
		t.Fatalf("Controls.Report = %#v want Enabled true", m.Controls.Report)
	}
}

func TestParseManifestYAMLReportAbsent(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: implement
version: 1.0.0
controls:
  phase_advance: host
phases:
  - id: boot
    activity_label: Test phase
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	if m.ReportEnabled() {
		t.Fatal("report is enabled without controls.report")
	}
	if m.Controls.Report != nil {
		t.Fatalf("Controls.Report = %#v want nil", m.Controls.Report)
	}
}

func TestParseManifestYAMLCatalogFields(t *testing.T) {
	data := []byte(`
id: plan
version: 1.0.0
name: Plan workflow
description: Draft and approve a plan
trigger: /plan
initial_posture: spec
phases:
  - id: stub
    activity_label: Test phase
  - id: research
    activity_label: Test phase
`)
	m, err := ParseManifestYAML(data)
	testutil.FailErr(t, "ParseManifestYAML failed", err)
	if m.Trigger != "/plan" {
		t.Fatalf("trigger = %q", m.Trigger)
	}
	if m.InitialPosture != "spec" {
		t.Fatalf("initial_posture = %q", m.InitialPosture)
	}
	if m.Name != "Plan workflow" {
		t.Fatalf("name = %q", m.Name)
	}
	if m.Description != "Draft and approve a plan" {
		t.Fatalf("description = %q", m.Description)
	}
	if len(m.Phases) != 2 || m.Phases[1] != "research" {
		t.Fatalf("phases = %v", m.Phases)
	}
}

func TestManifestSummaryDefaultsName(t *testing.T) {
	m := Manifest{ID: "plan", Version: "1.0.0"}
	s := m.Summary()
	if s.Name != "plan" {
		t.Fatalf("name = %q", s.Name)
	}
}

func TestManifestSummaryReportEnabled(t *testing.T) {
	on := Manifest{
		ID: "survey", Version: "1.0.0",
		Controls: ManifestControls{Report: &ReportControls{Enabled: true}},
	}.Summary()
	if !on.ReportEnabled {
		t.Fatal("ReportEnabled = false want true")
	}
	off := Manifest{ID: "implement", Version: "1.0.0"}.Summary()
	if off.ReportEnabled {
		t.Fatal("ReportEnabled = true without report controls")
	}
}

func TestRegistryFromDirsPlan(t *testing.T) {
	reg, err := RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs failed", err)
	entries := reg.All()
	m, ok := entries[ManifestKey("plan", "1.0.0")]
	if !ok {
		t.Fatal("missing plan@1.0.0")
	}
	if m.Trigger != "/plan" {
		t.Fatalf("trigger = %q", m.Trigger)
	}
	if m.SurfaceProfile != "plan" {
		t.Fatalf("surface_profile = %q want plan", m.SurfaceProfile)
	}
	if m.AgentToolAccess["coordinator"] != "all" {
		t.Fatalf("coordinator tools = %q want all", m.AgentToolAccess["coordinator"])
	}
	for _, id := range m.AllowedAgents {
		if id == "coordinator" {
			t.Fatal("root coordinator declaration must not make coordinator spawnable")
		}
	}
}

func TestLoadManifestsRawDuplicateFatal(t *testing.T) {
	dir := t.TempDir()
	content := "id: x\nversion: 1.0.0\nphases:\n  - id: a\n"
	for _, feature := range []string{"dup", "dup2"} {
		featureDir := filepath.Join(dir, feature)
		if err := os.MkdirAll(featureDir, 0o755); err != nil {
			testutil.FailErr(t, "create feature directory", err)
		}
		if err := os.WriteFile(filepath.Join(featureDir, "workflow.yaml"), []byte(content), 0o644); err != nil {
			testutil.FailErr(t, "write file", err)
		}
	}
	if _, err := LoadManifestsRaw(dir); err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestParseManifestYAMLDefaultExecutionMode(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: parallel-only
version: 1.0.0
controls:
  default_execution_mode: orchestrate
phases:
  - id: boot
    activity_label: Test phase
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	if m.Controls.DefaultExecutionMode != ExecutionModeOrchestrate {
		t.Fatalf("default = %q", m.Controls.DefaultExecutionMode)
	}
}

func TestParseManifestYAMLRejectsInvalidDefaultExecutionMode(t *testing.T) {
	_, err := ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
controls:
  default_execution_mode: parallel
phases:
  - id: boot
    activity_label: Test phase
`))
	if err == nil {
		t.Fatal("expected invalid default_execution_mode error")
	}
}

func TestParseManifestYAMLPhaseSetExecutionMode(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: scout-build
version: 1.0.0
phases:
  - id: scout
    activity_label: Test phase
    on_enter:
      set_execution_mode: investigate
  - id: work
    activity_label: Test phase
    on_enter:
      set_execution_mode: orchestrate
`))
	testutil.FailErr(t, "ParseManifestYAML", err)
	scout, ok := m.PhaseByID("scout")
	if !ok || scout.OnEnter.SetExecutionMode != ExecutionModeInvestigate {
		t.Fatalf("scout on_enter = %+v", scout.OnEnter)
	}
}

func TestResolveManifestChainDefaultExecutionModeMerge(t *testing.T) {
	parent, err := ParseManifestYAML([]byte(`
id: implement
version: 1.0.0
controls:
  default_execution_mode: investigate
phases:
  - id: boot
    activity_label: Test phase
  - id: work
    activity_label: Test phase
`))
	testutil.FailErr(t, "parse parent", err)
	child, err := ParseManifestYAML([]byte(`
id: parallel-only
version: 1.0.0
extends: implement@1.0.0
controls:
  default_execution_mode: orchestrate
phases:
  - id: boot
    activity_label: Test phase
`))
	testutil.FailErr(t, "parse child", err)
	catalog := map[string]Manifest{"implement@1.0.0": parent}
	merged, err := ResolveManifestChain(child, catalog)
	testutil.FailErr(t, "ResolveManifestChain", err)
	if merged.Controls.DefaultExecutionMode != ExecutionModeOrchestrate {
		t.Fatalf("merged default = %q want orchestrate", merged.Controls.DefaultExecutionMode)
	}
}

func TestManifestDeclaredIdentifierGrammar(t *testing.T) {
	for _, id := range []string{"stage-2", "review_pass", "1"} {
		m, err := ParseManifestYAML([]byte("id: " + id + "\nversion: 1.0.0\nphases:\n  - id: " + id + "\n    activity_label: Review\n"))
		testutil.FailErr(t, "parse symbolic workflow and phase identifiers", err)
		if m.ID != id || len(m.PhaseDefs) != 1 || m.PhaseDefs[0].ID != id {
			t.Fatalf("declared identity changed: %+v", m)
		}
	}
	for _, id := range []string{"Bad", "two words", "path/name", "name.ext"} {
		for _, body := range []string{
			"id: " + id + "\nversion: 1.0.0\n",
			"id: valid\nversion: 1.0.0\nphases:\n  - id: " + id + "\n    activity_label: Review\n",
		} {
			if _, err := ParseManifestYAML([]byte(body)); err == nil {
				t.Fatalf("accepted invalid symbolic identifier %q", id)
			}
		}
	}
}

func TestParseManifestYAMLControlsRetries(t *testing.T) {
	m, err := ParseManifestYAML([]byte(`
id: custom-flow
version: 1.0.0
controls:
  report:
    enabled: true
    retries: 5
phases:
  - id: step1
    activity_label: Quick step
    controls:
      retries: 1
  - id: report
    activity_label: Deliver report
    gates: [topology_report_delivered]
`))
	testutil.FailErr(t, "ParseManifestYAML with retries", err)
	if m.Controls.Report == nil || m.Controls.Report.Retries != 5 {
		t.Fatalf("expected Controls.Report.Retries = 5, got %+v", m.Controls.Report)
	}
	p1, ok := m.PhaseForRun(nil, "step1")
	if !ok || p1.CloseoutRetries != 1 {
		t.Fatalf("expected step1 CloseoutRetries = 1, got %+v", p1)
	}
	pReport, ok := m.PhaseForRun(nil, "report")
	if !ok || pReport.CloseoutRetries != 5 {
		t.Fatalf("expected report CloseoutRetries = 5 inherited from Controls.Report, got %+v", pReport)
	}
}
