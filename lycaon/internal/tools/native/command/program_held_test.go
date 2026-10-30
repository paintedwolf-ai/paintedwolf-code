package command_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/approvaloutcome"
	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

type heldProbe struct {
	invoked, rechecked int
	settled            []string
	stale              error
}

func (p *heldProbe) effect(id string) command.HeldEffect {
	return command.HeldEffect{
		Identity: toolcontract.ExecutionIdentity{ProgramID: "program", NodeID: "node", ExecutionID: id},
		Recheck:  func(context.Context) error { p.rechecked++; return p.stale },
		Invoke: func(context.Context) (toolcontract.ProducerResult, error) {
			p.invoked++
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectCompleted}, nil
		},
		Settle: func(_ context.Context, result toolcontract.ProducerResult, code string) error {
			p.settled = append(p.settled, string(result.Terminal)+":"+code)
			return nil
		},
	}
}

func TestHeldEffectResumesExactlyOnceAfterFreshRecheck(t *testing.T) {
	probe := &heldProbe{}
	held := &command.ProgramHeldEffects{}
	effect := probe.effect("held-1")
	if err := held.Park(effect); err != nil {
		testutil.FailErr(t, "park held effect", err)
	}
	if err := held.Park(effect); err == nil {
		t.Fatal("same execution parked twice")
	}
	result, err := held.Resume(t.Context(), effect.Identity)
	if err != nil || result.EffectCertainty != toolcontract.EffectCompleted || probe.invoked != 1 || probe.rechecked != 1 {
		t.Fatalf("resume %+v %v probe %+v", result, err, probe)
	}
	if _, err := held.Resume(t.Context(), effect.Identity); toolrejection.AsToolReject(err) == nil || probe.invoked != 1 {
		t.Fatalf("held effect replayed: %v invoked %d", err, probe.invoked)
	}
	other := effect.Identity
	other.NodeID = "different-node"
	if _, err := held.Resume(t.Context(), other); toolrejection.AsToolReject(err) == nil {
		t.Fatal("mismatched identity resumed a held effect")
	}
	if len(probe.settled) != 1 || probe.settled[0] != "completed:" {
		t.Fatalf("settlements %v", probe.settled)
	}
}

func TestHeldEffectStaleInputConflictsWithoutRecomputation(t *testing.T) {
	probe := &heldProbe{stale: errors.New("destination revision changed")}
	held := &command.ProgramHeldEffects{}
	effect := probe.effect("held-stale")
	if err := held.Park(effect); err != nil {
		testutil.FailErr(t, "park held effect", err)
	}
	result, err := held.Resume(t.Context(), effect.Identity)
	if err == nil || result.Terminal != toolcontract.ProducerRefused || result.StopCode != "EDIT_TARGET_CHANGED_BY_OTHERS" || probe.invoked != 0 {
		t.Fatalf("stale resume %+v %v invoked %d", result, err, probe.invoked)
	}
	if len(probe.settled) != 1 || probe.settled[0] != "refused:EDIT_TARGET_CHANGED_BY_OTHERS" {
		t.Fatalf("stale settlement %v", probe.settled)
	}
}

func TestHeldEffectDenialSettlesWithoutInvoking(t *testing.T) {
	probe := &heldProbe{}
	held := &command.ProgramHeldEffects{}
	effect := probe.effect("held-denied")
	if err := held.Park(effect); err != nil {
		testutil.FailErr(t, "park held effect", err)
	}
	if err := held.Stop(t.Context(), effect.Identity); err != nil {
		testutil.FailErr(t, "stop held effect", err)
	}
	if _, err := held.Resume(t.Context(), effect.Identity); err == nil || probe.invoked != 0 || probe.rechecked != 0 {
		t.Fatal("denied effect resumed")
	}
	if err := held.Stop(t.Context(), effect.Identity); err == nil {
		t.Fatal("repeated stop settled twice")
	}
	if len(probe.settled) != 1 || probe.settled[0] != "refused:"+approvaloutcome.CodeApprovalDenied {
		t.Fatalf("denial settlement %v", probe.settled)
	}
	if err := held.Park(command.HeldEffect{}); err == nil {
		t.Fatal("incomplete held effect parked")
	}
}

func TestSupervisorStopAcknowledgesBeforeExitAndClosesAdmission(t *testing.T) {
	supervisor := &command.ProgramSupervisor{}
	root := toolcontract.OperationInvocation{SessionID: "s1", Identity: toolcontract.ExecutionIdentity{ProgramID: "p1", ExecutionID: "root"}}
	entered, release := make(chan struct{}), make(chan struct{})
	var admitted error
	done := make(chan toolcontract.ProducerResult, 1)
	go func() {
		result, _ := supervisor.RunProgram(t.Context(), root, func(ctx context.Context) (toolcontract.ProducerResult, error) {
			if err := supervisor.AdmitChild(ctx); err != nil {
				t.Errorf("first child refused: %v", err)
			}
			close(entered)
			<-release
			admitted = supervisor.AdmitChild(ctx)
			return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.EffectCompleted}, nil
		})
		done <- result
	}()
	<-entered
	if ids := supervisor.Stop("s1"); len(ids) != 1 || ids[0] != "p1" {
		t.Fatalf("stop acknowledged %v", ids)
	}
	if ids := supervisor.Stop("s1"); len(ids) != 0 {
		t.Fatalf("repeated stop re-acknowledged %v", ids)
	}
	if ids := supervisor.Stop("other-session"); len(ids) != 0 {
		t.Fatal("stop reached another session's program")
	}
	close(release)
	result := <-done
	if admitted == nil || result.Terminal != toolcontract.ProducerInterrupted || result.EffectCertainty != toolcontract.EffectCompleted {
		t.Fatalf("stopped program admitted=%v result=%+v", admitted, result)
	}
	if err := supervisor.AdmitChild(context.Background()); err == nil {
		t.Fatal("unsupervised child admitted")
	}
	if _, err := supervisor.RunProgram(t.Context(), toolcontract.OperationInvocation{}, nil); err == nil {
		t.Fatal("program without identity supervised")
	}
	if _, err := supervisor.Lost(t.Context(), "s1", "output:lost"); err == nil {
		t.Fatal("loss reconciled without a receipt owner")
	}
}

