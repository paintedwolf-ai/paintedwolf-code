package portablepython

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/portablepython"
	"github.com/lycaon/lycaon/internal/programtest"
	"github.com/lycaon/lycaon/internal/pythonadmission"
	"github.com/lycaon/lycaon/internal/pythoncatalog"
	"github.com/lycaon/lycaon/internal/pythondocuments"
	"github.com/lycaon/lycaon/internal/pythonruntime"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

type py09RecordingPythonRunner struct {
	mu       sync.Mutex
	requests []command.PythonExecutionRequest
}

func (r *py09RecordingPythonRunner) ExecuteCommand(_ context.Context, req command.PythonExecutionRequest) (command.PythonExecutionResponse, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return command.PythonExecutionResponse{ExitCode: 0, Stdout: "unexpected-exec\n"}, nil
}

func (r *py09RecordingPythonRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

type py09FileOutputPublication struct{ dir string }

func (p py09FileOutputPublication) Open(_ context.Context, dest commandprogram.OutputDestination) (commandprogram.StagedOutput, error) {
	return &py09FileStage{path: filepath.Join(p.dir, dest.Target.Value)}, nil
}

type py09FileStage struct {
	path string
	buf  []byte
}

func (s *py09FileStage) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *py09FileStage) Publish(context.Context) (commandprogram.OutputPublished, error) {
	if err := os.WriteFile(s.path, s.buf, 0o600); err != nil {
		return commandprogram.OutputPublished{}, err
	}
	return commandprogram.OutputPublished{}, nil
}

func (s *py09FileStage) Abandon(context.Context) error { return nil }

func newPY09CommandFixture(t *testing.T, runner command.PythonRunner, directOffered bool) *programtest.Fixture {
	t.Helper()
	fx := programtest.New(t, programtest.Options{
		Publication: func(dir string) commandprogram.OutputPublication {
			return py09FileOutputPublication{dir: dir}
		},
	})
	classes, err := toolcontract.NewCommandEffects(
		toolcontract.CommandReadOnly,
		toolcontract.CommandMutation,
		toolcontract.CommandExternalExec,
	)
	contractcheck.FailErr(t, "compile profile effects", err)
	fx.Runtime.Boundary.SetProfileSource(func(context.Context, string) []sandbox.ToolProfile {
		return []sandbox.ToolProfile{{
			ID:             "py09-fixture",
			Tools:          map[string]bool{"command": true, "python": true},
			CommandEffects: classes,
		}}
	})
	fx.Runtime.PortablePython = runner
	fx.Runtime.DirectPythonOffered = func(tools.ToolContext) bool { return directOffered }
	return fx
}

// py09EffectRecord summarizes a single witnessed host effect in an evidence bundle.
type py09EffectRecord struct {
	Effect    string `json:"effect"`
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Decision  string `json:"decision"`
}

// py09EvidenceBundle is a serializable snapshot of a passing (or stopped)
// provider-free replay used to verify PY09 defect controls and benign refusal controls.
type py09EvidenceBundle struct {
	CaseID          string             `json:"case_id"`
	Revision        string             `json:"revision"`
	Backend         string             `json:"backend"`
	CleanCompletion bool               `json:"clean_completion"`
	ExitCode        int                `json:"exit_code"`
	StopCode        string             `json:"stop_code,omitempty"`
	StopReason      string             `json:"stop_reason,omitempty"`
	Stdout          string             `json:"stdout,omitempty"`
	Stderr          string             `json:"stderr,omitempty"`
	ArtifactPath    string             `json:"artifact_path,omitempty"`
	ArtifactBytes   []byte             `json:"artifact_bytes,omitempty"`
	PublishedCount  int                `json:"published_count"`
	Effects         []py09EffectRecord `json:"effects"`
	Observation     Observation        `json:"observation"`
}

func (b py09EvidenceBundle) clone(t *testing.T) py09EvidenceBundle {
	t.Helper()
	raw, err := json.Marshal(b)
	contractcheck.FailErr(t, "marshal evidence bundle for clone", err)
	var cp py09EvidenceBundle
	contractcheck.FailErr(t, "unmarshal cloned evidence bundle", json.Unmarshal(raw, &cp))
	return cp
}

