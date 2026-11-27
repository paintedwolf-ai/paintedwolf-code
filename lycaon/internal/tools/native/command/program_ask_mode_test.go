package command_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/agentpolicy"
	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolexecution"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
	"github.com/lycaon/lycaon/internal/toolsurface"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAskProgramEntryRejectsPrunedEnvelopeFields(t *testing.T) {
	f := newAskComposedFixture(t)
	cmdTool := &command.CommandTool{Runner: &hostcmd.Runner{}, Programs: f.Runtime, Boundary: f.Boundary, Background: f.Background}
	for _, c := range []struct {
		field string
		value any
	}{
		{field: "capability_request", value: map[string]any{"write_root": "/tmp/out"}},
		{field: "secret_use", value: map[string]any{"services": []any{"https://api.example.com"}}},
		{field: "snapshot_capture", value: map[string]any{"caption": "snap"}},
		{field: "socks_proxy", value: false},
		{field: "terminal_capture", value: map[string]any{"winsize": map[string]any{"cols": 80, "rows": 24}}},
		{field: "verification", value: false},
	} {
		t.Run(c.field, func(t *testing.T) {
			args := map[string]any{"command": "pwd", c.field: c.value}
			_, err := f.Invoke(t, t.Context(), "command", args, cmdTool.Run)
			reject := toolrejection.AsToolReject(err)
			if reject == nil || reject.Code != tools.AskModeActiveCode {
				t.Fatalf("reject = %+v, want %s", reject, tools.AskModeActiveCode)
			}
			if reject.Data["tool"] != "command" || reject.Data["field"] != c.field || len(reject.Data) != 2 {
				t.Fatalf("reject.Data = %#v, want tool=command and field=%s only", reject.Data, c.field)
			}
		})
	}
}

func TestAskProgramOutputSpillCanBeSlicedInFollowUpAskCommand(t *testing.T) {
	f := newAskComposedFixture(t)
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Host.ModelContextWindow = 16000 // 12 KiB budget
	}
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "line-%04d: the quick brown fox jumps over the lazy dog\n", i)
	}
	testutil.FailErr(t, "write big.txt", os.WriteFile(filepath.Join(f.Dir, "big.txt"), []byte(b.String()), 0o644))

	raw, err := f.Call(t, t.Context(), "cat big.txt")
	testutil.FailErr(t, "cat big.txt in Ask", err)
	out := decodeOutcome(t, raw)
	if !out.Truncated || len(out.Outputs) != 1 || !out.Outputs[0].Truncated {
		t.Fatalf("expected truncated spill window, got %+v", out)
	}
	win := out.Outputs[0]
	if win.OutputPath != "@output/"+out.ProgramID+".stdout" {
		t.Fatalf("output path = %q", win.OutputPath)
	}
	re := regexp.MustCompile(`\(sed -n '(\d+),(\d+)p' (@output/[^)]+)\)`)
	m := re.FindStringSubmatch(out.Stdout)
	if m == nil {
		t.Fatalf("missing sed spill notice in stdout:\n%s", out.Stdout)
	}
	sliceCmd := fmt.Sprintf("sed -n '%s,%sp' %s", m[1], m[2], m[3])
	slicedRaw, err := f.Call(t, t.Context(), sliceCmd)
	testutil.FailErr(t, "slice @output in Ask", err)
	sliced := decodeOutcome(t, slicedRaw)
	wantFirstSliced := fmt.Sprintf("line-%04d: the quick brown fox jumps over the lazy dog\n", win.HeadEndLine+1)
	if sliced.Status != 0 || sliced.Refusal != nil || !strings.HasPrefix(sliced.Stdout, wantFirstSliced) {
		t.Fatalf("sliced outcome = %+v, want prefix %q", sliced, wantFirstSliced)
	}
}

