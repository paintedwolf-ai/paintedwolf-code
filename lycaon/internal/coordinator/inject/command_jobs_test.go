package inject_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/inject"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/heldcall"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/prompts/promptstest"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderCommandJobsBlockUsesSurfaceControls(t *testing.T) {
	now := time.Now().UTC()
	renderer := promptstest.InjectRenderer(t)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)

	fullCoord, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      plans["implement_investigate"].Promote("command_output", "command_stop", "held_result", "held_stop", "wait"),
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "fullCoord reader", err)
	workerReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "implement",
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "worker reader", err)

	jobs := []bgprocess.JobSnapshot{{
		Handle: "command-1", Mode: bgprocess.JobModeAwaited, OriginTool: "verify",
		StartedAt: now.Add(-time.Minute), Timeout: 10 * time.Minute,
		Stages: []hostcmd.StageResult{{Command: "./task check-fast"}},
	}}

	coordinator := inject.RenderCommandJobsBlock(
		context.Background(), renderer, fullCoord, []string{"process_done"}, "sess-inject-test", jobs, nil, now, "coordinator",
	)
	worker := inject.RenderCommandJobsBlock(
		context.Background(), renderer, workerReader, []string{"process_done"}, "sess-inject-test", jobs, nil, now, "worker",
	)
	for name, block := range map[string]string{"coordinator": coordinator, "worker": worker} {
		if !strings.Contains(block, "handle=command-1") || !strings.Contains(block, "command_output(handle=") || !strings.Contains(block, "command_stop(handle=") {
			t.Fatalf("%s ledger missing job controls: %q", name, block)
		}
		if !strings.Contains(block, `wait(until_complete=true,conditions=[{"kind":"process_done","handles":["<handle>"]}])`) {
			t.Fatalf("%s ledger missing exact wait control: %q", name, block)
		}
	}

	// Paired witness 2: deferred/loadable surface (floor-only implement_investigate) teaches request_tools(need=...).
	floorCoord, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      plans["implement_investigate"],
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "floorCoord reader", err)
	deferredBlock := inject.RenderCommandJobsBlock(
		context.Background(), renderer, floorCoord, []string{"process_done"}, "sess-inject-test", jobs, nil, now, "coordinator",
	)
	if strings.Contains(deferredBlock, "command_output(handle=") || strings.Contains(deferredBlock, "command_stop(handle=") {
		t.Fatalf("floor-only witness leaked unoffered direct command_output/command_stop calls:\n%s", deferredBlock)
	}
	if !strings.Contains(deferredBlock, `request_tools(need="command_output")`) || !strings.Contains(deferredBlock, `request_tools(need="command_stop")`) {
		t.Fatalf("floor-only witness missing request_tools loading steps:\n%s", deferredBlock)
	}

	// Paired witness 3: closed surface without command_output, request_tools, or process_done wait.
	parkCoord, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_park",
		Profiles:  profiles,
		Plan:      plans["implement_park"],
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "parkCoord reader", err)
	parkNoProcessWait := inject.RenderCommandJobsBlock(
		context.Background(), renderer, parkCoord, []string{"worker_done"}, "sess-inject-test", jobs, nil, now, "coordinator",
	)
	if strings.Contains(parkNoProcessWait, "command_output") || strings.Contains(parkNoProcessWait, "command_stop") || strings.Contains(parkNoProcessWait, "request_tools") || strings.Contains(parkNoProcessWait, "process_done") {
		t.Fatalf("unoffered/no-process_done witness leaked unavailable controls:\n%s", parkNoProcessWait)
	}
	if !strings.Contains(parkNoProcessWait, "Live job output stays recorded on `<handle>`") {
		t.Fatalf("unoffered witness missing recorded-on-handle notice:\n%s", parkNoProcessWait)
	}
}

func TestRenderCommandJobsBlockListsHeldCallsWithTheirControls(t *testing.T) {
	now := time.Now().UTC()
	renderer := promptstest.InjectRenderer(t)
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	plans, err := surface.CompileToolPlans(1)
	testutil.FailErr(t, "CompileToolPlans", err)
	withHeld, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: "implement_investigate",
		Profiles:  profiles,
		Plan:      plans["implement_investigate"].Promote("held_result", "held_stop", "wait"),
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "withHeld reader", err)
	withoutHeld, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: "plan_write_only",
		Profiles:  profiles,
		Visible:   []string{"command"},
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "withoutHeld reader", err)

	held := []heldcall.Running{{Handle: "held-1", Tool: "find", Elapsed: 14 * time.Minute}}

	block := inject.RenderCommandJobsBlock(context.Background(), renderer, withHeld, []string{"process_done"}, "sess-inject-test", nil, held, now, "coordinator")
	for _, want := range []string{"handle=held-1", "mode=held", "tool=find", "held_result", "held_stop", `"kind":"process_done"`} {
		if !strings.Contains(block, want) {
			t.Fatalf("held ledger missing %q: %q", want, block)
		}
	}
	if strings.Contains(block, "command_output") || strings.Contains(block, "command=") {
		t.Fatalf("a held-only ledger names command controls: %q", block)
	}

	neg := inject.RenderCommandJobsBlock(context.Background(), renderer, withoutHeld, nil, "sess-inject-test", nil, held, now, "worker")
	if strings.Contains(neg, "held_result") || strings.Contains(neg, "held_stop") || strings.Contains(neg, "process_done") {
		t.Fatalf("held ledger without held_result/held_stop leaked unoffered controls: %q", neg)
	}
	if !strings.Contains(neg, "Output remains on `<handle>`") {
		t.Fatalf("held ledger negative witness missing output-remains notice: %q", neg)
	}
}

func TestCommandJobsBoundCommandPreviewWithoutDroppingHandle(t *testing.T) {
	profiles, err := sandbox.LoadToolProfiles()
	testutil.FailErr(t, "LoadToolProfiles", err)
	coordReader, err := prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		Profiles:  profiles,
		RootCount: 1,
		WebSearch: true,
	})
	testutil.FailErr(t, "coord reader", err)
	block := inject.RenderCommandJobsBlock(context.Background(), promptstest.InjectRenderer(t), coordReader, []string{"process_done"}, "sess-inject-test", []bgprocess.JobSnapshot{{
		Handle: "live-check", Mode: bgprocess.JobModeAwaited, OriginTool: "command", StartedAt: time.Now(),
		Stages: []hostcmd.StageResult{{Command: strings.Repeat("long-command-", 100)}},
	}}, nil, time.Now(), "coordinator")
	if !strings.Contains(block, "handle=live-check") || !strings.Contains(block, "command_output") || strings.Contains(block, strings.Repeat("long-command-", 20)) {
		t.Fatalf("unbounded preview or missing recovery handle: %q", block)
	}
}
