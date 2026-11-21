package command_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitengine"
	"github.com/lycaon/lycaon/internal/hostcmd"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/command"
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
