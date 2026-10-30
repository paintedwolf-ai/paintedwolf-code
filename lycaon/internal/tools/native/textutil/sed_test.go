//go:build darwin || linux

package textutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/toolrejection"
)

// sedHarness runs sed on the shared POSIX engine the owners construct.
func sedHarness(t *testing.T) *harness { return newHarness(t) }

func TestSedGolden(t *testing.T) {
	lines := "a\nb\nc\nd\n"
	sedHarness(t).goldens(t, []golden{
		{"sed -n 1,2p", lines, "a\nb\n", 0},
		{"sed '1d;$d'", lines, "b\nc\n", 0},
		{"sed -n '$p'", lines, "d\n", 0},
		{"sed 2q", lines, "a\nb\n", 0},
		{"sed -n '2{p;q;}'", lines, "b\n", 0},
		{"sed '2,3!d'", lines, "b\nc\n", 0},
		{"sed '3,1d'", lines, "a\nb\nd\n", 0},
		{"sed -n '/^start$/,/^end$/p'", "x\nstart\na\nend\ny\n", "start\na\nend\n", 0},
		{"sed -n '/a/,/a/p'", "a\nb\na\nc\n", "a\nb\na\n", 0},
		{"sed -n '/b/,$p'", lines, "b\nc\nd\n", 0},
		{"sed s/a/b/", "aaa\n", "baa\n", 0},
		{"sed s/a/b/g", "aaa\n", "bbb\n", 0},
		{"sed s/a/b/2", "aaa\n", "aba\n", 0},
		{"sed s/a/b/2g", "aaaa\n", "abbb\n", 0},
		{"sed 's/x*/-/g'", "abc\n", "-a-b-c-\n", 0},
		{"sed 's/b*/x/g'", "abc\n", "xaxcx\n", 0},
		{`sed 's/\(a*\)b/[\1]&/'`, "aab\n", "[aa]aab\n", 0},
		{`sed -E 's/(x+)(y?)/<\2\1>/g'`, "xxy x\n", "<yxx> <x>\n", 0},
		{`sed 's|/|\||g'`, "a/b/c\n", "a|b|c\n", 0},
		{`sed 's/a/\n/'`, "bab\n", "b\nb\n", 0},
		{`sed 's/a/\&/'`, "a\n", "&\n", 0},
		{"sed -n s/a/b/p", "a\nc\n", "b\n", 0},
		{"sed -e s/a/b/ -e s/b/c/", "a\n", "c\n", 0},
		{"sed 'a\\\nafter'", "x\ny\n", "x\nafter\ny\nafter\n", 0},
		{"sed '1i before'", "x\ny\n", "before\nx\ny\n", 0},
		{"sed '2,3c\\\nreplaced'", lines, "a\nreplaced\nd\n", 0},
		{"sed '$!c gone'", "x\ny\n", "gone\ny\n", 0},
		{"sed 'a one; two'", "x\n", "x\none; two\n", 0},
		{"sed p", "x", "x\nx", 0},
		{"sed s/x/y/", "a\nx", "a\ny", 0},
		{"sed ''", "a\x00b\n\xff\n", "a\x00b\n\xff\n", 0},
		{"sed -n p", "", "", 0},
		{`sed 's/\(ab\)\1/X/'`, "ababc\n", "Xc\n", 0},
		{`sed -E 's/(a|ab)(c|bcd)(d*)/[\1|\2|\3]/'`, "abcd\n", "[ab|c|d]\n", 0},
		{`sed 's/a\tb/X/'`, "a\tb\n", "X\n", 0},
		{"sed 's/é/e/g'", "café é\n", "cafe e\n", 0},
		{"sed '1!G;h;$!d'", lines, "d\nc\nb\na\n", 0},
	})
}