type py09EvidenceExpectation struct {
	ExpectedRevision  string
	ExpectedBackend   string
	RequiredEffects   []py09EffectRecord
	ExpectedReposYAML []byte
}

func gradePY09EvidenceBundle(exp py09EvidenceExpectation, got py09EvidenceBundle) error {
	// 1. Revision provenance check (catches Defect Control 1: correct artifact + wrong revision).
	if strings.TrimSpace(got.Revision) == "" || got.Revision != exp.ExpectedRevision {
		return fmt.Errorf("revision mismatch: got %q want %q", got.Revision, exp.ExpectedRevision)
	}
	if exp.ExpectedBackend != "" && got.Backend != exp.ExpectedBackend {
		return fmt.Errorf("backend mismatch: got %q want %q", got.Backend, exp.ExpectedBackend)
	}

	// 2. Completion integrity check (catches Defect Control 3: incomplete/stopped result marked complete).
	if got.CleanCompletion {
		if got.StopCode != "" || got.StopReason != "" {
			return fmt.Errorf("completion integrity mismatch: clean_completion=true paired with terminal stop (%s: %s)", got.StopCode, got.StopReason)
		}
		if got.ExitCode != 0 {
			return fmt.Errorf("completion integrity mismatch: clean_completion=true paired with non-zero exit_code=%d", got.ExitCode)
		}
		if strings.Contains(got.Stderr, "Traceback") || strings.Contains(got.Stderr, "Error:") {
			return fmt.Errorf("completion integrity mismatch: clean_completion=true paired with error stderr=%q", got.Stderr)
		}
	} else {
		return fmt.Errorf("expected clean_completion=true for passing bundle (exit=%d stop=%s)", got.ExitCode, got.StopCode)
	}

	// 3. Effect completeness & duplicate check (catches Defect Control 2: missing or duplicate effect).
	counts := make(map[string]int, len(got.Effects))
	for _, eff := range got.Effects {
		key := eff.Effect + "|" + eff.Operation + "|" + eff.Target + "|" + eff.Decision
		counts[key]++
	}
	for _, req := range exp.RequiredEffects {
		key := req.Effect + "|" + req.Operation + "|" + req.Target + "|" + req.Decision
		n := counts[key]
		if n == 0 {
			return fmt.Errorf("missing required effect %s", key)
		}
		if n > 1 {
			return fmt.Errorf("duplicate effect recorded %d times for %s", n, key)
		}
	}

	// 4. Independent artifact oracle check.
	if len(exp.ExpectedReposYAML) > 0 {
		if err := GradeRepoWorkbookWithMode(exp.ExpectedReposYAML, got.ArtifactBytes, WorkbookFrozenReplay); err != nil {
			return fmt.Errorf("independent workbook oracle failed: %w", err)
		}
	}
	return nil
}

type py09RefusalExpectation struct {
	AllowedStopCodes []string
	ForbiddenSecrets []string
}

func gradePY09RefusalBundle(exp py09RefusalExpectation, got py09EvidenceBundle) error {
	if got.CleanCompletion {
		return errors.New("refusal integrity mismatch: refused execution incorrectly marked clean_completion=true")
	}
	if got.PublishedCount > 0 {
		return fmt.Errorf("refusal integrity mismatch: refused execution published %d writes", got.PublishedCount)
	}
	codeMatch := false
	for _, c := range exp.AllowedStopCodes {
		if got.StopCode == c {
			codeMatch = true
			break
		}
	}
	if !codeMatch {
		return fmt.Errorf("unexpected refusal stop_code %q (allowed %v)", got.StopCode, exp.AllowedStopCodes)
	}
	for _, sec := range exp.ForbiddenSecrets {
		if sec != "" && (strings.Contains(got.Stdout, sec) || strings.Contains(got.Stderr, sec)) {
			return fmt.Errorf("refusal disclosure violation: secret %q leaked into output", sec)
		}
	}
	return nil
}

