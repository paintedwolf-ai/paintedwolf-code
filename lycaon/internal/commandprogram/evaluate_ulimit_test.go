package commandprogram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"

	"github.com/lycaon/lycaon/internal/confine"
)

// ulimitFixture serves fixed host limits: 256 soft and 1024 hard open files,
// a 1 MiB file size and unlimited virtual memory.
func ulimitFixture(t *testing.T) (*evalFixture, *bytes.Buffer) {
	t.Helper()
	f := newEvalFixture(t)
	stderr := &bytes.Buffer{}
	f.ports.Streams.Error = stderr
	host := map[string]confine.ResourceLimit{
		"nofile": {Resource: "nofile", Soft: 256, Hard: 1024},
		"fsize":  {Resource: "fsize", Soft: 1 << 20, Hard: math.MaxUint64},
		"as":     {Resource: "as", Soft: math.MaxUint64, Hard: math.MaxUint64},
	}
	f.ports.Refusals = func(w io.Writer, report RefusalReport) {
		fmt.Fprintf(w, "%s: %s: [%s]\n", report.Operation, report.Operand, report.Code)
	}
	f.ports.ResourceLimits = func(resource string) (confine.ResourceLimit, error) {
		if limit, ok := host[resource]; ok {
			return limit, nil
		}
		return confine.ResourceLimit{Resource: resource, Soft: 8, Hard: 16}, nil
	}
	return f, stderr
}

func burnLimits(t *testing.T, log *dispatchLog) [][]confine.ResourceLimit {
	t.Helper()
	log.mu.Lock()
	defer log.mu.Unlock()
	var out [][]confine.ResourceLimit
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

func TestUlimitHardChangesAreRefused(t *testing.T) {
	f, stderr := ulimitFixture(t)
	out, err := f.run(t, context.Background(), "ulimit -H -n 512; burn")
	var refusal *EvaluationRefusal
	if !errors.As(err, &refusal) || refusal.Code != "COMMAND_OPTION_UNSUPPORTED" || refusal.Option != "-H" || out.Stop == nil {
		t.Fatalf("err=%v stop=%+v; want a -H refusal", err, out.Stop)
	}
	if len(burnLimits(t, f.log)) != 0 || !bytes.Contains(stderr.Bytes(), []byte("[COMMAND_OPTION_UNSUPPORTED]")) {
		t.Fatalf("burn ran after the refusal or stderr lacks the shell refusal: %q", stderr.String())
	}
}
