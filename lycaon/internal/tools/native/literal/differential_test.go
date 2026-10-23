package literal

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// referenceFamily names the compatibility target a host binary must be
// before it can judge a form: GNU coreutils and diffutils for their
// utilities, vim's xxd, and any canonical hexdump -C.
var referenceFamily = map[string]string{
	"cat": "GNU coreutils", "head": "GNU coreutils", "tail": "GNU coreutils", "nl": "GNU coreutils",
	"wc": "GNU coreutils", "ls": "GNU coreutils", "stat": "GNU coreutils", "base64": "GNU coreutils",
	"sha1sum": "GNU coreutils", "sha256sum": "GNU coreutils", "sha512sum": "GNU coreutils", "md5sum": "GNU coreutils",
	"diff": "GNU diffutils", "xxd": "xxd", "hexdump": "hexdump",
}

// differentialCases are forms whose catalog contract is the reference's own
// output. Forms the catalog deliberately defines differently (file's corpus,
// malformed manifests, long listings without
// block totals) are fixed by the exact fixtures instead.
var differentialCases = []struct {
	argv, stdin string
	stderr      bool
}{
	{argv: "cat -n lines"}, {argv: "cat -b lines"}, {argv: "cat -s lines"}, {argv: "cat -A lines"}, {argv: "cat -v lines"}, {argv: "cat -T lines"},
	{argv: "cat -n input empty input"}, {argv: "cat missing input", stderr: true},
	{argv: "head -n 2 lines"}, {argv: "head -3 ten"}, {argv: "head -c 3 input"}, {argv: "head input empty"}, {argv: "head -q input empty"},
	{argv: "head -v input"}, {argv: "head missing", stderr: true},
	{argv: "tail -n 1 input"}, {argv: "tail -n +2 lines"}, {argv: "tail -3 ten"}, {argv: "tail -c 3 input"}, {argv: "tail -c +3 input"},
	{argv: "tail input empty"}, {argv: "tail -n 2", stdin: "1\n2\n3"},
	{argv: "nl lines"}, {argv: "nl -ba -v 5 -w 3 -s : lines"}, {argv: "nl input"},
	{argv: "wc -l input"}, {argv: "wc -l crlf"}, {argv: "wc -w words"}, {argv: "wc -c utf8"}, {argv: "wc -m utf8"}, {argv: "wc -L crlf"}, {argv: "wc -l", stdin: "a\nb"},
	{argv: "ls dir"}, {argv: "ls -a dir"}, {argv: "ls -A dir"}, {argv: "ls -1 dir"}, {argv: "ls -d dir"}, {argv: "ls -F dir"}, {argv: "ls -R tree-a"},
	{argv: "ls -t dir"}, {argv: "ls -S dir"}, {argv: "ls -r dir"}, {argv: "ls input dir"}, {argv: "ls missing", stderr: true},
	{argv: "ls -p tree-a"}, {argv: "ls --group-directories-first tree-a"}, {argv: "ls -L dir"}, {argv: "ls -LF dir"},
	{argv: "ls --color=never dir"}, {argv: "ls --color=auto dir"}, {argv: "ls -i dir"}, {argv: "ls -s dir"}, {argv: "ls -is tree-a"},
	{argv: "ls -m dir"}, {argv: "ls -sm dir"}, {argv: "ls -im dir"}, {argv: "ls -ism dir"}, {argv: "ls -k dir"}, {argv: "ls -ks dir"}, {argv: "ls -H -F link"},
	{argv: "stat input"}, {argv: "stat dir"}, {argv: "stat link"},
	{argv: "stat -c %n|%s|%a|%A|%F|%Y input"}, {argv: "stat -c %Hd,%Ld input"}, {argv: "stat -c %F empty"}, {argv: "stat -c %F link"}, {argv: "stat -L -c %F link"}, {argv: "stat -c %%%s input"},
	{argv: "diff same-a same-b"}, {argv: "diff -u old new"}, {argv: "diff -U 1 old new"}, {argv: "diff -q old new"}, {argv: "diff -u noeol-a noeol-b"},
	{argv: "diff -u -w ws-a ws-b"}, {argv: "diff -u -b ws-a ws-b"}, {argv: "diff -u -B blank-a blank-b"}, {argv: "diff -u blank-a blank-b"},
	{argv: "diff bin-a bin-b"}, {argv: "diff -u empty input"}, {argv: "diff -u input empty"}, {argv: "diff -r -u tree-a tree-b"},
	{argv: "diff -u tree-a tree-b"}, {argv: "diff -q tree-a tree-b"}, {argv: "diff -u missing input", stderr: true},
	{argv: "xxd lines"}, {argv: "xxd -g 1 input"}, {argv: "xxd -g 4 lines"}, {argv: "xxd -p lines"}, {argv: "xxd -l 4 -s 2 lines"}, {argv: "xxd -s -3 lines"}, {argv: "xxd empty"},
	{argv: "hexdump -C input"}, {argv: "hexdump -C -n 4 -s 2 lines"}, {argv: "hexdump -C empty"}, {argv: "hexdump -C", stdin: strings.Repeat("a", 50)},
	{argv: "sha256sum empty input"}, {argv: "sha1sum empty"}, {argv: "md5sum empty"}, {argv: "sha512sum empty"},
	{argv: "base64 lines"}, {argv: "base64 -w 4 input"}, {argv: "base64 -w 0 input"}, {argv: "base64 empty"}, {argv: "base64 -d", stdin: "QU\nJD\n"},
}

