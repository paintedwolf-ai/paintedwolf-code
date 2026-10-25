package literal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolprofiles"
	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools/native"
)

// TestMissingInputsReportGNUDiagnosticsAndContinue: an absent operand is an
// ordinary failure on stderr with the utility's own status; later operands run.
func TestMissingInputsReportGNUDiagnosticsAndContinue(t *testing.T) {
	h := formHarness(t)
	for _, c := range []formCase{
		{argv: "cat missing input", stdout: "alpha\nbeta", stderr: "cat: missing: No such file or directory\n", status: 1},
		{argv: "cat dir input", stdout: "alpha\nbeta", stderr: "cat: dir: Is a directory\n", status: 1},
		{argv: "head missing", stderr: "head: cannot open 'missing' for reading: No such file or directory\n", status: 1},
		{argv: "tail missing", stderr: "tail: cannot open 'missing' for reading: No such file or directory\n", status: 1},
		{argv: "nl missing", stderr: "nl: missing: No such file or directory\n", status: 1},
		{argv: "xxd missing", stderr: "xxd: missing: No such file or directory\n", status: 1},
		{argv: "hexdump -C missing input", stdout: "00000000  61 6c 70 68 61 0a 62 65  74 61                    |alpha.beta|\n0000000a\n", stderr: "hexdump: missing: No such file or directory\n", status: 1},
		{argv: "base64 missing", stderr: "base64: missing: No such file or directory\n", status: 1},
		{argv: "md5sum missing empty", stdout: "d41d8cd98f00b204e9800998ecf8427e  empty\n", stderr: "md5sum: missing: No such file or directory\n", status: 1},
		{argv: "file missing", stderr: "file: missing: No such file or directory\n", status: 1},
		{argv: "outline --path missing", stderr: "outline: missing: No such file or directory\n", status: 1},
	} {
		t.Run(c.argv, func(t *testing.T) {
			h.exec("", strings.Fields(c.argv)...).expect(t, c.argv, c.stdout, c.stderr, c.status)
		})
	}
}

// TestPermissionDeniedIsAnOrdinaryFailure: the OS refusing a read is the
// utility's failure, not a host authority stop.
func TestPermissionDeniedIsAnOrdinaryFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not bind the superuser")
	}
	h := formHarness(t)
	seed(t, h.root, map[string]string{"secret": "hidden\n", "closed/inner": "x\n"})
	testutil.FailErr(t, "close secret", os.Chmod(filepath.Join(h.root, "secret"), 0))
	testutil.FailErr(t, "close dir", os.Chmod(filepath.Join(h.root, "closed"), 0))
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(h.root, "closed"), 0o755) })
	h.exec("", "cat", "secret", "input").expect(t, "cat secret", "alpha\nbeta", "cat: secret: Permission denied\n", 1)
	h.exec("", "sha1sum", "secret").expect(t, "sha1sum secret", "", "sha1sum: secret: Permission denied\n", 1)
	h.exec("", "ls", "closed").expect(t, "ls closed", "", "ls: cannot open directory 'closed': Permission denied\n", 2)
	h.exec("", "stat", "-c", "%s", "closed/inner").expect(t, "stat inside closed", "", "stat: cannot stat 'closed/inner': Permission denied\n", 1)
}