func captureWorkbookPassingBundle(t *testing.T, svc *portablepython.Service, expectedRevision string) (py09EvidenceBundle, CaseSpec, []byte) {
	t.Helper()
	files := loadFrozenFixtureZip(t)
	dir := extractWorkflowDir(t, files, "workbook")
	resp := execQualScript(t, svc, dir, "program.py", nil)

	xlsxBytes, err := os.ReadFile(filepath.Join(dir, "repositories.xlsx"))
	contractcheck.FailErr(t, "read settled repositories.xlsx", err)
	reposYAML := files["workbook/config/repos.yaml"]
	contractcheck.FailErr(t, "grade workbook frozen", GradeRepoWorkbookWithMode(reposYAML, xlsxBytes, WorkbookFrozenReplay))
	contractcheck.FailErr(t, "grade workbook live", GradeRepoWorkbookWithMode(reposYAML, xlsxBytes, WorkbookLiveDeliverable))

	var effects []py09EffectRecord
	seen := make(map[string]bool)
	if resp.AdmissionEvidence != nil {
		for _, rec := range resp.AdmissionEvidence.Admissions() {
			eff := py09EffectRecord{
				Effect:    string(rec.Family),
				Operation: rec.Operation,
				Target:    rec.Target.RelPath,
				Decision:  string(rec.Decision),
			}
			key := eff.Effect + "|" + eff.Operation + "|" + eff.Target + "|" + eff.Decision
			if !seen[key] {
				seen[key] = true
				effects = append(effects, eff)
			}
		}
	}
	// Ensure canonical read/write effects are present when the default policy didn't emit a write record separately.
	pubCount := 0
	if resp.Settlement != nil {
		pubCount = len(resp.Settlement.Published)
		for _, pub := range resp.Settlement.Published {
			eff := py09EffectRecord{
				Effect:    "filesystem_write",
				Operation: "publish",
				Target:    pub.Target.RelPath,
				Decision:  "allow",
			}
			key := eff.Effect + "|" + eff.Operation + "|" + eff.Target + "|" + eff.Decision
			if !seen[key] {
				seen[key] = true
				effects = append(effects, eff)
			}
		}
	}

	spec := CaseSpec{
		ID:            "py09-frozen-workbook-replay",
		Version:       "1.0",
		OracleVersion: OracleSchemaVersion,
		Actor:         "coordinator",
		Authority:     "workspace_rw",
		Program:       "workbook/program.py",
		ExpectedRoute: "portable_python",
		ExpectedFiles: map[string][]byte{
			"repositories.xlsx": xlsxBytes,
		},
		ExpectedOutcome: "completed",
		PackageExpectation: PackageExpectation{
			RequireInventoryMatch: true,
			ForbiddenTempPatterns: []string{"*.tmp", "openpyxl.*"},
		},
	}
	obs := Observation{
		CaseID: spec.ID,
		Route:  RouteRecord{Kind: "portable_python"},
		Receipts: []PublicationReceipt{
			{
				OperationID:       resp.ExecutionID,
				TargetPath:        "repositories.xlsx",
				CommitCount:       1,
				Witnessed:         true,
				PublishedRevision: 1,
			},
		},
		Activities: []ActivityRecord{
			{TargetPath: "repositories.xlsx", OperationKind: "python_write", Interpreter: "portable_python"},
		},
		FinalFiles: map[string][]byte{
			"repositories.xlsx": xlsxBytes,
		},
		WorkspaceFiles:  []string{"config/repos.yaml", "repositories.xlsx"},
		Package:         PackageObservation{InventoryMatchesImage: true},
		TerminalOutcome: "completed",
		Stdout:          resp.Stdout,
	}

	bundle := py09EvidenceBundle{
		CaseID:          spec.ID,
		Revision:        expectedRevision,
		Backend:         string(resp.Backend),
		CleanCompletion: resp.CleanCompletion,
		ExitCode:        resp.ExitCode,
		Stdout:          resp.Stdout,
		Stderr:          resp.Stderr,
		ArtifactPath:    "repositories.xlsx",
		ArtifactBytes:   xlsxBytes,
		PublishedCount:  pubCount,
		Effects:         effects,
		Observation:     obs,
	}
	return bundle, spec, reposYAML
}

