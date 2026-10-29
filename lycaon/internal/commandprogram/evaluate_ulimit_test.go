package commandprogram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
)

// fixtureLimits is a resource-limit owner's declared capability set over
// fixed host limits; a resource without a declared capability is unsupported.
type fixtureLimits struct {
	capabilities map[string]LimitCapability
	host         map[string]ResourceLimit
	reads        []string
}

func (l *fixtureLimits) Capability(resource string) LimitCapability { return l.capabilities[resource] }
func (l *fixtureLimits) Read(resource string) (ResourceLimit, error) {
	l.reads = append(l.reads, resource)
	if l.capabilities[resource]&LimitQuery == 0 {
		return ResourceLimit{}, fmt.Errorf("read of undeclared %s", resource)
	}
	if limit, ok := l.host[resource]; ok {
		return limit, nil
	}
	return ResourceLimit{Resource: resource, Soft: 8, Hard: 16}, nil
}

const fullLimit = LimitQuery | LimitSetSoft | LimitSetHard

// supportedLimits declares every resource fully: 256 soft and 1024 hard open
// files, a 1 MiB file size and unlimited virtual memory.
func supportedLimits() *fixtureLimits {
	capabilities := map[string]LimitCapability{}
	for _, resource := range ulimitResources {
		capabilities[resource.resource] = fullLimit
	}
	return &fixtureLimits{capabilities: capabilities, host: map[string]ResourceLimit{
		"nofile": {Resource: "nofile", Soft: 256, Hard: 1024},
		"fsize":  {Resource: "fsize", Soft: 1 << 20, Hard: math.MaxUint64},
		"as":     {Resource: "as", Soft: math.MaxUint64, Hard: math.MaxUint64},
	}}
}

func ulimitFixture(t *testing.T) (*evalFixture, *bytes.Buffer) {
	t.Helper()
	return ulimitFixtureWith(t, supportedLimits())
}

func ulimitFixtureWith(t *testing.T, limits ResourceLimits) (*evalFixture, *bytes.Buffer) {
	t.Helper()
	f := newEvalFixture(t)
	stderr := &bytes.Buffer{}
	f.ports.Streams.Error = stderr
	f.ports.Refusals = func(w io.Writer, report RefusalReport) {
		fmt.Fprintf(w, "%s: %s: [%s]\n", report.Operation, report.Operand, report.Code)
	}
	f.ports.ResourceLimits = limits
	return f, stderr
}

func burnLimits(t *testing.T, log *dispatchLog) [][]ResourceLimit {
	t.Helper()
	log.mu.Lock()
	defer log.mu.Unlock()
	var out [][]ResourceLimit
	for _, call := range log.calls {
		if call.external && call.argv[0] == "burn" {
			out = append(out, call.process.Limits)
		}
	}
	return out
}

func TestUlimitReportsTheLimitsChildrenReceive(t *testing.T) {
	for _, tc := range []struct{ program, stdout string }{
		{"ulimit -n", "256\n"},
		{"ulimit -Hn", "1024\n"},
		{"ulimit -S -n", "256\n"},
		{"ulimit", "1024\n"},
		{"ulimit -v", "unlimited\n"},
		{"ulimit -n -f", "open files                          (-n) 256\nfile size                   (blocks, -f) 1024\n"},
		{"ulimit -n -f 4", "open files                          (-n) 256\n"},
		{"ulimit -a 5", ulimitAll},
	} {
		t.Run(tc.program, func(t *testing.T) {
			f, stderr := ulimitFixture(t)
			out, err := f.run(t, context.Background(), tc.program)
			if err != nil || out.Status != 0 || f.stdout.String() != tc.stdout || stderr.Len() != 0 {
				t.Fatalf("status=%d err=%v stdout=%q stderr=%q; want %q", out.Status, err, f.stdout.String(), stderr.String(), tc.stdout)
			}
		})
	}
}

// ulimitAll is the fixture's -a report, in bash's layout and order.
const ulimitAll = "core file size              (blocks, -c) 0\n" +
	"file size                   (blocks, -f) 1024\n" +
	"open files                          (-n) 256\n" +
	"stack size                  (kbytes, -s) 0\n" +
	"cpu time                   (seconds, -t) 8\n" +
	"max user processes                  (-u) 8\n" +
	"virtual memory              (kbytes, -v) unlimited\n"