type dirSources string

func (d dirSources) Capture(_ context.Context, _ toolcontract.OperationInvocation, req commandprogram.SourceCaptureRequest) (commandprogram.CapturedSource, error) {
	raw, err := os.ReadFile(filepath.Join(string(d), req.Path))
	if err != nil {
		return commandprogram.CapturedSource{}, err
	}
	hash := sha256.Sum256(raw)
	return commandprogram.CapturedSource{
		Bytes: raw,
		Pin: toolcontract.SourcePin{
			SourceID:       req.Path,
			Revision:       "rev-1",
			Representation: toolcontract.UTF8SourceBytes,
			SHA256:         hex.EncodeToString(hash[:]),
			EndByte:        int64(len(raw)),
		},
	}, nil
}

func TestProgramEntrySourcesScriptAndDeduplicatesProgramDefinitions(t *testing.T) {
	f := newProgramFixture(t)
	baseOwners := f.Runtime.Owners
	f.Runtime.Owners = func(tc tools.ToolContext, paths *projectpaths.ProgramPaths) (command.ProgramOwners, error) {
		owners, err := baseOwners(tc, paths)
		owners.Sources = dirSources(f.Dir)
		return owners, err
	}
	testutil.FailErr(t, "write sourced script", os.WriteFile(filepath.Join(f.Dir, "lib.sh"), []byte("greet() { external -- /bin/echo \"$1\"; }\n"), 0o600))
	out, err := f.call(t, t.Context(), "source -- lib.sh; greet first; greet second")
	testutil.FailErr(t, "run sourced function calls", err)
	if !strings.Contains(out, `"stdout":"first\nsecond\n"`) || !strings.Contains(out, `"state":"completed"`) {
		t.Fatalf("sourced program outcome %s", out)
	}
	var programID string
	testutil.FailErr(t, "read program id", f.Database.QueryRowContext(t.Context(), `SELECT id FROM invocation_programs`).Scan(&programID))
	var sourceCount int
	testutil.FailErr(t, "count program sources", f.Database.QueryRowContext(t.Context(), `SELECT count(*) FROM invocation_program_sources WHERE program_id = ?`, programID).Scan(&sourceCount))
	if sourceCount != 2 {
		t.Fatalf("sourceCount = %d, want 2 (root and sourced unit)", sourceCount)
	}
	page, err := f.Recorder.ReadProgramPage(t.Context(), f.Session, programID, "", 10)
	testutil.FailErr(t, "read program page", err)
	var externalExecs []api.CommandExecutionReceipt
	for _, exec := range page.Executions {
		if exec.NativeCommand == "external" || strings.HasPrefix(exec.Command, "external") {
			externalExecs = append(externalExecs, exec)
		}
	}
	if len(externalExecs) != 2 || externalExecs[0].DefinitionDigest == "" || externalExecs[0].DefinitionDigest != externalExecs[1].DefinitionDigest {
		t.Fatalf("external executions = %+v (all = %+v)", externalExecs, page.Executions)
	}
	var defCount int
	testutil.FailErr(t, "count deduplicated definitions", f.Database.QueryRowContext(t.Context(),
		`SELECT count(*) FROM invocation_program_definitions WHERE program_id = ? AND definition_digest = ?`,
		programID, externalExecs[0].DefinitionDigest,
	).Scan(&defCount))
	if defCount != 1 {
		t.Fatalf("invocation_program_definitions rows for external digest = %d, want 1", defCount)
	}
	for _, exec := range externalExecs {
		binding, err := f.Recorder.ReadProgramExecutionBinding(t.Context(), f.Session, exec.ID)
		testutil.FailErr(t, "read child binding", err)
		if binding.DefinitionDigest != exec.DefinitionDigest || binding.DefinitionJSON == "" || binding.Invocation.SourceUnitID != "lib.sh" {
			t.Fatalf("binding %s = %+v", exec.ID, binding)
		}
	}
}

func TestHeldEffectPreservesInvocationAndSettlementErrors(t *testing.T) {
	invokeErr, settleErr := errors.New("owner failed"), errors.New("receipt failed")
	probe := &heldProbe{}
	effect := probe.effect("held-errors")
	effect.Invoke = func(context.Context) (toolcontract.ProducerResult, error) {
		probe.invoked++
		return toolcontract.ProducerResult{Status: 1, Terminal: toolcontract.ProducerFailed}, invokeErr
	}
	effect.Settle = func(context.Context, toolcontract.ProducerResult, string) error { return settleErr }
	held := &command.ProgramHeldEffects{}
	testutil.FailErr(t, "park failed owner", held.Park(effect))
	_, err := held.Resume(t.Context(), effect.Identity)
	if !errors.Is(err, invokeErr) || !errors.Is(err, settleErr) {
		t.Fatalf("lost error identity: %v", err)
	}
	if _, err := held.Resume(t.Context(), effect.Identity); toolrejection.AsToolReject(err) == nil || probe.invoked != 1 {
		t.Fatalf("failed owner replayed: %v, calls=%d", err, probe.invoked)
	}
}