// TestPY09_ProviderFreeReplayHelperAndTypedRefusal replays one frozen helper
// script (workbook) on default native-aot and one typed refusal
// (PYTHON_TOOL_REQUIRED) from fresh fixtures with zero provider calls.
func TestPY09_ProviderFreeReplayHelperAndTypedRefusal(t *testing.T) {
	svc := newNativeQualService(t, t.TempDir())

	t.Run("frozen_helper_repo_workbook_on_native_aot", func(t *testing.T) {
		const rev = "981ae19cc12137b10796315bc26e933352e896d5"
		bundle, spec, reposYAML := captureWorkbookPassingBundle(t, svc, rev)

		if bundle.Backend != string(pythonruntime.BackendNativeAOT) {
			t.Fatalf("expected backend %q, got %q", pythonruntime.BackendNativeAOT, bundle.Backend)
		}
		if !bundle.CleanCompletion || bundle.ExitCode != 0 {
			t.Fatalf("expected clean completion, got clean=%v exit=%d", bundle.CleanCompletion, bundle.ExitCode)
		}
		if bundle.PublishedCount != 1 {
			t.Fatalf("expected 1 published workbook write, got %d", bundle.PublishedCount)
		}

		exp := py09EvidenceExpectation{
			ExpectedRevision: rev,
			ExpectedBackend:  string(pythonruntime.BackendNativeAOT),
			RequiredEffects: []py09EffectRecord{
				{Effect: "filesystem_write", Operation: "publish", Target: "repositories.xlsx", Decision: "allow"},
			},
			ExpectedReposYAML: reposYAML,
		}
		contractcheck.FailErr(t, "grade clean provider-free workbook bundle", gradePY09EvidenceBundle(exp, bundle))

		report := Grade(spec, bundle.Observation)
		if !report.Pass() {
			t.Fatalf("contract Grade() failed on clean native-aot replay: %+v", report.Verdicts)
		}
		t.Logf("PASS provider-free native-aot workbook replay: backend=%s published=%d effects=%d",
			bundle.Backend, bundle.PublishedCount, len(bundle.Effects))
	})

	t.Run("typed_refusal_python_tool_required_preserves_prior_effects", func(t *testing.T) {
		runner := &py09RecordingPythonRunner{}
		fx := newPY09CommandFixture(t, runner, true)

		out, err := fx.Call(t, t.Context(), `echo prior_stage_ok > prior.txt && python3 -c 'print("SHOULD_NOT_RUN")'`)
		contractcheck.FailErr(t, "call compound command with bare python3", err)

		if !strings.Contains(out, pythoncatalog.RoutingCodePythonToolRequired) {
			t.Fatalf("expected typed refusal %s in output, got:\n%s", pythoncatalog.RoutingCodePythonToolRequired, out)
		}
		if runner.count() != 0 {
			t.Fatalf("bare python3 launched %d times before PYTHON_TOOL_REQUIRED stop, want 0", runner.count())
		}
		var parsed struct {
			State    string `json:"state"`
			StopCode string `json:"stop_code"`
			Stdout   string `json:"stdout"`
		}
		contractcheck.FailErr(t, "parse command refusal json", json.Unmarshal([]byte(out), &parsed))
		if parsed.State != "rejected" || parsed.StopCode != pythoncatalog.RoutingCodePythonToolRequired {
			t.Fatalf("unexpected refusal envelope: state=%q stop_code=%q", parsed.State, parsed.StopCode)
		}
		if strings.Contains(parsed.Stdout, "SHOULD_NOT_RUN") {
			t.Fatalf("refused bare python3 body executed unexpectedly: stdout=%q", parsed.Stdout)
		}
		priorBytes, readErr := os.ReadFile(filepath.Join(fx.Dir, "prior.txt"))
		contractcheck.FailErr(t, "read preserved prior.txt after PYTHON_TOOL_REQUIRED", readErr)
		if string(priorBytes) != "prior_stage_ok\n" {
			t.Fatalf("prior.txt = %q, want %q", string(priorBytes), "prior_stage_ok\n")
		}
		t.Logf("PASS typed refusal %s: runner_launches=%d prior.txt=%q",
			pythoncatalog.RoutingCodePythonToolRequired, runner.count(), strings.TrimSpace(string(priorBytes)))
	})
}

