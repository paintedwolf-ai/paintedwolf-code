package commandprogram

import (
	"bytes"
	"context"
	"testing"
)

func timeFixture(t *testing.T) (*evalFixture, *bytes.Buffer) {
	t.Helper()
	f := newEvalFixture(t)
	stderr := &bytes.Buffer{}
	f.ports.Streams.Error = stderr
	clock := newManualClock()
	f.ports.Clock = clock
	f.log.clock = clock
	return f, stderr
}

func TestTimeReportsExternalProcessTimeOnStderr(t *testing.T) {
	for _, tc := range []struct{ program, stdout, stderr string }{
		// burn advances the clock 1.5s and reports 2.5s user and 0.25s sys.
		{"time -p burn", "", "real 1.50\nuser 2.50\nsys 0.25\n"},
		{"time burn", "", "\nreal\t0m1.500s\nuser\t0m2.500s\nsys\t0m0.250s\n"},
		{"time -p burn 2>/dev/null", "", "real 1.50\nuser 2.50\nsys 0.25\n"},
		{"time -p { burn; time -p burn; }", "", "real 1.50\nuser 2.50\nsys 0.25\nreal 3.00\nuser 5.00\nsys 0.50\n"},
		{"time -p", "", "real 0.00\nuser 0.00\nsys 0.00\n"},
	} {
		t.Run(tc.program, func(t *testing.T) {
			f, stderr := timeFixture(t)
			out, err := f.run(t, context.Background(), tc.program)
			if err != nil || out.Status != 0 || f.stdout.String() != tc.stdout || stderr.String() != tc.stderr || out.CPUTimeUnmeasured {
				t.Fatalf("status=%d err=%v unmeasured=%v stdout=%q stderr=%q; want stdout %q stderr %q", out.Status, err, out.CPUTimeUnmeasured, f.stdout.String(), stderr.String(), tc.stdout, tc.stderr)
			}
		})
	}
}

// A native operation has no processor time of its own: it counts zero and
// the outcome records that the report did not measure it.
func TestTimeMarksNativeOperationsUnmeasured(t *testing.T) {
	for _, tc := range []struct{ program, stdout, stderr string }{
		{"time -p echo hi", "hi\n", "real 0.00\nuser 0.00\nsys 0.00\n"},
		{"time -p burn | cat; echo after", "after\n", "real 1.50\nuser 2.50\nsys 0.25\n"},
	} {
		t.Run(tc.program, func(t *testing.T) {
			f, stderr := timeFixture(t)
			out, err := f.run(t, context.Background(), tc.program)
			if err != nil || f.stdout.String() != tc.stdout || stderr.String() != tc.stderr || !out.CPUTimeUnmeasured {
				t.Fatalf("err=%v unmeasured=%v stdout=%q stderr=%q", err, out.CPUTimeUnmeasured, f.stdout.String(), stderr.String())
			}
		})
	}
}

func TestTimeKeepsThePipelineStatus(t *testing.T) {
	for _, tc := range []struct {
		program string
		status  int
	}{{"time -p false", 1}, {"time -p burn; time -p false", 1}, {"! time -p false", 0}, {"time -p fail 3", 3}} {
		t.Run(tc.program, func(t *testing.T) {
			f, _ := timeFixture(t)
			out, err := f.run(t, context.Background(), tc.program)
			if err != nil || out.Status != tc.status {
				t.Fatalf("status=%d err=%v; want %d", out.Status, err, tc.status)
			}
		})
	}
}