// A set soft limit reaches every later external child of the program and
// none started before it; a ( ) frame's setting stays in that frame.
func TestUlimitSettingReachesLaterExternalChildrenOnly(t *testing.T) {
	f, _ := ulimitFixture(t)
	out, err := f.run(t, context.Background(), "burn; (ulimit -n 64; burn); burn; ulimit -n 128; burn; ulimit -n")
	if err != nil || out.Status != 0 || f.stdout.String() != "128\n" {
		t.Fatalf("status=%d err=%v stdout=%q", out.Status, err, f.stdout.String())
	}
	got := burnLimits(t, f.log)
	want := []string{"[]", "[{nofile 64 1024}]", "[]", "[{nofile 128 1024}]"}
	if len(got) != len(want) {
		t.Fatalf("burn launches = %v, want %d", got, len(want))
	}
	for i := range want {
		if fmt.Sprint(got[i]) != want[i] {
			t.Errorf("launch %d limits = %v, want %s", i, got[i], want[i])
		}
	}
}

// Reading pins the reported value too, so a later child sees what was shown.
func TestUlimitReadPinsTheReportedLimit(t *testing.T) {
	f, _ := ulimitFixture(t)
	if _, err := f.run(t, context.Background(), "ulimit -n >/dev/null; burn"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := burnLimits(t, f.log); len(got) != 1 || fmt.Sprint(got[0]) != "[{nofile 256 1024}]" {
		t.Fatalf("burn limits = %v, want the reported open files limit", got)
	}
}

func TestUlimitUtilityErrorsKeepBashStatus(t *testing.T) {
	for _, tc := range []struct {
		program, stderr string
		status          int
	}{
		{"ulimit -n 2048", "bash: ulimit: open files: cannot modify limit: Operation not permitted\n", 1},
		{"ulimit -n many", "bash: ulimit: many: invalid number\n", 1},
	} {
		t.Run(tc.program, func(t *testing.T) {
			f, stderr := ulimitFixture(t)
			out, err := f.run(t, context.Background(), tc.program+"; burn")
			if err != nil || stderr.String() != tc.stderr {
				t.Fatalf("err=%v stderr=%q; want %q", err, stderr.String(), tc.stderr)
			}
			if got := burnLimits(t, f.log); len(got) != 1 || fmt.Sprint(got[0]) == "[{nofile 2048 1024}]" {
				t.Fatalf("burn limits = %v; a refused value must not apply", got)
			}
			if out.Children[len(out.Children)-2].Result.Status != tc.status {
				t.Fatalf("ulimit status = %d, want %d", out.Children[len(out.Children)-2].Result.Status, tc.status)
			}
		})
	}
}

func TestUlimitRaisingUpToTheHardLimitAndKeywords(t *testing.T) {
	f, _ := ulimitFixture(t)
	out, err := f.run(t, context.Background(), "ulimit -n 1024; ulimit -n; ulimit -n soft; ulimit -f unlimited; ulimit -f; ulimit -n 300; ulimit -n hard; ulimit -n; ulimit -n -f 4 7; ulimit -f")
	if err != nil || out.Status != 0 || f.stdout.String() != "1024\nunlimited\n1024\nopen files                          (-n) 1024\n4\n" {
		t.Fatalf("status=%d err=%v stdout=%q", out.Status, err, f.stdout.String())
	}
}

// A declared hard capability lowers the hard limit for later children;
// raising it, or lowering it under the soft limit, keeps bash's errors.
func TestUlimitSetsTheHardLimitWhereDeclared(t *testing.T) {
	f, stderr := ulimitFixture(t)
	out, err := f.run(t, context.Background(), "ulimit -H -n 512; burn; ulimit -Hn; ulimit -n; ulimit -H -S -n 300; burn; ulimit -H -n 4096; ulimit -H -n 100; burn")
	if err != nil || out.Status != 0 || f.stdout.String() != "512\n256\n" {
		t.Fatalf("status=%d err=%v stdout=%q", out.Status, err, f.stdout.String())
	}
	want := "bash: ulimit: open files: cannot modify limit: Operation not permitted\nbash: ulimit: open files: cannot modify limit: Invalid argument\n"
	if stderr.String() != want {
		t.Fatalf("stderr=%q, want %q", stderr.String(), want)
	}
	got := burnLimits(t, f.log)
	if len(got) != 3 || fmt.Sprint(got[0]) != "[{nofile 256 512}]" || fmt.Sprint(got[1]) != "[{nofile 300 300}]" || fmt.Sprint(got[2]) != "[{nofile 300 300}]" {
		t.Fatalf("burn limits = %v", got)
	}
}

// Each capability the owner lacks refuses with a structured rejection before
// any later child runs; a query-only resource still reports.
func TestUlimitRefusesWhatTheOwnerDoesNotDeclare(t *testing.T) {
	softOnly := supportedLimits()
	softOnly.capabilities["nofile"] = LimitQuery | LimitSetSoft
	queryOnly := supportedLimits()
	queryOnly.capabilities["nofile"] = LimitQuery
	hardOnly := supportedLimits()
	hardOnly.capabilities["nofile"] = LimitQuery | LimitSetHard
	unsupported := supportedLimits()
	delete(unsupported.capabilities, "nofile")
	noFileSize := supportedLimits()
	delete(noFileSize.capabilities, "fsize")
	for _, tc := range []struct {
		name, program, code, option string
		limits                      *fixtureLimits
	}{
		{"hard on a soft-only resource", "ulimit -H -n 512", "COMMAND_OPTION_UNSUPPORTED", "-H", softOnly},
		{"set on a query-only resource", "ulimit -n 128", "COMMAND_PLATFORM_UNAVAILABLE", "128", queryOnly},
		{"hard on a query-only resource", "ulimit -H -n 128", "COMMAND_PLATFORM_UNAVAILABLE", "128", queryOnly},
		{"soft on a hard-only resource", "ulimit -n 128", "COMMAND_PLATFORM_UNAVAILABLE", "128", hardOnly},
		{"query of an unsupported resource", "ulimit -n", "COMMAND_PLATFORM_UNAVAILABLE", "-n", unsupported},
		{"set of an unsupported resource", "ulimit -f -n 64", "COMMAND_PLATFORM_UNAVAILABLE", "-n", unsupported},
		{"implied unsupported file size", "ulimit", "COMMAND_PLATFORM_UNAVAILABLE", "-f", noFileSize},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, stderr := ulimitFixtureWith(t, tc.limits)
			out, err := f.run(t, context.Background(), tc.program+"; burn")
			var refusal *EvaluationRefusal
			if !errors.As(err, &refusal) || refusal.Code != tc.code || refusal.Option != tc.option || out.Stop == nil {
				t.Fatalf("err=%v stop=%+v; want %s at %s", err, out.Stop, tc.code, tc.option)
			}
			if len(burnLimits(t, f.log)) != 0 || !bytes.Contains(stderr.Bytes(), []byte("["+tc.code+"]")) || len(tc.limits.reads) != 0 {
				t.Fatalf("burn ran, stderr lacks the shell refusal or a limit was read: %q reads=%v", stderr.String(), tc.limits.reads)
			}
		})
	}
	f, stderr := ulimitFixtureWith(t, queryOnly)
	if out, err := f.run(t, context.Background(), "ulimit -n; ulimit -Hn"); err != nil || out.Status != 0 || f.stdout.String() != "256\n1024\n" || stderr.Len() != 0 {
		t.Fatalf("query-only report: err=%v stdout=%q stderr=%q", err, f.stdout.String(), stderr.String())
	}
}

