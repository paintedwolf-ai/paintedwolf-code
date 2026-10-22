//go:build darwin || linux

package literal

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/fseffect"
	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

// TestLsLongReportsOwnerNamesAndUTCTimes checks -l, -h and link targets
// against the entries' own metadata, with owners named as GNU ls names them.
func TestLsLongReportsOwnerNamesAndUTCTimes(t *testing.T) {
	h := formHarness(t)
	uid, gid := ownerIDs(t, filepath.Join(h.root, "dir", "a"))
	owner, group := ownerLabel(uid), groupLabel(gid)
	row := func(mode string, size, when, name string) string {
		return fmt.Sprintf("%s 1 %s %s %s %s %s\n", mode, owner, group, size, when, name)
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
// layout (observed from coreutils 9.12) over the entry's own metadata.
func TestStatDefaultIsGNUsDescription(t *testing.T) {
	h := formHarness(t)
	paths := mustPaths(t, h)
	describe := func(name, shown, kind string) string {
		raw := rawStat(t, filepath.Join(h.root, name))
		st, err := os.Lstat(filepath.Join(h.root, name))
		testutil.FailErr(t, "lstat", err)
		bound, err := paths.Bind(t.Context(), name, false)
		testutil.FailErr(t, "bind "+name, err)
		observed, err := bound.Stat(false)
		testutil.FailErr(t, "stat "+name, err)
		mode := fmt.Sprintf("%04o", uint32(st.Mode().Perm()))
		perms := modeString(st.Mode())
		user, group := accountName(lookupUser, raw.Uid), groupName(raw.Gid)
		atime := time.Unix(int64(raw.Atim.Sec), int64(raw.Atim.Nsec)).UTC().Format("2006-01-02 15:04:05.000000000 -0700")
		ctime := time.Unix(int64(raw.Ctim.Sec), int64(raw.Ctim.Nsec)).UTC().Format("2006-01-02 15:04:05.000000000 -0700")
		btime := "-"
		if !observed.BirthTime.IsZero() {
			btime = observed.BirthTime.UTC().Format("2006-01-02 15:04:05.000000000 -0700")
		}
		return fmt.Sprintf("  File: %s\n  Size: %-10d\tBlocks: %-10d IO Block: %-6d %s\nDevice: %d,%d\tInode: %-10d  Links: %d\nAccess: (%s/%s)  Uid: (%5d/%8s)   Gid: (%5d/%8s)\nAccess: %s\nModify: 2026-01-02 03:04:05.000000000 +0000\nChange: %s\n Birth: %s\n",
			shown, st.Size(), raw.Blocks, raw.Blksize, kind, unix.Major(uint64(raw.Dev)), unix.Minor(uint64(raw.Dev)), raw.Ino, raw.Nlink, mode, perms, raw.Uid, user, raw.Gid, group, atime, ctime, btime)
	}
	h.exec("", "stat", "input").expect(t, "stat input", describe("input", "input", "regular file"), "", 0)
	h.exec("", "stat", "link", "empty").expect(t, "stat link empty", describe("link", "link -> input", "symbolic link")+describe("empty", "empty", "regular empty file"), "", 0)
	seed(t, h.root, map[string]string{"two words": "x"})
	testutil.FailErr(t, "time", os.Chtimes(filepath.Join(h.root, "two words"), fixtureTime, fixtureTime))
	h.exec("", "stat", "two words").expect(t, "quoted name", describe("two words", "'two words'", "regular file"), "", 0)
	inputBound, err := paths.Bind(t.Context(), "input", false)
	testutil.FailErr(t, "bind input", err)
	inputStat, err := inputBound.Stat(false)
	testutil.FailErr(t, "stat input", err)
	birthText, birthUnix := "-", "0"
	if !inputStat.BirthTime.IsZero() {
		birthText = inputStat.BirthTime.UTC().Format("2006-01-02 15:04:05.000000000 -0700")
		birthUnix = fmt.Sprint(inputStat.BirthTime.Unix())
	}
	h.exec("", "stat", "-c", "[%-6s][%06s][%.3A][%5n][%h][%Hd,%Ld][%w][%W]", "input").expect(t, "printf flags",
		fmt.Sprintf("[10    ][000010][-rw][input][1][%d,%d][%s][%s]\n", inputStat.DeviceMajor, inputStat.DeviceMinor, birthText, birthUnix), "", 0)
	if w, ok := gnuDirectives["w"].render(statEntry{st: fseffect.EntryStat{}}); !ok || w != "-" {
		t.Fatalf("unknown birth %%w = %q", w)
	}
	if W, ok := gnuDirectives["W"].render(statEntry{st: fseffect.EntryStat{}}); !ok || W != "0" {
		t.Fatalf("unknown birth %%W = %q", W)
	}
}

func lookupUser(id string) (*user.User, error) { return user.LookupId(id) }

// TestLsInodeSizeTimesAndNumericOwners checks -i, -s, -n, -c, -u and -L
// against each entry's own metadata.
func TestLsInodeSizeTimesAndNumericOwners(t *testing.T) {
	h := formHarness(t)
	names := []string{"a", "b", "exe", "link"}
	inodes, sizes := map[string]string{}, map[string]string{}
	iw, sw, blocks := 0, 0, int64(0)
	for _, name := range names {
		raw := rawStat(t, filepath.Join(h.root, "dir", name))
		inodes[name] = fmt.Sprint(raw.Ino)
		sizes[name] = fmt.Sprint((int64(raw.Blocks) + 1) / 2)
		iw, sw = max(iw, len(inodes[name])), max(sw, len(sizes[name]))
		blocks += int64(raw.Blocks)
	}
	var withInode, withSize, withBoth strings.Builder
	for _, name := range names {
		fmt.Fprintf(&withInode, "%*s %s\n", iw, inodes[name], name)
		fmt.Fprintf(&withSize, "%*s %s\n", sw, sizes[name], name)
		fmt.Fprintf(&withBoth, "%*s %*s %s\n", iw, inodes[name], sw, sizes[name], name)
	}
	total := fmt.Sprintf("total %d\n", (blocks+1)/2)
	h.exec("", "ls", "-i", "dir").expect(t, "ls -i", withInode.String(), "", 0)
	h.exec("", "ls", "-s", "dir").expect(t, "ls -s", total+withSize.String(), "", 0)
	h.exec("", "ls", "-is", "dir").expect(t, "ls -is", total+withBoth.String(), "", 0)
	h.exec("", "ls", "-si", "input").expect(t, "ls -si file operand", fmt.Sprintf("%d %d input\n", rawStat(t, filepath.Join(h.root, "input")).Ino, (rawStat(t, filepath.Join(h.root, "input")).Blocks+1)/2), "", 0)

	uid, gid := ownerIDs(t, filepath.Join(h.root, "input"))
	numeric := fmt.Sprintf("-rw-r--r-- 1 %d %d 10 2026-01-02 03:04 input\n", uid, gid)
	named := fmt.Sprintf("-rw-r--r-- 1 %s %s 10 2026-01-02 03:04 input\n", ownerLabel(uid), groupLabel(gid))
	h.exec("", "ls", "-n", "input").expect(t, "ls -n", numeric, "", 0)
	h.exec("", "ls", "-ln", "input").expect(t, "ls -ln", numeric, "", 0)
	h.exec("", "ls", "-li", "input").expect(t, "ls -li", fmt.Sprintf("%d %s", rawStat(t, filepath.Join(h.root, "input")).Ino, named), "", 0)
	owner, group := ownerLabel(uid), groupLabel(gid)

	// -c and -u show and, without -l or with -t, sort by change and access time.
	changed := filepath.Join(h.root, "dir", "a")
	testutil.FailErr(t, "touch access time", os.Chtimes(changed, fixtureTime.Add(24*time.Hour), fixtureTime))
	raw := rawStat(t, changed)
	ctime := time.Unix(int64(raw.Ctim.Sec), int64(raw.Ctim.Nsec)).UTC().Format("2006-01-02 15:04")
	h.exec("", "ls", "-lc", "dir/a").expect(t, "ls -lc", fmt.Sprintf("-rw-r--r-- 1 %s %s 1 %s dir/a\n", owner, group, ctime), "", 0)
	h.exec("", "ls", "-lu", "dir/a").expect(t, "ls -lu", fmt.Sprintf("-rw-r--r-- 1 %s %s 1 2026-01-03 03:04 dir/a\n", owner, group), "", 0)
	h.exec("", "ls", "-u", "dir").expect(t, "ls -u sorts by access", "a\nb\nexe\nlink\n", "", 0)
	h.exec("", "ls", "-ut", "-r", "dir").expect(t, "ls -utr", lsByAccessReversed(t, h), "", 0)
	h.exec("", "ls", "-uc", "dir/a").expect(t, "last of -u and -c wins", "dir/a\n", "", 0)

	h.exec("", "ls", "-lG", "input").expect(t, "ls -lG omits the group", fmt.Sprintf("-rw-r--r-- 1 %s 10 2026-01-02 03:04 input\n", owner), "", 0)
	h.exec("", "ls", "-o", "input").expect(t, "ls -o", fmt.Sprintf("-rw-r--r-- 1 %s 10 2026-01-02 03:04 input\n", owner), "", 0)
	h.exec("", "ls", "-g", "input").expect(t, "ls -g", fmt.Sprintf("-rw-r--r-- 1 %s 10 2026-01-02 03:04 input\n", group), "", 0)
	h.exec("", "ls", "-lH", "link").expect(t, "ls -lH", fmt.Sprintf("-rw-r--r-- 1 %s %s 10 2026-01-02 03:04 link\n", owner, group), "", 0)
	h.exec("", "ls", "-LP", "-F", "dir").expect(t, "ls -LP -F", "a\nb\nexe*\nlink@\n", "", 0)
	h.exec("", "ls", "-HP", "-F", "link").expect(t, "ls -HP -F", "link@\n", "", 0)
	h.exec("", "ls", "-nG", "input").expect(t, "ls -nG", fmt.Sprintf("-rw-r--r-- 1 %d 10 2026-01-02 03:04 input\n", uid), "", 0)
	h.exec("", "ls", "-lL", "dir/link").expect(t, "ls -lL", fmt.Sprintf("-rw-r--r-- 1 %s %s 1 2026-01-02 03:04 dir/link\n", owner, group), "", 0)
	h.exec("", "ls", "--color=always", "dir").refused(t, "color always", "COMMAND_OPERAND_INVALID")
}

// lsByAccessReversed is dir's names by access time, oldest first, with the
// reversed name tie-break -r gives.
func lsByAccessReversed(t *testing.T, h *harness) string {
	t.Helper()
	type named struct {
		name string
		at   time.Time
	}
	var entries []named
	for _, name := range []string{"a", "b", "exe", "link"} {
		raw := rawStat(t, filepath.Join(h.root, "dir", name))
		entries = append(entries, named{name, time.Unix(int64(raw.Atim.Sec), int64(raw.Atim.Nsec))})
	}
	slices.SortStableFunc(entries, func(x, y named) int {
		if !x.at.Equal(y.at) {
			return y.at.Compare(x.at)
		}
		return strings.Compare(x.name, y.name)
	})
	slices.Reverse(entries)
	var out strings.Builder
	for _, e := range entries {
		out.WriteString(e.name + "\n")
	}
	return out.String()
}

// ownerLabel and groupLabel are what GNU ls prints: the account database's
// name, or the decimal id when it has none.
func ownerLabel(id uint32) string {
	if found, err := user.LookupId(fmt.Sprint(id)); err == nil && found.Username != "" {
		return found.Username
	}
	return fmt.Sprint(id)
}

func groupLabel(id uint32) string {
	if found, err := user.LookupGroupId(fmt.Sprint(id)); err == nil && found.Name != "" {
		return found.Name
	}
	return fmt.Sprint(id)
}

// TestLsFallsBackToNumericIDsWithoutAName checks the decimal fallback for an
// id the account database does not know.
func TestLsFallsBackToNumericIDsWithoutAName(t *testing.T) {
	var a accounts
	const unknown = 4294967000
	if got := a.owner(unknown, false); got != "4294967000" {
		t.Fatalf("unknown owner %q", got)
	}
	if got := a.group(unknown, false); got != "4294967000" {
		t.Fatalf("unknown group %q", got)
	}
	if got := a.owner(0, true); got != "0" {
		t.Fatalf("numeric owner %q", got)
	}
	if got, want := a.owner(0, false), ownerLabel(0); got != want {
		t.Fatalf("root owner %q, want %q", got, want)
	}
}