func TestSedRefusesBeforeReading(t *testing.T) {
	h := sedHarness(t)
	for _, c := range []struct{ source, code string }{
		{"sed 'e date'", "COMMAND_OPTION_UNSUPPORTED"},
		{"sed 's/a/b/e'", "COMMAND_OPTION_UNSUPPORTED"},
		{"sed 's/a/b/M'", "COMMAND_OPTION_UNSUPPORTED"},
		{"sed -i s/a/b/", "COMMAND_OPERAND_INVALID"},
		{"sed -i s/a/b/ -", "COMMAND_OPERAND_INVALID"},
		{"sed -f missing-script", "COMMAND_OPERAND_INVALID"},
		{"sed -f -", "COMMAND_OPERAND_INVALID"},
		{"sed 's/a/b'", "COMMAND_OPERAND_INVALID"},
		{`sed 's/a/\1/'`, "COMMAND_OPERAND_INVALID"},
		{"sed '0d'", "COMMAND_OPERAND_INVALID"},
		{"sed '{p'", "COMMAND_OPERAND_INVALID"},
		{"sed '}'", "COMMAND_OPERAND_INVALID"},
		{"sed 'pq'", "COMMAND_OPERAND_INVALID"},
		{"sed 'b nowhere'", "COMMAND_OPERAND_INVALID"},
		{"sed 's/[/x/'", "COMMAND_OPERAND_INVALID"},
		{"sed 's//x/'", "COMMAND_OPERAND_INVALID"},
		{"sed ':a;ba'", "COMMAND_LIMIT_EXCEEDED"},
		{"sed", "COMMAND_OPERAND_INVALID"},
	} {
		h.seed("f", "a\n")
		h.run(c.source, "a\n").expectRefusal(t, c.source, c.code)
	}
	h.seed("writes.sed", "w out\n")
	h.run("sed -f writes.sed", "a\n").expectRefusal(t, "w from a script file", "COMMAND_NATIVE_FORM_UNSUPPORTED")
	unwired := newHarness(t)
	unwired.owners.Regex = nil
	unwired.run("sed p", "a\n").expectRefusal(t, "no shared engine", "COMMAND_PLATFORM_UNAVAILABLE")
}

// GNU forms beyond the POSIX core: script files, separate streams, NUL
// records, w files and in-place backups, all through pinned publication.
func TestSedGNUForms(t *testing.T) {
	h := sedHarness(t)
	h.seed("one", "a\nb\n")
	h.seed("two", "c\nd\n")
	h.seed("script.sed", "s/a/A/\n$!d\n")
	h.run("sed -f script.sed -e 's/$/!/' one two", "").expectOutput(t, "-f then -e", "d!\n", 0)
	h.run("sed -s -n '$p' one two", "").expectOutput(t, "-s", "b\nd\n", 0)
	h.run("sed -sn 1F one two", "").expectOutput(t, "F names files", "one\ntwo\n", 0)
	h.run("sed -z s/b/B/", "a\x00b\x00").expectOutput(t, "-z", "a\x00B\x00", 0)
	h.run("sed q3", "x\ny\n").expectOutput(t, "exit code", "x\n", 3)
	r := h.run("sed -n '/a/w kept\n/c/p' one two", "")
	r.expectOutput(t, "w file", "c\n", 0)
	if h.read("kept") != "a\n" {
		t.Fatalf("kept %q", h.read("kept"))
	}
	h.seed("ins", "INSERTED\n")
	h.run("sed '1r ins' one", "").expectOutput(t, "r file", "a\nINSERTED\nb\n", 0)
	h.seed("f", "a\n")
	h.run("sed -i.bak s/a/b/ f", "").expectOutput(t, "GNU backup suffix", "", 0)
	if h.read("f") != "b\n" || h.read("f.bak") != "a\n" {
		t.Fatalf("f %q f.bak %q", h.read("f"), h.read("f.bak"))
	}
	h.seed("dir/g", "x\n")
	h.run("sed -i'orig-*' 's/x/y/w /dev/stdout' dir/g", "").expectOutput(t, "star backup and stdout", "y\n", 0)
	if h.read("dir/g") != "y\n" || h.read("dir/orig-g") != "x\n" {
		t.Fatalf("g %q backup %q", h.read("dir/g"), h.read("dir/orig-g"))
	}
	h.seed("bsd", "a\n")
	h.run("sed -i .orig s/a/b/ bsd", "").expectOutput(t, "BSD separate suffix", "", 0)
	if h.read("bsd") != "b\n" || h.read("bsd.orig") != "a\n" {
		t.Fatalf("bsd %q bsd.orig %q", h.read("bsd"), h.read("bsd.orig"))
	}
	h.seed("bsdI", "a\n")
	h.run("sed -I .k -u s/a/c/ bsdI", "").expectOutput(t, "BSD -I", "", 0)
	if h.read("bsdI") != "c\n" || h.read("bsdI.k") != "a\n" {
		t.Fatalf("bsdI %q bsdI.k %q", h.read("bsdI"), h.read("bsdI.k"))
	}
	h.seed("gnu", "p\n")
	h.run("sed -i p gnu", "").expectOutput(t, "GNU script after -i", "", 0)
	if h.read("gnu") != "p\np\n" {
		t.Fatalf("gnu %q", h.read("gnu"))
	}
	modes := map[string]string{"sed p": "stream-script", "sed -i p f": "-i", "sed -I x p f": "-i", "sed 'w out'": "w-command", "sort -o x": "-o", "uniq a b": "OUTPUT-supplied", "uniq": "stdin-input-only", "tac": ""}
	for source, mode := range modes {
		words := strings.Fields(strings.ReplaceAll(source, "'", ""))
		if source == "sed 'w out'" {
			words = []string{"sed", "w out"}
		}
		argv := make(commandprogram.Fields, len(words))
		for i, word := range words {
			argv[i] = commandprogram.Field{Value: word}
		}
		request := commandprogramRequest("mode")
		request.OperationID, request.Argv = words[0], argv
		request.Invocation.Kind = toolcontract.ProgramChildInvocation
		request.Statement.Span = request.Invocation.Span
		prepared, err := h.owners.Prepare(t.Context(), request)
		if err != nil || prepared.Mode != mode {
			t.Fatalf("%s mode %q, %v; want %q", source, prepared.Mode, err, mode)
		}
	}
}