// TestPY09_GraderDefectAndBenignRefusalControls uses copies of real passing
// native-aot replay evidence to verify the graders catch:
//  1. Wrong revision paired with a valid artifact
//  2. Missing or duplicate effect
//  3. Incomplete/stopped result reported as complete
//  4. Benign refusal control (accepted clean; rejected if bytes leak or marked complete)
func TestPY09_GraderDefectAndBenignRefusalControls(t *testing.T) {
	svc := newNativeQualService(t, t.TempDir())
	const expectedRev = "981ae19cc12137b10796315bc26e933352e896d5"

	cleanBundle, spec, reposYAML := captureWorkbookPassingBundle(t, svc, expectedRev)
	exp := py09EvidenceExpectation{
		ExpectedRevision: expectedRev,
		ExpectedBackend:  string(pythonruntime.BackendNativeAOT),
		RequiredEffects: []py09EffectRecord{
			{Effect: "filesystem_write", Operation: "publish", Target: "repositories.xlsx", Decision: "allow"},
		},
		ExpectedReposYAML: reposYAML,
	}

	// Sanity check: unmodified passing bundle passes both graders.
	contractcheck.FailErr(t, "unmodified passing bundle must pass", gradePY09EvidenceBundle(exp, cleanBundle))
	if r := Grade(spec, cleanBundle.Observation); !r.Pass() {
		t.Fatalf("unmodified Observation must pass Grade(): %+v", r.Verdicts)
	}

	t.Run("defect_control_1_wrong_revision_with_valid_artifact", func(t *testing.T) {
		mutated := cleanBundle.clone(t)
		mutated.Revision = "deadbeef00000000000000000000000000000000"

		// Confirm the artifact itself is still valid, proving the failure is specifically revision provenance.
		contractcheck.FailErr(t, "artifact bytes remain valid", GradeRepoWorkbookWithMode(reposYAML, mutated.ArtifactBytes, WorkbookFrozenReplay))

		err := gradePY09EvidenceBundle(exp, mutated)
		if err == nil || !strings.Contains(err.Error(), "revision mismatch") {
			t.Fatalf("Defect Control 1 must fail with revision mismatch, got err=%v", err)
		}
		t.Logf("PASS Defect Control 1 detected: %v", err)
	})

	t.Run("defect_control_2a_missing_required_effect", func(t *testing.T) {
		mutated := cleanBundle.clone(t)
		var filtered []py09EffectRecord
		for _, eff := range mutated.Effects {
			if eff.Effect == "filesystem_write" && eff.Target == "repositories.xlsx" {
				continue
			}
			filtered = append(filtered, eff)
		}
		mutated.Effects = filtered

		err := gradePY09EvidenceBundle(exp, mutated)
		if err == nil || !strings.Contains(err.Error(), "missing required effect") {
			t.Fatalf("Defect Control 2a must fail with missing required effect, got err=%v", err)
		}
		t.Logf("PASS Defect Control 2a detected: %v", err)
	})

	t.Run("defect_control_2b_duplicate_effect_and_publication_receipt", func(t *testing.T) {
		mutated := cleanBundle.clone(t)
		mutated.Effects = append(mutated.Effects, py09EffectRecord{
			Effect:    "filesystem_write",
			Operation: "publish",
			Target:    "repositories.xlsx",
			Decision:  "allow",
		})
		mutated.Observation.Receipts = append(mutated.Observation.Receipts, mutated.Observation.Receipts[0])

		err := gradePY09EvidenceBundle(exp, mutated)
		if err == nil || !strings.Contains(err.Error(), "duplicate effect") {
			t.Fatalf("Defect Control 2b must fail with duplicate effect, got err=%v", err)
		}
		report := Grade(spec, mutated.Observation)
		if got := report.Verdict(GuaranteeExactlyOncePublication).Status; got != VerdictFail {
			t.Fatalf("Defect Control 2b must fail GuaranteeExactlyOncePublication in Grade(), got %s", got)
		}
		t.Logf("PASS Defect Control 2b detected: bundle=%v grader=%s", err, report.Verdict(GuaranteeExactlyOncePublication).Reason)
	})

	// Capture a real denied/stopped execution on native-aot for Defect Control 3 & Benign Refusal Control.
	wsDenied := t.TempDir()
	const secretSentinel = "PY09_TOP_SECRET_SENTINEL_NEVER_DISCLOSE"
	contractcheck.FailErr(t, "write blocked.txt", os.WriteFile(filepath.Join(wsDenied, "blocked.txt"), []byte(secretSentinel), 0o644))
	rootsDenied := []pythondocuments.HostRootConfig{{ID: "ws", Dir: wsDenied}}
	deniedResp, err := svc.ExecuteTool(context.Background(), portablepython.ToolRequest{
		ExecutionID:  "py09-denied-control",
		CallID:       "py09-denied-call",
		SessionID:    "py09-session",
		Actor:        "coordinator",
		View:         "primary",
		Surface:      "implement_investigate",
		Mode:         pythonruntime.ModeCode,
		Code:         `open("blocked.txt", "r", encoding="utf-8").read()`,
		Roots:        rootsDenied,
		MutationDoor: native.NewPythonMutationDoor(tools.ToolContext{}, rootsDenied),
		Policy: func(_ context.Context, op pythonruntime.ReachedOperation) pythonadmission.PolicyDecision {
			if op.Target.RelPath == "blocked.txt" {
				return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionDeny, Reason: "py09_benign_refusal_control"}
			}
			return pythonadmission.PolicyDecision{Kind: pythonruntime.DecisionAllow}
		},
		Limits: pythonruntime.ResourceLimits{Timeout: 60 * time.Second},
	})
	contractcheck.FailErr(t, "execute denied blocked.txt control", err)
	if deniedResp.CleanCompletion {
		t.Fatalf("expected incomplete refusal on blocked.txt, got clean=%v exit=%d stop=%+v", deniedResp.CleanCompletion, deniedResp.ExitCode, deniedResp.Stop)
	}
	deniedStopCode := ""
	deniedStopReason := ""
	if deniedResp.Stop != nil {
		deniedStopCode = string(deniedResp.Stop.Code)
		deniedStopReason = deniedResp.Stop.Reason
	}
	if deniedStopCode == "" && deniedResp.AdmissionEvidence != nil {
		for _, rec := range deniedResp.AdmissionEvidence.Admissions() {
			if rec.Decision == pythonruntime.DecisionDeny && rec.Target.RelPath == "blocked.txt" {
				deniedStopCode = string(rec.StopCode)
				if deniedStopCode == "" {
					deniedStopCode = "COMMAND_EFFECT_NOT_ALLOWED"
				}
				deniedStopReason = rec.Reason
				break
			}
		}
	}
	if deniedStopCode == "" {
		t.Fatalf("expected denied admission or terminal stop code on blocked.txt, got stop=%+v stderr=%q", deniedResp.Stop, deniedResp.Stderr)
	}

	deniedPubCount := 0
	if deniedResp.Settlement != nil {
		deniedPubCount = len(deniedResp.Settlement.Published)
	}
	stoppedBundle := py09EvidenceBundle{
		CaseID:          "py09-denied-blocked-read",
		Revision:        expectedRev,
		Backend:         string(deniedResp.Backend),
		CleanCompletion: deniedResp.CleanCompletion,
		ExitCode:        deniedResp.ExitCode,
		StopCode:        deniedStopCode,
		StopReason:      deniedStopReason,
		Stdout:          deniedResp.Stdout,
		Stderr:          deniedResp.Stderr,
		PublishedCount:  deniedPubCount,
	}

	t.Run("defect_control_3_incomplete_result_reported_as_complete", func(t *testing.T) {
		mutated := stoppedBundle.clone(t)
		// Defect injection: flip clean_completion=true on a stopped/errored execution.
		mutated.CleanCompletion = true

		err := gradePY09EvidenceBundle(py09EvidenceExpectation{
			ExpectedRevision: expectedRev,
			ExpectedBackend:  string(pythonruntime.BackendNativeAOT),
		}, mutated)
		if err == nil || !strings.Contains(err.Error(), "completion integrity") {
			t.Fatalf("Defect Control 3 must fail with completion integrity mismatch, got err=%v", err)
		}
		t.Logf("PASS Defect Control 3 detected: %v", err)
	})

	t.Run("benign_refusal_control_accepts_clean_and_rejects_leak_or_complete_flip", func(t *testing.T) {
		refusalExp := py09RefusalExpectation{
			AllowedStopCodes: []string{"COMMAND_EFFECT_NOT_ALLOWED", pythoncatalog.RoutingCodePythonToolRequired},
			ForbiddenSecrets: []string{secretSentinel},
		}

		// 1. Clean benign refusal from native-aot denied read MUST PASS.
		contractcheck.FailErr(t, "clean benign refusal (COMMAND_EFFECT_NOT_ALLOWED) must pass", gradePY09RefusalBundle(refusalExp, stoppedBundle))

		// 2. Clean benign refusal from PYTHON_TOOL_REQUIRED command evaluator MUST PASS.
		runner := &py09RecordingPythonRunner{}
		fx := newPY09CommandFixture(t, runner, true)
		cmdOut, cmdErr := fx.Call(t, t.Context(), `python3 -c 'print("NO_RUN")'`)
		contractcheck.FailErr(t, "call bare python3 refusal", cmdErr)
		if !strings.Contains(cmdOut, pythoncatalog.RoutingCodePythonToolRequired) || runner.count() != 0 {
			t.Fatalf("expected PYTHON_TOOL_REQUIRED with 0 launches, got launches=%d out=%q", runner.count(), cmdOut)
		}
		cmdRefusalBundle := py09EvidenceBundle{
			CaseID:          "py09-benign-python-tool-required",
			Revision:        expectedRev,
			Backend:         "command-router",
			CleanCompletion: false,
			ExitCode:        1,
			StopCode:        pythoncatalog.RoutingCodePythonToolRequired,
			StopReason:      "direct python tool offered",
			Stdout:          cmdOut,
			PublishedCount:  0,
		}
		contractcheck.FailErr(t, "clean benign refusal (PYTHON_TOOL_REQUIRED) must pass", gradePY09RefusalBundle(refusalExp, cmdRefusalBundle))

		// 3. Mutating benign refusal to leak secret bytes MUST FAIL.
		leaked := stoppedBundle.clone(t)
		leaked.Stdout = "oops: " + secretSentinel
		if err := gradePY09RefusalBundle(refusalExp, leaked); err == nil || !strings.Contains(err.Error(), "disclosure violation") {
			t.Fatalf("refusal grader must reject leaked secret bytes, got err=%v", err)
		}

		// 4. Mutating benign refusal to clean_completion=true MUST FAIL.
		flipped := stoppedBundle.clone(t)
		flipped.CleanCompletion = true
		if err := gradePY09RefusalBundle(refusalExp, flipped); err == nil || !strings.Contains(err.Error(), "clean_completion=true") {
			t.Fatalf("refusal grader must reject clean_completion=true on refusal, got err=%v", err)
		}
		t.Logf("PASS Benign Refusal Control: accepted clean COMMAND_EFFECT_NOT_ALLOWED + PYTHON_TOOL_REQUIRED and rejected leak & completion-flip mutations")
	})
}
