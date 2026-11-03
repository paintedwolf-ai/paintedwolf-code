package search

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// stampFiles sets each file's access and modification times.
func stampFiles(t *testing.T, h *harness, times map[string][2]time.Time) {
	t.Helper()
	for name, at := range times {
		testutil.FailErr(t, "stamp "+name, os.Chtimes(filepath.Join(h.root, name), at[0], at[1]))
	}
}

func TestFindNewerComparesEntryTimesWithDatesAndReferences(t *testing.T) {
	h := newHarness(t, map[string]string{"old": "", "edge": "", "new": "", "late": "", "ref": ""})
	day := func(d, hour int) time.Time { return time.Date(2026, 10, d, hour, 0, 0, 0, time.UTC) }
	stampFiles(t, h, map[string][2]time.Time{
		"old": {day(1, 0), day(1, 0).Add(-12 * time.Hour)}, "edge": {day(1, 0), day(1, 0)}, "new": {day(1, 0), day(2, 0)},
		"late": {day(1, 0), day(2, 1)}, "ref": {day(2, 0), day(1, 12)},
	})
	h.exec("", "find", ".", "-type", "f", "-newermt", "2026-10-01").expect(t, "-newermt", "./late\n./new\n./ref\n", "", 0)
	h.exec("", "find", ".", "-type", "f", "-newermt", "2026-10-01 +1 day").expect(t, "-newermt relative", "./late\n", "", 0)
	h.exec("", "find", ".", "-type", "f", "-newer", "ref").expect(t, "-newer", "./late\n./new\n", "", 0)
	h.exec("", "find", ".", "-type", "f", "-newermm", "ref").expect(t, "-newermm", "./late\n./new\n", "", 0)
	h.exec("", "find", ".", "-type", "f", "-newerma", "ref").expect(t, "-newerma", "./late\n", "", 0)
	h.exec("", "find", ".", "-type", "f", "-neweram", "edge").expect(t, "-neweram", "./ref\n", "", 0)
	h.exec("", "find", ".", "-type", "f", "-newerat", "2026-10-01 12:00").expect(t, "-newerat", "./ref\n", "", 0)
	h.exec("", "find", ".", "-type", "f", "-name", "old", "-newerct", "2000-01-01").expect(t, "-newerct", "./old\n", "", 0)
	h.env = []string{"TZ=America/New_York"}
	h.exec("", "find", ".", "-type", "f", "-newermt", "2026-10-01 20:30").expect(t, "-newermt in the program TZ", "./late\n", "", 0)
	h.exec("", "find", ".", "-newermt", "next blue moon").expect(t, "invalid date", "", "find: I cannot figure out how to interpret 'next blue moon' as a date or time\n", 1)
	h.exec("", "find", ".", "-newercm", "missing").expect(t, "missing reference", "", "find: 'missing': No such file or directory\n", 1)
}
