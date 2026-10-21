//go:build darwin || linux

package literal

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// TestLsLongReportsNumericOwnersAndUTCTimes checks -l, -h and link targets
// against the entries' own metadata.
func TestLsLongReportsNumericOwnersAndUTCTimes(t *testing.T) {
	h := formHarness(t)
	uid, gid := ownerIDs(t, filepath.Join(h.root, "dir", "a"))
	row := func(mode string, size, when, name string) string {
		return fmt.Sprintf("%s 1 %d %d %s %s %s\n", mode, uid, gid, size, when, name)
	}
	// Link permission bits are the platform's own (Linux 0777, Darwin umask).
	link, err := os.Lstat(filepath.Join(h.root, "dir", "link"))
	testutil.FailErr(t, "lstat link", err)
	want := row("-rw-r--r--", "1", "2026-01-02 03:04", "a") + row("-rw-r--r--", "2", "2026-01-02 03:05", "b") +
		row("-rwxr-xr-x", "2", "2026-01-02 03:04", "exe") + row("l"+link.Mode().Perm().String()[1:], "1", "2026-01-02 03:04", "link -> a")
	blocks := int64(0)
	for _, name := range []string{"a", "b", "exe", "link"} {
		blocks += int64(rawStat(t, filepath.Join(h.root, "dir", name)).Blocks)
	}
	h.exec("", "ls", "-l", "dir").expect(t, "ls -l", fmt.Sprintf("total %d\n", (blocks+1)/2)+want, "", 0)
	h.exec("", "ls", "-lh", "dir").expect(t, "ls -lh", "total "+humanSize(blocks*512)+"\n"+want, "", 0)
	seed(t, h.root, map[string]string{"void/": ""})
	h.exec("", "ls", "-l", "void").expect(t, "ls -l empty dir", "total 0\n", "", 0)
	h.exec("", "ls", "-ld", "input").expect(t, "ls -ld", row("-rw-r--r--", "10", "2026-01-02 03:04", "input"), "", 0)
	big := strings.Repeat("x", 5000)
	seed(t, h.root, map[string]string{"big/f": big})
	bigBlocks := int64(rawStat(t, filepath.Join(h.root, "big", "f")).Blocks)
	h.exec("", "ls", "-lh", "big").expect(t, "ls -lh big", "total "+humanSize(bigBlocks*512)+"\n"+row("-rw-r--r--", "4.9K", "2026-01-02 03:04", "f"), "", 0)
}

// TestStatDefaultIsGNUsDescription renders bare stat in GNU's multi-line
// layout (observed from coreutils 8.32) over the entry's own metadata.
func TestStatDefaultIsGNUsDescription(t *testing.T) {
	h := formHarness(t)
	describe := func(name, shown, kind string) string {
		raw := rawStat(t, filepath.Join(h.root, name))
		st, err := os.Lstat(filepath.Join(h.root, name))
		testutil.FailErr(t, "lstat", err)
		mode := fmt.Sprintf("%04o", uint32(st.Mode().Perm()))
		perms := modeString(st.Mode())
		user, group := accountName(lookupUser, raw.Uid), groupName(raw.Gid)
		atime := time.Unix(int64(raw.Atim.Sec), int64(raw.Atim.Nsec)).UTC().Format("2006-01-02 15:04:05.000000000 -0700")
		ctime := time.Unix(int64(raw.Ctim.Sec), int64(raw.Ctim.Nsec)).UTC().Format("2006-01-02 15:04:05.000000000 -0700")
		return fmt.Sprintf("  File: %s\n  Size: %-10d\tBlocks: %-10d IO Block: %-6d %s\nDevice: %xh/%dd\tInode: %-11d Links: %d\nAccess: (%s/%s)  Uid: (%5d/%8s)   Gid: (%5d/%8s)\nAccess: %s\nModify: 2026-01-02 03:04:05.000000000 +0000\nChange: %s\n Birth: -\n",
			shown, st.Size(), raw.Blocks, raw.Blksize, kind, uint64(raw.Dev), uint64(raw.Dev), raw.Ino, raw.Nlink, mode, perms, raw.Uid, user, raw.Gid, group, atime, ctime)
	}
	h.exec("", "stat", "input").expect(t, "stat input", describe("input", "input", "regular file"), "", 0)
	h.exec("", "stat", "link", "empty").expect(t, "stat link empty", describe("link", "link -> input", "symbolic link")+describe("empty", "empty", "regular empty file"), "", 0)
	seed(t, h.root, map[string]string{"two words": "x"})
	testutil.FailErr(t, "time", os.Chtimes(filepath.Join(h.root, "two words"), fixtureTime, fixtureTime))
	h.exec("", "stat", "two words").expect(t, "quoted name", describe("two words", "'two words'", "regular file"), "", 0)
	h.exec("", "stat", "-c", "[%-6s][%06s][%.3A][%5n][%h][%w][%W]", "input").expect(t, "printf flags", "[10    ][000010][-rw][input][1][-][0]\n", "", 0)
}

func lookupUser(id string) (*user.User, error) { return user.LookupId(id) }