// -a reports only what the owner declares; an owner that declares nothing
// reports nothing, and no owner at all declares nothing.
func TestUlimitAllListsDeclaredResources(t *testing.T) {
	partial := supportedLimits()
	delete(partial.capabilities, "core")
	delete(partial.capabilities, "as")
	f, _ := ulimitFixtureWith(t, partial)
	want := "file size                   (blocks, -f) 1024\n" +
		"open files                          (-n) 256\n" +
		"stack size                  (kbytes, -s) 0\n" +
		"cpu time                   (seconds, -t) 8\n" +
		"max user processes                  (-u) 8\n"
	if out, err := f.run(t, context.Background(), "ulimit -a"); err != nil || out.Status != 0 || f.stdout.String() != want {
		t.Fatalf("partial -a: err=%v stdout=%q", err, f.stdout.String())
	}
	for _, limits := range []ResourceLimits{&fixtureLimits{}, nil} {
		f, stderr := ulimitFixtureWith(t, limits)
		if out, err := f.run(t, context.Background(), "ulimit -a"); err != nil || out.Status != 0 || f.stdout.Len() != 0 || stderr.Len() != 0 {
			t.Fatalf("empty -a: err=%v stdout=%q stderr=%q", err, f.stdout.String(), stderr.String())
		}
	}
	f, _ = ulimitFixtureWith(t, nil)
	var refusal *EvaluationRefusal
	if _, err := f.run(t, context.Background(), "ulimit -n"); !errors.As(err, &refusal) || refusal.Code != "COMMAND_PLATFORM_UNAVAILABLE" {
		t.Fatalf("no owner: %v", err)
	}
	if _, err := (noResourceLimits{}).Read("nofile"); err == nil {
		t.Fatal("no owner read a limit")
	}
}
