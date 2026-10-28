//go:build referenceuserland && unix

package referenceuserland

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// These contracts run the verified images through Docker; ./task test:userlands
// builds and verifies them first.

var (
	sharedRunner     *Runner
	sharedRunnerErr  error
	sharedRunnerOnce sync.Once
)

func contractRunner(t *testing.T) *Runner {
	t.Helper()
	sharedRunnerOnce.Do(func() {
		sharedRunner, sharedRunnerErr = NewRunner(context.Background())
	})
	testutil.FailErr(t, "open the Docker runner", sharedRunnerErr)
	return sharedRunner
}

func runIn(t *testing.T, id ID, cases ...Case) []Result {
	t.Helper()
	results, err := contractRunner(t).Run(t.Context(), id, cases)
	testutil.FailErr(t, "run in "+string(id), err)
	return results
}

func TestEveryImageReproducesItsRecordedTree(t *testing.T) {
	r := contractRunner(t)
	for _, id := range IDs {
		inv, err := r.Build(t.Context(), id)
		testutil.FailErr(t, "verify "+string(id), err)
		if inv.Tree != r.Manifest().Userlands[id].Trees[r.Architecture()] || inv.Entries == 0 {
			t.Errorf("userland %s inventory %+v does not match its record", id, inv)
		}
	}
}

func TestEveryUserlandRunsProgramsInTheSameFixedEnvironment(t *testing.T) {
	program := `printf '%s\n' "$(id -u):$(id -g):$(id -un):$(id -gn)" "$(hostname)" "$(pwd)" "$HOME" "$PATH" \
  "$LANG" "$LC_ALL" "$TZ" "$TMPDIR" "$USER" "$LOGNAME" "$SHELL" "$(umask)" "[$(ls -A "$HOME")][$(ls -A /tmp)]" \
  "$(ls /sys/class/net)" "$(ls -ln seed.txt | tr -s ' ')"; cat`
	for _, id := range IDs {
		shell := contractRunner(t).Manifest().Userlands[id].Shell
		want := strings.Join([]string{"1000:1000:reference:reference", "reference", "/workspace", "/home/reference",
			"/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin", "C.UTF-8", "C.UTF-8", "UTC", "/tmp", "reference", "reference", shell, "0022",
			"[][]", "lo", "-rw-r--r-- 1 1000 1000 5 Jan 1 2025 seed.txt"}, "\n") + "\n"
		got := runIn(t, id, Case{Program: program, Workspace: map[string]Entry{"seed.txt": {Data: []byte("seed\n")}}})[0]
		if got.Status != 0 || string(got.Stdout) != want || len(got.Stderr) != 0 {
			t.Errorf("userland %s: %d %q %q, want 0 %q", id, got.Status, got.Stdout, got.Stderr, want)
		}
	}
}

func TestEveryUserlandIsTheImplementationItNames(t *testing.T) {
	for id, probe := range map[ID]struct{ program, want string }{
		GNU: {
			`sed --version | head -n 1; grep --version | head -n 1; ls --version | head -n 1; find --version | head -n 1
tar --version | head -n 1; awk --version | head -n 1 | cut -d, -f1`,
			"sed (GNU sed) 4.9\ngrep (GNU grep) 3.11\nls (GNU coreutils) 9.7\nfind (GNU findutils) 4.10.0\ntar (GNU tar) 1.35\nGNU Awk 5.2.1\n",
		},
		BSD: {
			`awk --version; tar --version | cut -d' ' -f1-2; printf 'a\nb\n' | tail -r; sed --version >/dev/null 2>&1; echo "sed --version: $?"
for c in tac ps gawk bash git jq; do if command -v "$c" >/dev/null; then echo "$c"; fi; done`,
			"awk version 20250804\nbsdtar 3.7.4\nb\na\nsed --version: 1\nbash\ngit\njq\n",
		},
		BusyBox: {
			`busybox | head -n 1 | cut -d' ' -f1-2; tar --version | head -n 1
for c in tac bash git jq; do if command -v "$c" >/dev/null; then echo "$c"; fi; done`,
			"BusyBox v1.37.0\ntar (busybox) 1.37.0\ntac\n",
		},
	} {
		got := runIn(t, id, Case{Program: probe.program})[0]
		if got.Status != 0 || string(got.Stdout) != probe.want {
			t.Errorf("userland %s: %d %q %q, want %q", id, got.Status, got.Stdout, got.Stderr, probe.want)
		}
	}
}

