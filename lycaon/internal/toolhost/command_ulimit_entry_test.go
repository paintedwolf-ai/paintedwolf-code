//go:build unix

package toolhost

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const loweredDescriptorLimitEnv = "LYCAON_TEST_LOWERED_DESCRIPTOR_LIMIT"

// measureChildDescriptorLimit is the soft limit a child of this test receives.
func measureChildDescriptorLimit(t *testing.T) uint64 {
	t.Helper()
	out, err := exec.Command("/bin/sh", "-c", "ulimit -Sn").Output()
	testutil.FailErr(t, "measure the inherited limit", err)
	report := strings.TrimSpace(string(out))
	if report == "unlimited" {
		var host syscall.Rlimit
		testutil.FailErr(t, "read the host limit", syscall.Getrlimit(syscall.RLIMIT_NOFILE, &host))
		return host.Cur
	}
	value, err := strconv.ParseUint(report, 10, 64)
	testutil.FailErr(t, "parse the inherited limit", err)
	return value
}

// The test binary runs again under a lowered soft limit, because the Go runtime
// raises its own limit only when the soft limit is below the hard one.
func TestCommandEntryUlimitMatchesChildWithoutUlimit(t *testing.T) {
	lowered, err := strconv.ParseUint(os.Getenv(loweredDescriptorLimitEnv), 10, 64)
	if err != nil {
		self, err := os.Executable()
		testutil.FailErr(t, "test binary", err)
		target := measureChildDescriptorLimit(t) / 2
		command := exec.Command("/bin/sh", "-c", `ulimit -Sn "$1" && exec "$0" -test.run "$2"`, self, strconv.FormatUint(target, 10), "^"+t.Name()+"$")
		command.Env = append(os.Environ(), loweredDescriptorLimitEnv+"="+strconv.FormatUint(target, 10))
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("under a soft limit of %d: %v\n%s", target, err, out)
		}
		return
	}
	var own syscall.Rlimit
	testutil.FailErr(t, "read the runtime's limit", syscall.Getrlimit(syscall.RLIMIT_NOFILE, &own))
	if own.Cur == lowered {
		t.Fatalf("the runtime kept the soft limit at %d, so this run cannot tell a report from a child's value", lowered)
	}

	f := newPathsFixture(t)
	const child = `external -- /bin/sh -c 'ulimit -n'`
	stdout := func(program string) string {
		t.Helper()
		raw, err := f.Call(t, t.Context(), program)
		testutil.FailErr(t, program, err)
		got := decodeProgramOutcome(t, raw)
		if got.Status != 0 || got.Refusal != nil {
			t.Fatalf("%s: %+v", program, got)
		}
		return got.Stdout
	}

	baseline := stdout(child)
	if want := fmt.Sprintf("%d\n", lowered); baseline != want {
		t.Fatalf("a child with no ulimit received %q, want the lowered %q", baseline, want)
	}
	for _, read := range []string{"ulimit -n", "ulimit -Sn", "ulimit -Hn", "ulimit -a", "ulimit -n -f", "( ulimit -n )"} {
		if got := stdout(read + " >/dev/null; " + child); got != baseline {
			t.Errorf("after %s a child received %q, want %q", read, got, baseline)
		}
	}
	// These options share their units across bash and the /bin/sh of each platform.
	for _, option := range []string{"-n", "-Hn", "-s", "-Hs", "-t", "-v"} {
		report := fmt.Sprintf("ulimit %s; external -- /bin/sh -c 'ulimit %s'", option, option)
		lines := strings.Split(strings.TrimSuffix(stdout(report), "\n"), "\n")
		if len(lines) != 2 || lines[0] != lines[1] {
			t.Errorf("ulimit %s and a child's own differ: %q", option, lines)
		}
	}

	half := lowered / 2
	want := fmt.Sprintf("%d\n%d\n", half, half)
	if got := stdout(fmt.Sprintf("ulimit -n %d; ulimit -n; %s", half, child)); got != want {
		t.Errorf("a set limit reported and reached a child as %q, want %q", got, want)
	}
	if got := stdout(child); got != baseline {
		t.Errorf("the set limit reached the next call: %q, want %q", got, baseline)
	}
	// A hard limit at the child's soft limit is valid only against the real soft limit.
	want = baseline + baseline
	if got := stdout(fmt.Sprintf("ulimit -H -n %d; external -- /bin/sh -c 'ulimit -Sn; ulimit -Hn'", lowered)); got != want {
		t.Errorf("a hard limit at the child's soft limit gave %q, want %q", got, want)
	}
	// "soft" keeps the child's soft limit rather than the runtime's.
	if got := stdout("ulimit -n soft; " + child); got != baseline {
		t.Errorf("ulimit -n soft gave a child %q, want %q", got, baseline)
	}
}
