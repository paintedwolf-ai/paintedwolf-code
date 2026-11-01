//go:build darwin || linux

package hostenv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/exec"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoveryReportsNativeKeywordAndLaunchSelection(t *testing.T) {
	h := newHarness(t, defaultHost())
	tool, cat := filepath.Join(h.path, "pwtool"), filepath.Join(h.path, "cat")
	cases := []struct {
		source, stdout, stderr string
		status                 int
	}{
		{"which -- cat pwtool", "cat\n" + tool + "\n", "", 0},
		{"which git", "git\n", "", 0},
		{"which jq", "jq\n", "", 0},
		{"which missing plain if", "", "", 1},
		{"command -v cat git pwtool jq if", "cat\ngit\n" + tool + "\njq\nif\n", "", 0},
		{"command -v -- missing", "", "", 1},
		{"command -V cat pwtool if missing", "cat is a native built-in\npwtool is " + tool + "\nif is a shell keyword\n", "command: missing: not found\n", 1},
		{"type cat git", "cat is a native built-in\ngit is a native built-in\n", "", 0},
		{"type -t -- cat pwtool if", "builtin\nfile\nkeyword\n", "", 0},
		{"type -t missing", "", "", 1},
		{"type missing", "", "type: missing: not found\n", 1},
		{"type -a cat pwtool", "cat is a native built-in\ncat is " + cat + "\npwtool is " + tool + "\n", "", 0},
		{"type -a -t cat", "builtin\nfile\n", "", 0},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			h.run(c.source).expectOutput(t, c.source, c.stdout, c.stderr, c.status)
		})
	}
}

// The reported program is the launch's own selection: the same final PATH
// (resolved PATH plus host resource directories) and the same refusal for a
// key a launch reserves.
func TestDiscoveryUsesTheLaunchEnvironment(t *testing.T) {
	h := newHarness(t, defaultHost())
	extra := t.TempDir()
	testutil.FailErr(t, "write extra tool", writeExecutable(filepath.Join(extra, "helper")))
	h.tc.Host.HostResourcePathExtra = []string{extra}
	selected, err := exec.LaunchLookup("helper", exec.ExecOpts{Dir: h.root, PathExtra: []string{extra}})
	testutil.FailErr(t, "launch lookup", err)
	h.run("which helper").expectOutput(t, "which helper", selected+"\n", "", 0)
	if selected != filepath.Join(extra, "helper") {
		t.Fatalf("launch selected %q", selected)
	}
	h.env = []string{"LD_PRELOAD=/tmp/x.so"}
	h.run("which pwtool").expectRefusal(t, "which with a reserved key", "COMMAND_OPERAND_INVALID")
	h.env = nil
	h.run("which pwtool").expectOutput(t, "which without reserved keys", filepath.Join(h.path, "pwtool")+"\n", "", 0)
}

func TestDiscoveryRefusesUnsupportedForms(t *testing.T) {
	h := newHarness(t, defaultHost())
	r := h.run("which --bogus cat")
	reject := r.expectRefusal(t, "which --bogus", "COMMAND_OPTION_UNSUPPORTED")
	if reject.Data["word"] != "--bogus" || reject.Data["source_start_byte"] != r.argv[1].Span.StartByte || reject.Data["source_end_byte"] != r.argv[1].Span.EndByte {
		t.Fatalf("rejection data %+v, want the --bogus word span %+v", reject.Data, r.argv[1].Span)
	}
	h.run("command cat").expectRefusal(t, "command without -v", "COMMAND_OPERAND_INVALID")
	h.cwd = "missing-directory"
	h.run("type cat").expectOutput(t, "native type needs no lookup", "cat is a native built-in\n", "", 0)
	h.run("which cat").expectOutput(t, "native which needs no lookup", "cat\n", "", 0)
	h.run("type -a cat").expectRefusal(t, "type -a cat without a launch directory", "CWD_NOT_DIRECTORY")
	h.run("which pwtool").expectRefusal(t, "which without a launch directory", "CWD_NOT_DIRECTORY")
}

func writeExecutable(path string) error {
	return os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755)
}