func TestSedReadsPinnedFilesAsOneStream(t *testing.T) {
	h := sedHarness(t)
	h.seed("one", "a\nb\n")
	h.seed("two", "c\n")
	r := h.run("sed -n '$p;2p' one two", "")
	r.expectOutput(t, "files", "b\nc\n", 0)
	if len(h.pins.executions[r.execution]) != 2 {
		t.Fatalf("pins %+v", h.pins.executions[r.execution])
	}
}

// Ambiguous captures follow the portable POSIX engine's subexpression
// ordering. Host sed implementations disagree on this case.
func TestSedAmbiguousCapturesUsePortablePOSIXOrdering(t *testing.T) {
	h := sedHarness(t)
	h.run(`sed -E 's/(a|al)(l|lpha)(.*)/[\1|\2|\3]/'`, "alpha\n").expectOutput(t, "longest second capture", "[a|lpha|]\n", 0)
	h.run(`sed -E 's/(a|ab)(c|bcd)(d*)/[\1|\2|\3]/'`, "abcd\n").expectOutput(t, "earlier capture wins", "[ab|c|d]\n", 0)
}

// GNU and BSD sed agree on these forms under LC_ALL=C.
func TestSedDifferentialAgainstHost(t *testing.T) {
	h := sedHarness(t)
	input := "alpha\nbeta\nstart\ngamma\nend\nalpha beta\n"
	for _, c := range []struct {
		source string
		argv   []string
	}{
		{"sed -n 2,4p", []string{"-n", "2,4p"}},
		{"sed '1d;$d'", []string{"1d;$d"}},
		{"sed s/a/A/g", []string{"s/a/A/g"}},
		{"sed s/a/A/2", []string{"s/a/A/2"}},
		{"sed -n '/^start$/,/^end$/p'", []string{"-n", "/^start$/,/^end$/p"}},
		{`sed 's/\(al\)\(pha\)/\2\1/'`, []string{`s/\(al\)\(pha\)/\2\1/`}},
		{"sed -E 's/(a|e)+/<&>/g'", []string{"-E", "s/(a|e)+/<&>/g"}},
		{"sed 's/x*/-/g'", []string{"s/x*/-/g"}},
		{"sed 3q", []string{"3q"}},
		{"sed '$!d'", []string{"$!d"}},
		{"sed -n '/beta/{p;p;}'", []string{"-n", "/beta/{p;p;}"}},
		{"sed -e s/a/b/ -e s/b/c/", []string{"-e", "s/a/b/", "-e", "s/b/c/"}},
		{`sed 's/\(a\)\(.*\)\1/<\2>/'`, []string{`s/\(a\)\(.*\)\1/<\2>/`}},
		{"sed '1!G;h;$!d'", []string{"1!G;h;$!d"}},
	} {
		cmd := exec.Command(hostTool(t, "sed"), c.argv...)
		cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
		cmd.Stdin = strings.NewReader(input)
		want, err := cmd.Output()
		if err != nil {
			t.Fatalf("host %s: %v", c.source, err)
		}
		h.run(c.source, input).expectOutput(t, c.source, string(want), 0)
	}
}

