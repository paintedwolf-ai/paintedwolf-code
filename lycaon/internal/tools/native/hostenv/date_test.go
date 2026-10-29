//go:build darwin || linux

package hostenv

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The harness clock is Thursday 2026-01-01 22:04:05 UTC.

func TestDateReadsTheClockOnceAndPrintsEachOutputForm(t *testing.T) {
	h := newHarness(t, defaultHost())
	bsd := time.Date(2026, 10, 26, 22, 4, 5, 0, time.UTC).Unix()
	cases := []struct{ source, stdout string }{
		{"date", "Thu Jan  1 22:04:05 UTC 2026\n"},
		{"date -u", "Thu Jan  1 22:04:05 UTC 2026\n"},
		{"date --utc +%T", "22:04:05\n"},
		{"date '+%Y/%m/%d %H:%M:%S %s %z %% literal'", "2026/01/01 22:04:05 1767305045 +0000 % literal\n"},
		{"date -u +", "\n"},
		{"date +%a,%A,%j,%-d,%_m,%V", "Thu,Thursday,001,1, 1,01\n"},
		{"date -I", "2026-01-01\n"},
		{"date -Iseconds", "2026-01-01T22:04:05+00:00\n"},
		{"date -Ih", "2026-01-01T22+00:00\n"},
		{"date --iso-8601=minutes", "2026-01-01T22:04+00:00\n"},
		{"date --iso-8601=ns", "2026-01-01T22:04:05,000000000+00:00\n"},
		{"date --iso-8601", "2026-01-01\n"},
		{"date -R", "Thu, 01 Jan 2026 22:04:05 +0000\n"},
		{"date --rfc-email", "Thu, 01 Jan 2026 22:04:05 +0000\n"},
		{"date --rfc-3339=seconds", "2026-01-01 22:04:05+00:00\n"},
		{"date -u -d '2026-10-26 +1 day' +%F", "2026-10-27\n"},
		{"date -d yesterday +%F", "2025-12-31\n"},
		{"date --date='2 days ago' +%F", "2025-12-30\n"},
		{"date -d 'next monday' +%F", "2026-01-05\n"},
		{"date -d @0 -R", "Thu, 01 Jan 1970 00:00:00 +0000\n"},
		{"date -j -f %Y-%m-%d 2026-10-26 +%s", strconv.FormatInt(bsd, 10) + "\n"},
		{"date -f '%Y-%m-%d %H:%M' '2026-10-26 09:30' +%T", "09:30:05\n"},
		{"date -v-1d +%F", "2025-12-31\n"},
		{"date -v+1m -v-3d -j -f %F 2026-01-31 +%F", "2026-02-25\n"},
		{"date -v+mon +%F", "2026-01-05\n"},
		{"date -d 2026-03-01 -v-1d +%F", "2026-02-28\n"},
		{"date -r 86400 -u +%F", "1970-01-02\n"},
		{"date -r -1 +%s", "-1\n"},
	}
	for _, c := range cases {
		reads := h.clock.reads
		h.run(c.source).expectOutput(t, c.source, c.stdout, "", 0)
		if h.clock.reads != reads+1 {
			t.Fatalf("%s read the clock %d times", c.source, h.clock.reads-reads)
		}
	}
}

func TestDateUsesTheProgramTimeZone(t *testing.T) {
	h := newHarness(t, defaultHost())
	h.env = []string{"TZ=America/New_York"}
	h.run("date").expectOutput(t, "date", "Thu Jan  1 17:04:05 EST 2026\n", "", 0)
	h.run("date -u").expectOutput(t, "date -u", "Thu Jan  1 22:04:05 UTC 2026\n", "", 0)
	h.run("date -d '2026-07-04 12:00' -Iminutes").expectOutput(t, "zoneless -d", "2026-07-04T12:00-04:00\n", "", 0)
	h.run("date -u -d '2026-07-04 12:00' +%H").expectOutput(t, "-u -d", "12\n", "", 0)
	h.env = []string{"TZ=EST5"}
	h.run("date +%H%Z").expectOutput(t, "posix TZ", "17EST\n", "", 0)
}

func TestDateReadsAReferenceFileThroughReadAuthority(t *testing.T) {
	h := newHarness(t, defaultHost())
	stamp := time.Date(2026, 10, 20, 8, 30, 0, 0, time.UTC)
	path := filepath.Join(h.root, "pkg", "built")
	testutil.FailErr(t, "write reference", os.WriteFile(path, []byte("x"), 0o644))
	testutil.FailErr(t, "stamp reference", os.Chtimes(path, stamp, stamp))
	h.cwd = "pkg"
	h.run("date -r built '+%F %T'").expectOutput(t, "date -r FILE", "2026-10-20 08:30:00\n", "", 0)
	h.run("date --reference=built +%s").expectOutput(t, "date --reference", strconv.FormatInt(stamp.Unix(), 10)+"\n", "", 0)
	h.run("date -r missing").expectOutput(t, "date -r missing", "", "date: missing: No such file or directory\n", 1)
	h.cwd = ""
	h.run("date -r /etc/hosts").expectRefusal(t, "date -r outside the roots", "SURVEY_PATH_ESCAPE")
}

func TestDateReportsOrdinaryFailuresAsTheUtilities(t *testing.T) {
	h := newHarness(t, defaultHost())
	for _, c := range []struct{ source, stderr string }{
		{"date -d foo", "date: invalid date 'foo'\n"},
		{"date -d '2026-02-30'", "date: invalid date '2026-02-30'\n"},
		{"date -j -f %Y-%m-%d nope +%s", "Failed conversion of ``nope'' using format ``%Y-%m-%d''\ndate: illegal time format\n"},
		{"date -v+1x", "+1x: Cannot apply date adjustment\n"},
	} {
		h.run(c.source).expectOutput(t, c.source, "", c.stderr, 1)
	}
	h.run("date -j -f %Y-%m-%d 2026-10-26x +%F").expectOutput(t, "extraneous", "2026-10-26\n", "Warning: Ignoring 1 extraneous characters in date string (x)\n", 0)
}

func TestDateRefusesConflictingForms(t *testing.T) {
	h := newHarness(t, defaultHost())
	for _, source := range []string{"date +%Y +%m", "date 0102", "date -d now -r 0", "date -d now -f %Y 2026", "date -I -R", "date -R +%F", "date -Ix", "date --rfc-3339=minutes", "date -f %Y", "date -f %Y 2026 extra"} {
		h.run(source).expectRefusal(t, source, "COMMAND_OPERAND_INVALID")
	}
	h.run("date --set=now").expectRefusal(t, "date --set", "COMMAND_OPTION_UNSUPPORTED")
}