// TestConfinementRefusesOutsideAuthorizedRoots: operands outside the roots,
// links that leave them, and entries outside the profile's read scope stop
// the program with a typed refusal, emit nothing and pin nothing.
func TestConfinementRefusesOutsideAuthorizedRoots(t *testing.T) {
	h := formHarness(t)
	const outside = "/etc/hosts"
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("confinement fixture %s: %v", outside, err)
	}
	symlink(t, outside, filepath.Join(h.root, "escape"))
	refused := func(h *harness, argv ...string) {
		t.Helper()
		r := h.exec("", argv...)
		if toolrejection.AsToolReject(r.err) == nil || r.result.StopCode == "" || r.result.PublicationEligible || r.stdout != "" {
			t.Fatalf("%v escaped confinement: %+v err %v stdout %q", argv, r.result, r.err, r.stdout)
		}
		if len(h.pins.pins[r.execution]) != 0 {
			t.Fatalf("%v pinned an unauthorized read", argv)
		}
	}
	for _, argv := range [][]string{
		{"cat", outside}, {"cat", "escape"}, {"head", "-n", "1", "escape"}, {"sha256sum", "escape"},
		{"diff", "input", "escape"}, {"stat", "-L", "-c", "%s", "escape"}, {"ls", "/etc"}, {"file", outside}, {"xxd", "escape"},
	} {
		refused(h, argv...)
	}
	// Describing the link itself reads no target content.
	h.exec("", "stat", "-c", "%F", "escape").expect(t, "stat link", "symbolic link\n", "", 0)

	scoped := formHarness(t)
	seed(t, scoped.root, map[string]string{"scoped/in": "inside\n"})
	boundary := sandbox.NewBoundary(sandbox.Config{ProjectRootRequired: true, RejectSymlinkEscape: true}, []sandbox.ToolProfile{{ID: toolprofiles.DefaultToolProfileID, Tools: map[string]bool{"read": true}, ReadGlobs: []string{"scoped", "scoped/**"}}})
	scoped.owners.Revisions = native.ExecutionRevisions{Boundary: boundary}
	scoped.boundary, scoped.cwd = boundary, "scoped"
	scoped.exec("", "cat", "in").expect(t, "scoped read", "inside\n", "", 0)
	scoped.exec("", "stat", "-c", "%s", "in").expect(t, "scoped metadata", "7\n", "", 0)
	refused(scoped, "cat", "../lines")
	refused(scoped, "wc", "-l", "../crlf")
	refused(scoped, "stat", "-c", "%F", "../lines")
}

// TestInputsAboveTheReadBudgetFailExplicitly: a capture above its budget is
// refused before any byte or outline reaches stdout.
func TestInputsAboveTheReadBudgetFailExplicitly(t *testing.T) {
	h := formHarness(t)
	h.owners.ReadBytes = 64
	seed(t, h.root, map[string]string{"large": strings.Repeat("0123456789\n", 20)})
	for _, argv := range [][]string{{"cat", "large"}, {"tail", "-n", "1", "large"}, {"wc", "-l", "large"}, {"outline", "--path", "large"}, {"cat", "input", "large"}} {
		r := h.exec("", argv...)
		r.refused(t, strings.Join(argv, " "), "READ_FILE_TOO_LARGE")
		if strings.Contains(r.stdout, "0123456789") {
			t.Fatalf("%v emitted part of an over-budget input: %q", argv, r.stdout)
		}
	}
}

// TestStreamsStayWithinTheirBound: output above the stream ceiling and
// gathered stdin above it refuse rather than truncate.
func TestStreamsStayWithinTheirBound(t *testing.T) {
	h := formHarness(t)
	h.owners.StreamBytes = 8
	seed(t, h.root, map[string]string{"long": strings.Repeat("y", 64)})
	r := h.exec("", "cat", "long")
	r.refused(t, "cat long", "COMMAND_LIMIT_EXCEEDED")
	if r.stdout != "" {
		t.Fatalf("over-bound write delivered %q", r.stdout)
	}
	h.exec(strings.Repeat("z", 64), "tail", "-n", "1").refused(t, "tail stdin", "COMMAND_LIMIT_EXCEEDED")
	h.exec("", "cat", "input").refused(t, "cat input", "COMMAND_LIMIT_EXCEEDED")
	h.exec("", "head", "-c", "4", "input").expect(t, "head within bound", "alph", "", 0)
}