// GNU -i SCRIPT FILE and BSD -i ” SCRIPT FILE edit each file separately,
// publishing exact bytes at the pinned destination after a pinned source.
func TestSedInPlacePublishesEachFileAtItsPin(t *testing.T) {
	h := sedHarness(t)
	h.seed("f", "a\na")
	r := h.run("sed -i s/a/b/ f", "")
	r.expectOutput(t, "GNU form", "", 0)
	if got := h.read("f"); got != "b\nb" {
		t.Fatalf("f holds %q", got)
	}
	pins := h.pins.executions[r.execution]
	if len(pins) != 2 || pins[0].Role != toolcontract.DestinationRevision || pins[1].Role != toolcontract.SourceRevision || pins[0].Path != "f" || pins[1].Path != "f" {
		t.Fatalf("pins %+v", pins)
	}
	h.seed("g", "1\n2\n")
	h.seed("k", "3\n4\n")
	h.run("sed -i '' 1d g k", "").expectOutput(t, "BSD form restarts lines per file", "", 0)
	if g, k := h.read("g"), h.read("k"); g != "2\n" || k != "4\n" {
		t.Fatalf("g %q k %q", g, k)
	}
	h.seed("q", "a\nb\nc\n")
	h.seed("later", "x\n")
	h.run("sed -i 2q q later", "").expectOutput(t, "q publishes and stops", "", 0)
	if q, later := h.read("q"), h.read("later"); q != "a\nb\n" || later != "x\n" {
		t.Fatalf("q %q later %q", q, later)
	}
	h.seed("ok", "a\n")
	missing := h.run("sed -i s/a/b/ ok missing ok", "")
	missing.expectOutput(t, "missing stops after earlier files", "", 2)
	if h.read("ok") != "b\n" || missing.stderr != "sed: missing: No such file or directory\n" {
		t.Fatalf("ok %q stderr %q", h.read("ok"), missing.stderr)
	}
	if _, err := os.Stat(filepath.Join(h.root, "missing")); !os.IsNotExist(err) {
		t.Fatal("a missing file was created")
	}
	h.seed("p", "x\n")
	h.run("sed -i -n p p", "").expectOutput(t, "-n -i keeps printed lines only", "", 0)
	if h.read("p") != "x\n" {
		t.Fatalf("p %q", h.read("p"))
	}
}

// A destination that moved after its capture is a typed revision conflict
// from the redirect owner; the prepared bytes are not refreshed or retried.
func TestInPlacePublicationConflictIsTyped(t *testing.T) {
	h := sedHarness(t)
	h.seed("f", "a\n")
	r := h.run("sed -n p f", "")
	r.expectOutput(t, "setup", "a\n", 0)
	request := commandprogramRequest(r.execution + "-conflict")
	inv := &invocation{ctx: t.Context(), owners: h.owners, ports: h.ports(), spec: h.owners.specs["sed"], request: request}
	stage, err := inv.openDestination(commandprogram.Field{Value: "f"})
	testutil.FailErr(t, "open destination", err)
	h.seed("f", "moved\n")
	_, err = stage.Write([]byte("b\n"))
	testutil.FailErr(t, "stage", err)
	err = settleStage(t.Context(), stage, nil)
	if reject := toolrejection.AsToolReject(err); reject == nil || reject.Code != "COMMAND_REVISION_CONFLICT" {
		t.Fatalf("err = %v", err)
	}
	if h.read("f") != "moved\n" {
		t.Fatalf("conflicting publication changed f to %q", h.read("f"))
	}
	unwired := sedHarness(t)
	unwired.publication = nil
	unwired.seed("f", "a\n")
	unwired.run("sed -i p f", "").expectRefusal(t, "no publication owner", "COMMAND_PLATFORM_UNAVAILABLE")
}

func commandprogramRequest(execution string) commandprogram.OperationRequest {
	return commandprogram.OperationRequest{Invocation: toolcontract.OperationInvocation{Identity: toolcontract.ExecutionIdentity{ExecutionID: execution}, Span: toolcontract.SourceSpan{Line: 1, Column: 1}}}
}