func TestEveryUserlandReturnsExactResultsAndIsolatesEachProgram(t *testing.T) {
	cases := []Case{
		{
			Program: `printf 'out\000put'; printf 'err\n' >&2; printf 'new\n' > made.txt; rm gone.txt; chmod 600 kept.txt
mkdir dir; ln -s made.txt link; printf x > "$HOME/home.txt"; printf x > /tmp/tmp.txt; exit 7`,
			Workspace: map[string]Entry{"gone.txt": {Data: []byte("bye\n")}, "kept.txt": {Data: []byte("same\n")}},
			Tracked:   []string{"made.txt", "gone.txt", "kept.txt", "dir", "link", "never.txt"},
		},
		{
			Program: `ls -A . "$HOME" /tmp`,
			Tracked: []string{"made.txt", "kept.txt"},
		},
	}
	want := []Result{
		{Status: 7, Stdout: []byte("out\x00put"), Stderr: []byte("err\n"), Files: map[string]*Entry{
			"made.txt": {Mode: 0o644, Data: []byte("new\n")}, "gone.txt": nil, "kept.txt": {Mode: 0o600, Data: []byte("same\n")},
			"dir": {Mode: fs.ModeDir | 0o755}, "link": {Mode: fs.ModeSymlink | 0o777, Link: "made.txt"}, "never.txt": nil,
		}},
		{Status: 0, Stdout: []byte(".:\n\n/home/reference:\n\n/tmp:\n"), Stderr: []byte{}, Files: map[string]*Entry{"made.txt": nil, "kept.txt": nil}},
	}
	for _, id := range IDs {
		first := runIn(t, id, cases...)
		if !reflect.DeepEqual(first, want) {
			t.Errorf("userland %s results:\n%s\nwant:\n%s", id, describe(first), describe(want))
		}
		if again := runIn(t, id, cases...); !reflect.DeepEqual(again, first) {
			t.Errorf("userland %s gave different results for the same batch:\n%s\nthen:\n%s", id, describe(first), describe(again))
		}
	}
}

func describe(results []Result) string {
	var b strings.Builder
	for i, r := range results {
		fmt.Fprintf(&b, "  %d: status %d stdout %q stderr %q files %s\n", i, r.Status, r.Stdout, r.Stderr, describeFiles(r.Files))
	}
	return b.String()
}

func describeFiles(files map[string]*Entry) string {
	var parts []string
	for name, entry := range files {
		if entry == nil {
			parts = append(parts, name+"=absent")
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v:%q:%q", name, entry.Mode, entry.Data, entry.Link))
	}
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

func TestInventoryListsWhatEachUserlandProvides(t *testing.T) {
	for id, expect := range map[ID]struct{ present, absent []string }{
		GNU:     {present: []string{"bash", "gawk", "git", "jq", "ps", "sed", "tac", "yq"}, absent: []string{"bsdtar", "busybox"}},
		BSD:     {present: []string{"bash", "bsdtar", "chown", "git", "jq", "sed", "sh", "yq"}, absent: []string{"gawk", "ps", "tac", "busybox"}},
		BusyBox: {present: []string{"busybox", "sed", "sh", "tac"}, absent: []string{"bash", "git", "jq"}},
	} {
		inv, err := contractRunner(t).Inventory(t.Context(), id)
		testutil.FailErr(t, "inventory "+string(id), err)
		for _, name := range expect.present {
			if !slices.Contains(inv.Commands, name) {
				t.Errorf("userland %s does not provide %s", id, name)
			}
		}
		for _, name := range expect.absent {
			if slices.Contains(inv.Commands, name) {
				t.Errorf("userland %s provides %s", id, name)
			}
		}
	}
}