func TestAskProgramAdmitsReadPipelinesAndRefusesWritesCurlAndExternal(t *testing.T) {
	f := newAskComposedFixture(t)
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(f.Dir, "src"), 0o755))
	testutil.FailErr(t, "write notes.txt", os.WriteFile(filepath.Join(f.Dir, "src", "notes.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644))
	testutil.FailErr(t, "write data.json", os.WriteFile(filepath.Join(f.Dir, "src", "data.json"), []byte(`{"items":[{"name":"wolf","count":3}]}`+"\n"), 0o644))

	bin, err := gitengine.BinaryPath()
	testutil.FailErr(t, "bundled git", err)
	runGitCLI(t, bin, f.Dir, "init", "-q", "-b", "main")
	runGitCLI(t, bin, f.Dir, "add", ".")
	runGitCLI(t, bin, f.Dir, "commit", "-q", "-m", "seed repo")

	for _, c := range []struct {
		cmd  string
		want string
	}{
		{cmd: "cat -n src/notes.txt | sed -n '2,2p'", want: "2\tbeta\n"},
		{cmd: "rg -n beta src/notes.txt", want: "2:beta\n"},
		{cmd: "find src -name '*.json'", want: "src/data.json\n"},
		{cmd: "jq -r '.items[0].name' src/data.json", want: "wolf\n"},
	} {
		raw, callErr := f.Call(t, t.Context(), c.cmd)
		testutil.FailErr(t, "run Ask read "+c.cmd, callErr)
		out := decodeOutcome(t, raw)
		if out.Status != 0 || out.Refusal != nil || !strings.Contains(out.Stdout, c.want) {
			t.Fatalf("%s: outcome = %+v, want substring %q", c.cmd, out, c.want)
		}
	}

	for _, cmd := range []string{
		"printf 'mutated\n' | tee src/out.txt",
		"mkdir src/newdir",
		"rm src/notes.txt",
		"sed -i 's/beta/delta/' src/notes.txt",
		"true > src/redirected.txt",
		"cat src/notes.txt > src/redirected.txt",
		"cat src/notes.txt >> src/redirected.txt",
		"git checkout -b topic",
		"git commit -m 'mutate'",
		"git reset --hard HEAD",
		"git ls-remote origin",
		"git fetch origin",
		"curl -s https://example.com",
		"curl -s -H 'Authorization: Bearer {{paintedwolf-secret:api_token}}' https://example.com",
	} {
		raw, callErr := f.Call(t, t.Context(), cmd)
		testutil.FailErr(t, "run refused mutation "+cmd, callErr)
		out := decodeOutcome(t, raw)
		if out.Refusal == nil || out.Refusal.Code != "COMMAND_EFFECT_NOT_ALLOWED" {
			t.Fatalf("%s: refusal = %+v, want COMMAND_EFFECT_NOT_ALLOWED (raw=%s)", cmd, out.Refusal, raw)
		}
	}

	for _, cmd := range []string{
		"/bin/echo outside",
		"external -- /bin/echo outside",
		"cat --definitely-unsupported-flag src/notes.txt", // unsupported option triggers native-gap fallback to invokeExternal
		"git commit --allow-empty -m 'gap'",               // unsupported git option triggers native-gap fallback to invokeExternal
	} {
		raw, callErr := f.Call(t, t.Context(), cmd)
		testutil.FailErr(t, "run refused external "+cmd, callErr)
		out := decodeOutcome(t, raw)
		if out.Refusal == nil || out.Refusal.Code != tools.AskModeActiveCode {
			t.Fatalf("%s: refusal = %+v, want %s (raw=%s)", cmd, out.Refusal, tools.AskModeActiveCode, raw)
		}
	}
}

func TestAskAndReadOnlyProgramFeedbackAlternativesAndAttribution(t *testing.T) {
	f := newAskComposedFixture(t)
	testutil.FailErr(t, "mkdir src", os.MkdirAll(filepath.Join(f.Dir, "src"), 0o755))
	testutil.FailErr(t, "write dead.tmp", os.WriteFile(filepath.Join(f.Dir, "src", "dead.tmp"), []byte("scratch\n"), 0o644))
	testutil.FailErr(t, "write notes.txt", os.WriteFile(filepath.Join(f.Dir, "src", "notes.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644))

	// 1. Refused mutation paired with an admitted read-only inspection on the same target.
	rawFindDelete, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "find -delete in Ask", err)
	outFindDelete := decodeOutcome(t, rawFindDelete)
	if outFindDelete.Refusal == nil || outFindDelete.Refusal.Code != "COMMAND_EFFECT_NOT_ALLOWED" {
		t.Fatalf("find -delete refusal = %+v", outFindDelete.Refusal)
	}
	for _, want := range []string{
		"Rejected: `find` (`-delete`) at 1:1 is unavailable in Ask mode.",
		"Cause: Ask mode permits only read-only inspection and blocks `mutation`.",
		"Fix: Run `find src -name '*.tmp'` to list the paths selected for deletion and report the affected files without deleting them.",
		"Next: Use the read-only observation or gathered evidence to answer, and explain that the user can switch to Agent mode if they want you to apply the change.",
	} {
		if !strings.Contains(outFindDelete.Refusal.Feedback, want) {
			t.Fatalf("find -delete feedback missing %q:\n%s", want, outFindDelete.Refusal.Feedback)
		}
	}
	suggestedCmd, _ := outFindDelete.Refusal.Details["code_instead"].(string)
	if suggestedCmd != "find src -name '*.tmp'" {
		t.Fatalf("code_instead = %q, want %q", suggestedCmd, "find src -name '*.tmp'")
	}
	rawFindRead, err := f.Call(t, t.Context(), suggestedCmd)
	testutil.FailErr(t, "run recommended find read in Ask", err)
	outFindRead := decodeOutcome(t, rawFindRead)
	if outFindRead.Status != 0 || outFindRead.Refusal != nil || !strings.Contains(outFindRead.Stdout, "src/dead.tmp\n") {
		t.Fatalf("recommended read outcome = %+v", outFindRead)
	}

	// Also verify sed -i -> sed stream preview on the same target.
	rawSedInPlace, err := f.Call(t, t.Context(), "sed -i 's/beta/delta/' src/notes.txt")
	testutil.FailErr(t, "sed -i in Ask", err)
	outSedInPlace := decodeOutcome(t, rawSedInPlace)
	if outSedInPlace.Refusal == nil || !strings.Contains(outSedInPlace.Refusal.Feedback, "Run `sed s/beta/delta/ src/notes.txt` to preview the transformed output on stdout without modifying the file in place.") {
		t.Fatalf("sed -i feedback = %+v", outSedInPlace.Refusal)
	}
	sedPreviewCmd, _ := outSedInPlace.Refusal.Details["code_instead"].(string)
	rawSedPreview, err := f.Call(t, t.Context(), sedPreviewCmd)
	testutil.FailErr(t, "run recommended sed preview in Ask", err)
	outSedPreview := decodeOutcome(t, rawSedPreview)
	if outSedPreview.Status != 0 || outSedPreview.Refusal != nil || !strings.Contains(outSedPreview.Stdout, "alpha\ndelta\ngamma\n") {
		t.Fatalf("recommended sed preview outcome = %+v", outSedPreview)
	}

	// 2. Removing the relevant form or target access makes the recommendation disappear while the precise cause remains.
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Turn.TurnOfferedToolNames = []string{"command"}
		tc.Turn.TurnPrograms = func(name string) bool { return name != "find" }
	}
	rawNoFindForm, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "find -delete with find program unreachable", err)
	outNoFindForm := decodeOutcome(t, rawNoFindForm)
	if outNoFindForm.Refusal == nil || strings.Contains(outNoFindForm.Refusal.Feedback, "Run `find src -name '*.tmp'`") || strings.Contains(outNoFindForm.Refusal.Feedback, "Available read-only forms") || outNoFindForm.Refusal.Details["code_instead"] != nil {
		t.Fatalf("unreachable form still recommended in feedback:\n%s", outNoFindForm.Refusal.Feedback)
	}
	if !strings.Contains(outNoFindForm.Refusal.Feedback, "Cause: Ask mode permits only read-only inspection and blocks `mutation`.") || !strings.Contains(outNoFindForm.Refusal.Feedback, "Fix: Do not retry this operation on the active read-only surface.") {
		t.Fatalf("missing precise cause or grounded fallback when form removed:\n%s", outNoFindForm.Refusal.Feedback)
	}

	// 2a. Offered command lacking program evidence (TurnPrograms == nil) also suppresses candidate and unverified form availability.
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Turn.TurnOfferedToolNames = []string{"command"}
		tc.Turn.TurnPrograms = nil
	}
	rawNoProgEvidence, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "find -delete without program evidence", err)
	outNoProgEvidence := decodeOutcome(t, rawNoProgEvidence)
	if outNoProgEvidence.Refusal == nil || strings.Contains(outNoProgEvidence.Refusal.Feedback, "Run `find src -name '*.tmp'`") || strings.Contains(outNoProgEvidence.Refusal.Feedback, "Available read-only forms") || outNoProgEvidence.Refusal.Details["code_instead"] != nil {
		t.Fatalf("missing program evidence still recommended form:\n%+v", outNoProgEvidence.Refusal)
	}
	if !strings.Contains(outNoProgEvidence.Refusal.Feedback, "Fix: Do not retry this operation on the active read-only surface.") {
		t.Fatalf("missing grounded fallback without program evidence:\n%s", outNoProgEvidence.Refusal.Feedback)
	}

	// 2b. Recipient reach matrix: loadable (with request_tools + proven program reach vs denied/missing program reach), absent, and unknown reach.
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Turn.TurnOfferedToolNames = []string{"request_tools"}
		tc.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools"}, []string{"command"})
		tc.Turn.TurnPrograms = func(name string) bool { return name == "find" }
	}
	rawLoadable, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "find -delete with loadable command", err)
	outLoadable := decodeOutcome(t, rawLoadable)
	if outLoadable.Refusal == nil || !strings.Contains(outLoadable.Refusal.Feedback, "Fix: Load `command` via `request_tools` and run `find src -name '*.tmp'` to list the paths selected for deletion and report the affected files without deleting them.") {
		t.Fatalf("loadable command feedback missing request_tools remedy:\n%+v", outLoadable.Refusal)
	}

	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Turn.TurnOfferedToolNames = []string{"request_tools"}
		tc.Turn.TurnToolPlan = toolsurface.Compile([]string{"request_tools"}, []string{"command"})
		tc.Turn.TurnPrograms = nil
	}
	rawLoadableNoProg, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "find -delete with loadable command but no program evidence", err)
	outLoadableNoProg := decodeOutcome(t, rawLoadableNoProg)
	if outLoadableNoProg.Refusal == nil || strings.Contains(outLoadableNoProg.Refusal.Feedback, "Load `command`") || outLoadableNoProg.Refusal.Details["code_instead"] != nil {
		t.Fatalf("loadable command without program evidence still recommended candidate:\n%+v", outLoadableNoProg.Refusal)
	}

	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Turn.TurnOfferedToolNames = []string{"task"}
		tc.Turn.TurnToolPlan = toolsurface.Compile([]string{"task"}, nil)
	}
	rawAbsent, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "find -delete with absent command", err)
	outAbsent := decodeOutcome(t, rawAbsent)
	if outAbsent.Refusal == nil || strings.Contains(outAbsent.Refusal.Feedback, "Run `find src -name '*.tmp'`") || strings.Contains(outAbsent.Refusal.Feedback, "Load `command`") || strings.Contains(outAbsent.Refusal.Feedback, "Available read-only forms") || outAbsent.Refusal.Details["code_instead"] != nil {
		t.Fatalf("absent command still recommended executable remedy or surface availability:\n%+v", outAbsent.Refusal)
	}
	if !strings.Contains(outAbsent.Refusal.Feedback, "Fix: Do not retry this operation on the active read-only surface.") {
		t.Fatalf("absent command missing complete grounded fallback text:\n%s", outAbsent.Refusal.Feedback)
	}

	f.Context = nil
	f.noReach = true
	rawUnknownReach, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "find -delete with unknown reach", err)
	outUnknownReach := decodeOutcome(t, rawUnknownReach)
	if outUnknownReach.Refusal == nil || strings.Contains(outUnknownReach.Refusal.Feedback, "Run `find src -name '*.tmp'`") || strings.Contains(outUnknownReach.Refusal.Feedback, "Load `command`") || strings.Contains(outUnknownReach.Refusal.Feedback, "Available read-only forms") || outUnknownReach.Refusal.Details["code_instead"] != nil || outUnknownReach.Refusal.Details["can_command"] != nil {
		t.Fatalf("unknown reach still recommended executable remedy or invented availability:\n%+v", outUnknownReach.Refusal)
	}
	if !strings.Contains(outUnknownReach.Refusal.Feedback, "Cause: Ask mode permits only read-only inspection and blocks `mutation`.") || !strings.Contains(outUnknownReach.Refusal.Feedback, "Fix: Do not retry this operation on the active read-only surface.") {
		t.Fatalf("unknown reach lost precise cause or complete grounded fallback:\n%s", outUnknownReach.Refusal.Feedback)
	}
	f.noReach = false

	// 2c. Candidate with non-read or unknown nested effects (find -delete -exec rm / non-native) and unresolved relative target (../outside).
	for _, nonReadFind := range []string{
		`find src -name '*.tmp' -exec rm {} \; -delete`,
		`find src -name '*.tmp' -exec unknown_external_binary {} \; -delete`,
		`find ../outside -name '*.tmp' -delete`,
	} {
		rawNonRead, callErr := f.Call(t, t.Context(), nonReadFind)
		testutil.FailErr(t, "non-read or escaping candidate: "+nonReadFind, callErr)
		outNonRead := decodeOutcome(t, rawNonRead)
		if outNonRead.Refusal == nil || outNonRead.Refusal.Details["code_instead"] != nil || strings.Contains(outNonRead.Refusal.Feedback, "Fix: Run `find") {
			t.Fatalf("%q unexpectedly recommended a read alternative:\n%+v", nonReadFind, outNonRead.Refusal)
		}
		if !strings.Contains(outNonRead.Refusal.Feedback, "Cause: Ask mode permits only read-only inspection and blocks") {
			t.Fatalf("%q missing precise cause:\n%s", nonReadFind, outNonRead.Refusal.Feedback)
		}
	}

	deniedTargetCtx := tools.WithAgentPolicy(t.Context(), func(_ context.Context, targets []agentpolicy.Target) error {
		for _, target := range targets {
			if !target.Mutation && strings.HasSuffix(target.Path, "notes.txt") {
				return errors.New("policy denied target read")
			}
		}
		return nil
	})
	rawDeniedTarget, err := f.Call(t, deniedTargetCtx, "sed -i 's/beta/delta/' src/notes.txt")
	testutil.FailErr(t, "sed -i with target read denied", err)
	outDeniedTarget := decodeOutcome(t, rawDeniedTarget)
	if outDeniedTarget.Refusal == nil || strings.Contains(outDeniedTarget.Refusal.Feedback, "Run `sed s/beta/delta/ src/notes.txt`") || outDeniedTarget.Refusal.Details["code_instead"] != nil {
		t.Fatalf("unreadable target still recommended in feedback:\n%s", outDeniedTarget.Refusal.Feedback)
	}
	if !strings.Contains(outDeniedTarget.Refusal.Feedback, "Cause: Ask mode permits only read-only inspection and blocks `mutation`.") {
		t.Fatalf("missing precise cause when target access removed:\n%s", outDeniedTarget.Refusal.Feedback)
	}

	// 3. Operation with no useful read equivalent (mkdir).
	rawMkdir, err := f.Call(t, t.Context(), "mkdir src/newdir")
	testutil.FailErr(t, "mkdir in Ask", err)
	outMkdir := decodeOutcome(t, rawMkdir)
	if outMkdir.Refusal == nil || outMkdir.Refusal.Details["code_instead"] != nil || !strings.Contains(outMkdir.Refusal.Feedback, "Fix: Do not retry this operation on the active read-only surface.") {
		t.Fatalf("mkdir feedback unexpectedly proposed a read form:\n%s", outMkdir.Refusal.Feedback)
	}

	// 4. Partially completed multi-statement program with retained results, including a program-local directory change (cd src) with distinguishable targets.
	testutil.FailErr(t, "write root notes.txt", os.WriteFile(filepath.Join(f.Dir, "notes.txt"), []byte("root-only\n"), 0o644))
	rawCdSed, err := f.Call(t, t.Context(), "cd src && sed -i 's/beta/delta/' notes.txt")
	testutil.FailErr(t, "cd src && sed -i notes.txt in Ask", err)
	outCdSed := decodeOutcome(t, rawCdSed)
	if outCdSed.Refusal == nil || !strings.Contains(outCdSed.Refusal.Feedback, "Fix: Run `sed s/beta/delta/ src/notes.txt` to preview the transformed output on stdout without modifying the file in place.") {
		t.Fatalf("cd src && sed -i feedback did not preserve target identity for fresh call:\n%+v", outCdSed.Refusal)
	}
	cdSedSuggested, _ := outCdSed.Refusal.Details["code_instead"].(string)
	if cdSedSuggested != "sed s/beta/delta/ src/notes.txt" {
		t.Fatalf("cd src && sed -i code_instead = %q, want %q", cdSedSuggested, "sed s/beta/delta/ src/notes.txt")
	}
	rawCdSedFresh, err := f.Call(t, t.Context(), cdSedSuggested)
	testutil.FailErr(t, "run recommended fresh sed call from initial directory", err)
	outCdSedFresh := decodeOutcome(t, rawCdSedFresh)
	if outCdSedFresh.Status != 0 || outCdSedFresh.Refusal != nil || !strings.Contains(outCdSedFresh.Stdout, "alpha\ndelta\ngamma\n") || strings.Contains(outCdSedFresh.Stdout, "root-only") {
		t.Fatalf("fresh sed call after cd inspected wrong target:\n%+v", outCdSedFresh)
	}

	rawPartial, err := f.Call(t, t.Context(), "cat src/notes.txt && find src -name '*.tmp' -delete")
	testutil.FailErr(t, "partial multi-statement program in Ask", err)
	outPartial := decodeOutcome(t, rawPartial)
	if !strings.Contains(outPartial.Stdout, "alpha\nbeta\ngamma\n") {
		t.Fatalf("partial program lost earlier stdout: %q", outPartial.Stdout)
	}
	for _, want := range []string{
		"Rejected: `find` (`-delete`) at 1:22 is unavailable in Ask mode.",
		"State: Operations started: 1. Completed effects: 0. Uncertain effects: 0.",
		"Why: Earlier statements in this program remain authoritative (`observed`); do not replay completed work.",
		"Fix: Run `find src -name '*.tmp'` to list the paths selected for deletion and report the affected files without deleting them.",
	} {
		if outPartial.Refusal == nil || !strings.Contains(outPartial.Refusal.Feedback, want) {
			t.Fatalf("partial program feedback missing %q:\n%+v", want, outPartial.Refusal)
		}
	}

	// 5. Delayed refusal delivered after upgrade to Agent mode.
	f.sessionMode = api.SessionModeAgent
	rawDelayed, err := f.Call(t, t.Context(), "sed -i 's/beta/delta/' src/notes.txt")
	testutil.FailErr(t, "delayed Ask refusal after upgrade", err)
	outDelayed := decodeOutcome(t, rawDelayed)
	for _, want := range []string{
		"Rejected: `sed` (`-i`) at 1:1 was refused under launch-time Ask authority.",
		"Cause: This invocation started while Ask mode was active and kept its launch-time read-only authority across the session upgrade, which blocks `mutation`.",
		"Fix: Run `sed s/beta/delta/ src/notes.txt` to preview the transformed output on stdout without modifying the file in place.",
		"Next: Agent mode is now active for new calls; if the change is still needed, issue a fresh Agent-mode call under current permissions without replaying already completed statements.",
	} {
		if outDelayed.Refusal == nil || !strings.Contains(outDelayed.Refusal.Feedback, want) {
			t.Fatalf("delayed refusal feedback missing %q:\n%+v", want, outDelayed.Refusal)
		}
	}
	if strings.Contains(outDelayed.Refusal.Feedback, "switch to Agent mode") || strings.Contains(outDelayed.Refusal.Feedback, "is unavailable in Ask mode") {
		t.Fatalf("delayed refusal after upgrade misstated current mode:\n%s", outDelayed.Refusal.Feedback)
	}

	// 6. Independent restriction in Agent mode (worker_readonly profile) attributes denial to the profile, never Ask mode.
	f.askMode = false
	f.sessionMode = api.SessionModeAgent
	readOnlyEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly)
	testutil.FailErr(t, "read-only command effects", err)
	allowedTools := map[string]bool{"command": true, "read": true, "find": true, "grep": true, "list_dir": true, "stat": true, "wc": true, "jq": true}
	roBoundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: "worker_readonly", Tools: allowedTools, CommandEffects: readOnlyEffects,
	}})
	f.Boundary, f.Runtime.Boundary = roBoundary, roBoundary
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(roBoundary), f.Runtime.Registry, "worker_readonly")
	hints, err := guidance.LoadHintConfigStock()
	testutil.FailErr(t, "load hint catalog", err)
	f.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Identity.Agent = "worker_readonly"
		tc.Identity.ParentSessionID = "root-session"
	}
	rawWorkerRO, err := f.Call(t, t.Context(), "find src -name '*.tmp' -delete")
	testutil.FailErr(t, "worker_readonly refusal in Agent mode", err)
	outWorkerRO := decodeOutcome(t, rawWorkerRO)
	for _, want := range []string{
		"Rejected: `find` (`-delete`) at 1:1 is outside profile **worker_readonly**.",
		"Cause: Its bound effect set (`mutation`) is not permitted by the active command surface.",
		"Fix: Run `find src -name '*.tmp'` to list the paths selected for deletion and report the affected files without deleting them.",
		"report the required change to the coordinator",
	} {
		if outWorkerRO.Refusal == nil || !strings.Contains(outWorkerRO.Refusal.Feedback, want) {
			t.Fatalf("worker_readonly feedback missing %q:\n%+v", want, outWorkerRO.Refusal)
		}
	}
	if strings.Contains(outWorkerRO.Refusal.Feedback, "Ask") {
		t.Fatalf("worker_readonly feedback in Agent mode mentioned Ask:\n%s", outWorkerRO.Refusal.Feedback)
	}

	// 7. Mixed-permission profile (web_research: read_only + network_egress, denying mutation) renders profile-neutral fallback variants.
	mixedEffects, err := toolcontract.NewCommandEffects(toolcontract.CommandReadOnly, toolcontract.CommandNetworkEgress)
	testutil.FailErr(t, "mixed command effects", err)
	mixedTools := map[string]bool{"command": true, "read": true, "find": true, "grep": true, "list_dir": true, "stat": true, "wc": true, "jq": true, "http_request": true, "mkdir": true}
	mixedBoundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{
		ID: "web_research", Tools: mixedTools, CommandEffects: mixedEffects,
	}})
	f.Boundary, f.Runtime.Boundary = mixedBoundary, mixedBoundary
	f.Runtime.Executor = toolexecution.NewExecutor(toolprofiles.NewProfilePolicyEngine(mixedBoundary), f.Runtime.Registry, "web_research")
	f.Runtime.Executor.Rejections.SetRejectFormatter(guidance.NewStaticRejectFormatter(hints))
	f.Context = func(_ string, tc *tools.ToolContext) {
		tc.Identity.Agent = "web_research"
		tc.Identity.ParentSessionID = "root-session"
	}

	// 7a. Mixed profile denying mutation when candidate is absent (e.g. find ../outside -delete) renders profile-neutral expected_form fallback.
	rawMixedWithForm, err := f.Call(t, t.Context(), "find ../outside -name '*.tmp' -delete")
	testutil.FailErr(t, "web_research find ../outside -delete", err)
	outMixedWithForm := decodeOutcome(t, rawMixedWithForm)
	for _, want := range []string{
		"Rejected: `find` (`-delete`) at 1:1 is outside profile **web_research**.",
		"Cause: Its bound effect set (`mutation`) is not permitted by the active command surface.",
		"Fix: Available forms on this surface: find [default-predicates-print].",
	} {
		if outMixedWithForm.Refusal == nil || !strings.Contains(outMixedWithForm.Refusal.Feedback, want) {
			t.Fatalf("web_research expected_form fallback missing %q:\n%+v", want, outMixedWithForm.Refusal)
		}
	}
	if strings.Contains(outMixedWithForm.Refusal.Feedback, "read-only") || strings.Contains(outMixedWithForm.Refusal.Feedback, "Ask") {
		t.Fatalf("web_research expected_form fallback mislabeled mixed surface as read-only or Ask:\n%s", outMixedWithForm.Refusal.Feedback)
	}

	// 7b. Mixed profile denying pure mutation with no available form (mkdir) renders profile-neutral no-form fallback.
	rawMixedNoForm, err := f.Call(t, t.Context(), "mkdir src/newdir")
	testutil.FailErr(t, "web_research mkdir", err)
	outMixedNoForm := decodeOutcome(t, rawMixedNoForm)
	for _, want := range []string{
		"Rejected: `mkdir` (`each-target`) at 1:1 is outside profile **web_research**.",
		"Cause: Its bound effect set (`mutation`) is not permitted by the active command surface.",
		"Fix: Do not retry this operation on the active surface.",
	} {
		if outMixedNoForm.Refusal == nil || !strings.Contains(outMixedNoForm.Refusal.Feedback, want) {
			t.Fatalf("web_research no-form fallback missing %q:\n%+v", want, outMixedNoForm.Refusal)
		}
	}
	if strings.Contains(outMixedNoForm.Refusal.Feedback, "read-only") || strings.Contains(outMixedNoForm.Refusal.Feedback, "Ask") {
		t.Fatalf("web_research no-form fallback mislabeled mixed surface as read-only or Ask:\n%s", outMixedNoForm.Refusal.Feedback)
	}
}