// referenceBinary finds a host utility of the selected compatibility target.
// Homebrew and other ports install GNU tools with a g prefix (gls, gstat)
// beside the platform's own; either spelling qualifies when it is the target.
func referenceBinary(name string) (string, bool) {
	family := referenceFamily[name]
	for _, candidate := range []string{"g" + name, name} {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		if family == "hexdump" {
			return path, true
		}
		for _, flag := range []string{"--version", "-v"} {
			out, _ := exec.Command(path, flag).CombinedOutput()
			if strings.Contains(string(out), family) {
				return path, true
			}
		}
	}
	return "", false
}

// TestDifferentialAgainstReferenceUtilities compares each form with the
// host's reference binary over the same fixture tree, where that binary is
// the chosen compatibility target. The exact fixtures above remain the
// required evidence; a host without the target simply has nothing to compare.
func TestDifferentialAgainstReferenceUtilities(t *testing.T) {
	h := formHarness(t)
	binDir := t.TempDir()
	binaries := map[string]string{}
	compared := 0
	for _, c := range differentialCases {
		argv := strings.Fields(c.argv)
		binary, cached := binaries[argv[0]]
		if !cached {
			found, ok := referenceBinary(argv[0])
			if ok {
				// On Darwin getprogname() reads the exec path rather than
				// argv[0]; a link under the canonical name keeps diagnostics
				// named "cat: ...", not "gcat: ...".
				binary = filepath.Join(binDir, argv[0])
				if err := os.Symlink(found, binary); err != nil {
					t.Fatalf("link reference %s: %v", argv[0], err)
				}
			}
			binaries[argv[0]] = binary
		}
		if binary == "" {
			continue
		}
		t.Run(c.argv, func(t *testing.T) {
			cmd := exec.Command(binary, argv[1:]...)
			cmd.Args[0] = argv[0]
			cmd.Dir = h.root
			cmd.Env = []string{"TZ=UTC", "LC_ALL=C.UTF-8", "PATH=" + os.Getenv("PATH")}
			cmd.Stdin = strings.NewReader(c.stdin)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := runBounded(cmd)
			status := 0
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				status = exit.ExitCode()
			} else if err != nil {
				t.Fatalf("reference %s: %v", binary, err)
			}
			native := h.exec(c.stdin, argv...)
			if native.err != nil {
				t.Fatalf("native %s: %v", c.argv, native.err)
			}
			if native.stdout != stdout.String() || native.result.Status != status || (c.stderr && native.stderr != stderr.String()) {
				t.Fatalf("%s differs from %s\nnative    %q status %d stderr %q\nreference %q status %d stderr %q", c.argv, binary, native.stdout, native.result.Status, native.stderr, stdout.String(), status, stderr.String())
			}
		})
		compared++
	}
	t.Logf("compared %d forms with reference utilities", compared)
}

func runBounded(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		return errors.New("reference utility exceeded 10s")
	}
}