// TestUnsupportedAndInvalidFormsRefuse: forms outside the catalog, or values
// it cannot accept, refuse with the catalog's codes and no fallback.
func TestUnsupportedAndInvalidFormsRefuse(t *testing.T) {
	h := formHarness(t)
	for _, c := range []struct {
		argv, code string
	}{
		{"cat --not-a-version-1-option input", "COMMAND_OPTION_UNSUPPORTED"},
		{"tail -f input", "COMMAND_OPTION_UNSUPPORTED"},
		{"ls --hyperlink input", "COMMAND_OPTION_UNSUPPORTED"},
		{"ls --color=always input", "COMMAND_OPERAND_INVALID"},
		{"wc -x input", "COMMAND_OPTION_UNSUPPORTED"},
		{"head -n abc input", "COMMAND_OPERAND_INVALID"},
		{"head -n -1 input", "COMMAND_OPERAND_INVALID"},
		{"tail -c x input", "COMMAND_OPERAND_INVALID"},
		{"nl -b x input", "COMMAND_OPERAND_INVALID"},
		{"nl -w 0 input", "COMMAND_OPERAND_INVALID"},
		{"nl input lines", "COMMAND_OPERAND_INVALID"},
		{"xxd -g 3 input", "COMMAND_OPERAND_INVALID"},
		{"xxd input lines", "COMMAND_OPERAND_INVALID"},
		{"xxd -s x input", "COMMAND_OPERAND_INVALID"},
		{"hexdump input", "COMMAND_OPERAND_INVALID"},
		{"hexdump -C -s -1 input", "COMMAND_OPERAND_INVALID"},
		{"stat -c %-5q input", "COMMAND_OPERAND_INVALID"},
		{"stat -c %s -f %z input", "COMMAND_OPERAND_INVALID"},
		{"stat -c %q input", "COMMAND_OPERAND_INVALID"},
		{"stat -c %s", "COMMAND_OPERAND_INVALID"},
		{"stat --file-system input", "COMMAND_PLATFORM_UNAVAILABLE"},
		{"diff input", "COMMAND_OPERAND_INVALID"},
		{"diff -U x input input", "COMMAND_OPERAND_INVALID"},
		{"base64 input lines", "COMMAND_OPERAND_INVALID"},
		{"base64 -w x input", "COMMAND_OPERAND_INVALID"},
		{"shasum -a 384 input", "COMMAND_OPERAND_INVALID"},
		{"file", "COMMAND_OPERAND_INVALID"},
		{"outline", "COMMAND_OPERAND_INVALID"},
		{"outline --path input lines", "COMMAND_OPERAND_INVALID"},
		{"repo-map --depth invalid", "COMMAND_OPERAND_INVALID"},
	} {
		t.Run(c.argv, func(t *testing.T) {
			r := h.exec("", strings.Fields(c.argv)...)
			r.refused(t, c.argv, c.code)
			if r.result.Status != 2 || r.stdout != "" {
				t.Fatalf("refusal status %d stdout %q", r.result.Status, r.stdout)
			}
		})
	}
	r := h.exec("", "cat", "--not-a-version-1-option", "input")
	if data := toolrejection.AsToolReject(r.err).Data; data["expected_form"] != "cat [OPTIONS] [--] [PATH ...]" || data["word"] != "--not-a-version-1-option" {
		t.Fatalf("unsupported option data = %+v", data)
	}
}

// TestOperationBoundaryRejectsForeignCommands: Prepare only binds the
// catalog commands these owners serve, under their own operation identity.
func TestOperationBoundaryRejectsForeignCommands(t *testing.T) {
	h := formHarness(t)
	request := requestFor(h, "cat", "input")
	request.OperationID = "wc"
	if _, err := h.owners.Prepare(mustPaths(t, h), request); err == nil {
		t.Fatal("cat bound under the wc operation")
	}
	if _, ok := h.owners.Operation("grep"); ok {
		t.Fatal("grep is not a literal command")
	}
	want := map[string]string{"cat": "read", "head": "read", "tail": "read", "nl": "read", "outline": "read", "file": "read", "xxd": "read", "hexdump": "read",
		"shasum": "read", "sha1sum": "read", "sha256sum": "read", "sha512sum": "read", "md5sum": "read", "base64": "read",
		"wc": "wc", "ls": "list_dir", "stat": "stat", "diff": "diff", "summarize": "summarize", "repo-map": "survey_repo"}
	if got := h.owners.Commands(); len(got) != len(want) {
		t.Fatalf("commands = %v", got)
	}
	for command, operation := range want {
		if got, _ := h.owners.Operation(command); got != operation {
			t.Errorf("%s operation = %q, want %q", command, got, operation)
		}
	}
	prepared, err := h.owners.Prepare(mustPaths(t, h), requestFor(h, "head", "-3", "--", "-input"))
	testutil.FailErr(t, "prepare head", err)
	if prepared.Review.OperationID != "read" || len(prepared.Review.Options) != 1 || prepared.Review.Options[0].Option != "-n" || prepared.Review.Options[0].Values[0] != "3" || prepared.Review.Operands[0] != "-input" {
		t.Fatalf("review = %+v", prepared.Review)
	}
}
