//go:build darwin || linux

package textutil

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// hostTool finds a platform utility in the fixed system directories only.
func hostTool(t *testing.T, name string) string {
	t.Helper()
	for _, dir := range []string{"/usr/bin", "/bin"} {
		if path, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
			return path
		}
	}
	t.Fatalf("host %s is not installed in /usr/bin or /bin", name)
	return ""
}

// The host's GNU or BSD utility under LC_ALL=C produces the same bytes for
// forms where both dialects agree. Inputs are small fixed fixtures; the
// golden tables remain the specification.
func TestDifferentialAgainstHostUtilities(t *testing.T) {
	lines := "pear\nApple\n10\n9\n\xff\nbanana\napple\n-3\n\n"
	cases := []struct {
		source string
		argv   []string
		stdin  string
	}{
		{"sort", nil, lines},
		{"sort -r", []string{"-r"}, lines},
		{"sort -u", []string{"-u"}, lines + lines},
		{"sort -n", []string{"-n"}, "10\n9\n-3\n2.5\n2.25\n0\n"},
		{"sort -t : -k 2n", []string{"-t", ":", "-k", "2n"}, "a:10\nb:9\nc:-1\n"},
		{"sort -t : -k 2,2 -k 1,1r", []string{"-t", ":", "-k", "2,2", "-k", "1,1r"}, "b:x\na:x\nc:a\n"},
		{"sort -z", []string{"-z"}, "b\x00a\x00c\x00"},
		{"uniq", nil, "a\na\nb\nb\nb\nc\na\n"},
		{"uniq -d", []string{"-d"}, "a\na\nb\nc\nc\n"},
		{"uniq -u", []string{"-u"}, "a\na\nb\nc\nc\n"},
		{"cut -d : -f 2", []string{"-d", ":", "-f", "2"}, "a:b:c\nnone\nx:y\n"},
		{"cut -d : -f 1,3", []string{"-d", ":", "-f", "1,3"}, "a:b:c:d\n"},
		{"cut -b 2-3", []string{"-b", "2-3"}, "abcd\n\xff\xfeq\n"},
		{"tr a-z A-Z", []string{"a-z", "A-Z"}, "Hello, world\n\xff\n"},
		{"tr -d '[:digit:]'", []string{"-d", "[:digit:]"}, "a1b2c3\n"},
		{"tr -s ' '", []string{"-s", " "}, "a   b  c\n"},
		{"cut -d : -f 2 -s", []string{"-d", ":", "-f", "2", "-s"}, "a:b\nnone\n"},
		{"tr -c a-z _", []string{"-c", "a-z", "_"}, "ab-c 1\n"},
		{"tr -cs '[:alnum:]' '\\n'", []string{"-cs", "[:alnum:]", "\\n"}, "a  b,,c\n"},
		{"rev", nil, "abc\nxy\n"},
		{"paste -s -d , -", []string{"-s", "-d", ",", "-"}, "a\nb\nc\n"},
		{"fold -w 3", []string{"-w", "3"}, "abcdefg\nhi\n"},
		{"seq 3", []string{"3"}, ""},
		{"seq 2 3 11", []string{"2", "3", "11"}, ""},
		{"fold -w 4", []string{"-w", "4"}, "a\tb\n"},
		{"printf '%s-%d|%5.2f|%x\\n' a 42 3.14159 255", []string{"%s-%d|%5.2f|%x\\n", "a", "42", "3.14159", "255"}, ""},
	}
	h := newHarness(t)
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			name := strings.Fields(c.source)[0]
			cmd := exec.Command(hostTool(t, name), c.argv...)
			cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
			cmd.Stdin = strings.NewReader(c.stdin)
			var stdout bytes.Buffer
			cmd.Stdout = &stdout
			if err := cmd.Run(); err != nil {
				t.Fatalf("host %s: %v", c.source, err)
			}
			h.run(c.source, c.stdin).expectOutput(t, c.source, stdout.String(), 0)
		})
	}
}
