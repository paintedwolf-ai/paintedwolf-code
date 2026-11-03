package literal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
)

// dfRow lays out one row with df's column rules for these fixtures:
// Filesystem left in 14, the numeric columns right-aligned, Mounted on last.
func dfRow(widths [4]int, cells ...string) string {
	return fmt.Sprintf("%-14s %*s %*s %*s %*s %s\n", cells[0], widths[0], cells[1], widths[1], cells[2], widths[2], cells[3], widths[3], cells[4], cells[5])
}

// scratchVolume is a second volume with no inode accounting, for the
// default listing and the unknown ratio.
func scratchVolume(scratch string) volumeStats {
	return func(file *os.File, path string) (fseffect.Volume, error) {
		if strings.HasPrefix(path, scratch) {
			return fseffect.Volume{Source: "tmpfs", Target: "/scratch", BlockSize: 512, Blocks: 2048, Free: 2048, Available: 2048}, nil
		}
		return fixtureVolume(file, path)
	}
}

func TestDfLayoutProducesExactBytes(t *testing.T) {
	h := newHarness(t, map[string]string{"input": "alpha\n", "dir/": ""})
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve scratch", err)
	h.tc.Host.SessionScratchDir = scratch
	h.owners.volumes = scratchVolume(scratch)
	k := [4]int{9, 5, 9, 4}
	for _, c := range []formCase{
		{argv: "df -k .", stdout: dfRow(k, "Filesystem", "1K-blocks", "Used", "Available", "Use%", "Mounted on") + dfRow(k, "/dev/fixture", "4000", "2400", "1200", "67%", "/fixture")},
		{argv: "df input", stdout: dfRow(k, "Filesystem", "1K-blocks", "Used", "Available", "Use%", "Mounted on") + dfRow(k, "/dev/fixture", "4000", "2400", "1200", "67%", "/fixture")},
		{argv: "df dir input", stdout: dfRow(k, "Filesystem", "1K-blocks", "Used", "Available", "Use%", "Mounted on") + strings.Repeat(dfRow(k, "/dev/fixture", "4000", "2400", "1200", "67%", "/fixture"), 2)},
		{argv: "df", stdout: dfRow(k, "Filesystem", "1K-blocks", "Used", "Available", "Use%", "Mounted on") + dfRow(k, "/dev/fixture", "4000", "2400", "1200", "67%", "/fixture") + dfRow(k, "tmpfs", "1024", "0", "1024", "0%", "/scratch")},
		{argv: "df -h .", stdout: dfRow([4]int{5, 5, 5, 4}, "Filesystem", "Size", "Used", "Avail", "Use%", "Mounted on") + dfRow([4]int{5, 5, 5, 4}, "/dev/fixture", "4.0M", "2.4M", "1.2M", "67%", "/fixture")},
		{argv: "df --human-readable -k .", stdout: dfRow(k, "Filesystem", "1K-blocks", "Used", "Available", "Use%", "Mounted on") + dfRow(k, "/dev/fixture", "4000", "2400", "1200", "67%", "/fixture")},
		{argv: "df -P .", stdout: dfRow([4]int{11, 5, 9, 8}, "Filesystem", "1024-blocks", "Used", "Available", "Capacity", "Mounted on") + dfRow([4]int{11, 5, 9, 8}, "/dev/fixture", "4000", "2400", "1200", "67%", "/fixture")},
		{argv: "df -i", stdout: dfRow([4]int{6, 5, 5, 5}, "Filesystem", "Inodes", "IUsed", "IFree", "IUse%", "Mounted on") + dfRow([4]int{6, 5, 5, 5}, "/dev/fixture", "5000", "1000", "4000", "20%", "/fixture") + dfRow([4]int{6, 5, 5, 5}, "tmpfs", "0", "0", "0", "-", "/scratch")},
		{argv: "df --inodes " + scratch, stdout: dfRow([4]int{6, 5, 5, 5}, "Filesystem", "Inodes", "IUsed", "IFree", "IUse%", "Mounted on") + dfRow([4]int{6, 5, 5, 5}, "tmpfs", "0", "0", "0", "-", "/scratch")},
		{argv: "df -hi .", stdout: dfRow([4]int{6, 5, 5, 5}, "Filesystem", "Inodes", "IUsed", "IFree", "IUse%", "Mounted on") + dfRow([4]int{6, 5, 5, 5}, "/dev/fixture", "4.9K", "1000", "4.0K", "20%", "/fixture")},
		{argv: "df missing .", stdout: dfRow(k, "Filesystem", "1K-blocks", "Used", "Available", "Use%", "Mounted on") + dfRow(k, "/dev/fixture", "4000", "2400", "1200", "67%", "/fixture"), stderr: "df: missing: No such file or directory\n", status: 1},
		{argv: "df missing", stderr: "df: missing: No such file or directory\n", status: 1},
	} {
		t.Run(c.argv, func(t *testing.T) {
			h.exec(c.stdin, strings.Fields(c.argv)...).expect(t, c.argv, c.stdout, c.stderr, c.status)
		})
	}
}

func TestDfRefusesWhereThePlatformHasNoStatisticsAndStopsOnFailure(t *testing.T) {
	h := newHarness(t, map[string]string{"input": "x"})
	h.owners.volumes = func(*os.File, string) (fseffect.Volume, error) { return fseffect.Volume{}, fseffect.ErrUnsupported }
	h.exec("", "df", ".").refused(t, "df without statistics", "COMMAND_PLATFORM_UNAVAILABLE")
	h.owners.volumes = func(*os.File, string) (fseffect.Volume, error) { return fseffect.Volume{}, errors.New("statfs failed") }
	if r := h.exec("", "df", "."); r.err == nil || r.result.Status == 0 {
		t.Fatalf("df statistics failure %+v", r.result)
	}
}

// TestDfRefusesPathsOutsideReadAuthority: a path the read authority does not
// admit never reaches the statistics owner.
func TestDfRefusesPathsOutsideReadAuthority(t *testing.T) {
	h := newHarness(t, map[string]string{"input": "x"})
	reached := false
	h.owners.volumes = func(file *os.File, path string) (fseffect.Volume, error) {
		reached = true
		return fixtureVolume(file, path)
	}
	r := h.exec("", "df", "/etc")
	if r.err == nil || r.result.Status == 0 || reached || r.stdout != "" {
		t.Fatalf("df outside the roots: %+v reached %v stdout %q", r.result, reached, r.stdout)
	}
}
