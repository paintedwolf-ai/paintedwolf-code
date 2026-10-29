//go:build darwin || linux

package hostenv

import (
	"os"
	"strconv"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func TestIdentityAndSystemUtilities(t *testing.T) {
	h := newHarness(t, defaultHost())
	cases := []struct{ source, stdout string }{
		{"uname", "FixtureOS\n"},
		{"uname -a", "FixtureOS fixture-host 1.2.3 #1 fixture fx64\n"},
		{"uname -m -s", "FixtureOS fx64\n"},
		{"uname -nr", "fixture-host 1.2.3\n"},
		{"uname -v", "#1 fixture\n"},
		{"hostname", "fixture-host\n"},
		{"nproc", "6\n"},
		{"nproc --all", "8\n"},
		{"nproc --ignore 2", "4\n"},
		{"nproc --ignore=9", "1\n"},
		{"whoami", "fixture-user\n"},
		{"id", "uid=501 gid=20 groups=20,12\n"},
		{"id -u", "501\n"},
		{"id -g", "20\n"},
		{"id -G", "20 12\n"},
		{"id -un", "fixture-user\n"},
		{"id -n -g", "staff\n"},
		{"id -Gn", "staff everyone\n"},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			h.run(c.source).expectOutput(t, c.source, c.stdout, "", 0)
		})
	}
	for source, code := range map[string]string{
		"id -n": "COMMAND_OPERAND_INVALID", "id -u -g": "COMMAND_OPERAND_INVALID", "uname -p": "COMMAND_OPTION_UNSUPPORTED",
		"whoami --help": "COMMAND_OPTION_UNSUPPORTED", "hostname -f": "COMMAND_OPTION_UNSUPPORTED",
	} {
		h.run(source).expectRefusal(t, source, code)
	}
}

func TestMissingHostFactsRefuseWithoutSubstitutes(t *testing.T) {
	unnamed := defaultHost()
	unnamed.identity.User = ""
	unnamed.identity.GroupNames = map[int]string{20: "staff"}
	h := newHarness(t, unnamed)
	for _, source := range []string{"whoami", "id -un", "id -Gn"} {
		reject := h.run(source).expectRefusal(t, source, "COMMAND_PLATFORM_UNAVAILABLE")
		if reject.Data["command"] == nil || reject.Data["capability"] == nil {
			t.Fatalf("%s refusal data %+v", source, reject.Data)
		}
	}
	h.run("id -gn").expectOutput(t, "id -gn", "staff\n", "", 0)
	missing := newHarness(t, fixtureHost{err: ErrHostFactUnavailable})
	for _, source := range []string{"uname", "hostname", "whoami", "id"} {
		missing.run(source).expectRefusal(t, source, "COMMAND_PLATFORM_UNAVAILABLE")
	}
}

func TestDateCapturesTheClockOnceInUTC(t *testing.T) {
	h := newHarness(t, defaultHost())
	cases := []struct{ source, stdout string }{
		{"date", "2026-01-01T22:04:05Z\n"},
		{"date -u", "2026-01-01T22:04:05Z\n"},
		{"date '+%Y/%m/%d %H:%M:%S %s %z %% literal'", "2026/01/01 22:04:05 1767305045 +0000 % literal\n"},
		{"date -u +", "\n"},
	}
	for _, c := range cases {
		reads := h.clock.reads
		h.run(c.source).expectOutput(t, c.source, c.stdout, "", 0)
		if h.clock.reads != reads+1 {
			t.Fatalf("%s read the clock %d times", c.source, h.clock.reads-reads)
		}
	}
	for _, source := range []string{"date +%a", "date +%", "date 0102", "date +%Y +%m", "date -d now"} {
		code := "COMMAND_OPERAND_INVALID"
		if source == "date -d now" {
			code = "COMMAND_OPTION_UNSUPPORTED"
		}
		h.run(source).expectRefusal(t, source, code)
	}
}

func TestPwdUsesThePathOwner(t *testing.T) {
	h := newHarness(t, defaultHost())
	for _, cwd := range []string{"", "pkg"} {
		h.cwd = cwd
		paths, err := projectpaths.NewProgramPaths(t.Context(), nil, h.tc, cwd)
		testutil.FailErr(t, "path frame", err)
		h.run("pwd").expectOutput(t, "pwd in "+cwd, paths.Cwd().DisplayPath+"\n", "", 0)
	}
	h.run("pwd extra").expectRefusal(t, "pwd with an operand", "COMMAND_OPERAND_INVALID")
	h.run("pwd -P").expectRefusal(t, "pwd -P", "COMMAND_OPTION_UNSUPPORTED")
}

func TestOwnersBindOnlyTheirOperations(t *testing.T) {
	h := newHarness(t, defaultHost())
	if got := h.owners.Commands(); len(got) != len(utilities) {
		t.Fatalf("commands %q", got)
	}
	request := commandprogram.OperationRequest{OperationID: "printenv", Argv: commandprogram.Fields{{Value: "env"}}}
	request.Invocation.Identity.NodeID = string(request.Statement.ID)
	if _, err := h.owners.Prepare(request); err == nil {
		t.Fatal("env prepared under the printenv operation")
	}
	request.Argv[0].Value = "cat"
	request.OperationID = "cat"
	if _, err := h.owners.Prepare(request); err == nil {
		t.Fatal("prepared a command these owners do not serve")
	}
	if _, err := NewOwners(nil, h.clock.now, nil); err == nil {
		t.Fatal("owners without host facts accepted")
	}
	var unprepared Prepared
	if result, err := unprepared.InvokeOwner(t.Context(), h.tc, nil, nil, commandprogram.Streams{}); err == nil || result.Status == 0 {
		t.Fatalf("unprepared invocation ran: %+v", result)
	}
}

// OSHost reads the actual platform; its answers agree with the process.
func TestOSHostReportsTheRunningProcess(t *testing.T) {
	system, err := OSHost{}.System(t.Context())
	testutil.FailErr(t, "system facts", err)
	hostname, err := os.Hostname()
	testutil.FailErr(t, "hostname", err)
	if system.Nodename != hostname || system.Sysname == "" || system.Machine == "" {
		t.Fatalf("system facts %+v, hostname %q", system, hostname)
	}
	identity, err := OSHost{}.Identity(t.Context())
	testutil.FailErr(t, "identity facts", err)
	if identity.UID != os.Getuid() || identity.GID != os.Getgid() || len(identity.Groups) == 0 {
		t.Fatalf("identity facts %+v for uid %s", identity, strconv.Itoa(os.Getuid()))
	}
}
