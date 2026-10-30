//go:build darwin || linux

package textutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/commandprogram"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

// gnuTool finds the GNU implementation: the g-prefixed Homebrew install on
// macOS, the system utility on Linux. It must identify itself as GNU.
func gnuTool(t *testing.T, name string) string {
	t.Helper()
	for _, candidate := range []string{"/opt/homebrew/bin/g" + name, "/usr/local/bin/g" + name, "/usr/bin/" + name, "/bin/" + name} {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		version, _ := exec.Command(path, "--version").CombinedOutput()
		if bytes.Contains(version, []byte("GNU")) {
			return path
		}
	}
	t.Fatalf("GNU %s is not installed (g%s on macOS, %s on Linux)", name, name, name)
	return ""
}

// echoChild runs xargs batches the way /bin/echo would with plain words.
type echoChild struct{}

func (echoChild) RunNested(_ context.Context, _ commandprogram.OperationRequest, argv commandprogram.Fields, _ map[string]string, streams commandprogram.Streams) (toolcontract.ProducerResult, error) {
	values := argv.Values()
	if values[0] != "echo" {
		return toolcontract.ProducerResult{Status: 127, Terminal: toolcontract.ProducerCompleted, Complete: true, EffectCertainty: toolcontract.NoEffect}, nil
	}
	_, err := io.WriteString(streams.Output, strings.Join(values[1:], " ")+"\n")
	return toolcontract.ProducerResult{Terminal: toolcontract.ProducerCompleted, Complete: true, PublicationEligible: true, EffectCertainty: toolcontract.EffectAttempted}, err
}

type gnuCase struct {
	source string
	argv   []string
	stdin  string
	stderr bool
}

// GNU coreutils, sed and findutils produce the same bytes, status and, where
// marked, stderr for every form here under LC_ALL=C, including the forms
// where BSD differs. Files come from the shared fixture directory.
func TestDifferentialAgainstGNU(t *testing.T) {
	lines := "pear\nApple\n10\n9\n\xff\nbanana\napple\n-3\n\n"
	cases := map[string][]gnuCase{
		"sed": {
			{"sed ':a;N;$!ba;s/\\n/,/g'", []string{":a;N;$!ba;s/\\n/,/g"}, "a\nb\nc\n", false},
			{"sed -n '1!G;h;$p'", []string{"-n", "1!G;h;$p"}, "a\nb\nc\n", false},
			{"sed '1!G;h;$!d'", []string{"1!G;h;$!d"}, "a\nb\nc\nd\n", false},
			{"sed '$!N;P;D'", []string{"$!N;P;D"}, "a\na\nb\n", false},
			{"sed 'n;d'", []string{"n;d"}, "1\n2\n3\n", false},
			{"sed 'N;s/\\n/-/'", []string{"N;s/\\n/-/"}, "1\n2\n3\n", false},
			{"sed y/abc/xyz/", []string{"y/abc/xyz/"}, "aabbcc\n", false},
			{"sed =", []string{"="}, "x\ny\n", false},
			{"sed -n l", []string{"-n", "l"}, "a\tb\\c\x01\xc3\xa9\n" + strings.Repeat("x", 100) + "\n", false},
			{"sed -n 'l 10'", []string{"-n", "l 10"}, "abcdefghijklmnop\n", false},
			{"sed 0~2d", []string{"0~2d"}, "1\n2\n3\n4\n5\n", false},
			{"sed '/b/,+1d'", []string{"/b/,+1d"}, "a\nb\nc\nd\n", false},
			{"sed 2,~4d", []string{"2,~4d"}, "1\n2\n3\n4\n5\n", false},
			{"sed 0,/a/d", []string{"0,/a/d"}, "a\nb\na\n", false},
			{"sed /B/Id", []string{"/B/Id"}, "a\nb\nc\n", false},
			{"sed s/A/x/Ig", []string{"s/A/x/Ig"}, "aAa\n", false},
			{"sed '/b/s//X/'", []string{"/b/s//X/"}, "abc\nb\n", false},
			{`sed 's/\(\w*\) \(\w*\)/\U\1\E \u\2/'`, []string{`s/\(\w*\) \(\w*\)/\U\1\E \u\2/`}, "hello world\n", false},
			{`sed 's/.*/\L\u&/'`, []string{`s/.*/\L\u&/`}, "hELLO\n", false},
			{"sed 2Q", []string{"2Q"}, "a\nb\nc\n", false},
			{"sed 2q5", []string{"2q5"}, "a\nb\nc\n", false},
			{"sed 's/b/B/;tx;s/$/-/;:x'", []string{"s/b/B/;tx;s/$/-/;:x"}, "abc\nxyz\n", false},
			{"sed 's/x/y/;Tend;s/$/!/;:end'", []string{"s/x/y/;Tend;s/$/!/;:end"}, "x\nz\n", false},
			{"sed -e '2i\\\nins' -e '3a app'", []string{"-e", "2i\\\nins", "-e", "3a app"}, "1\n2\n3\n", false},
			{"sed -z s/b/B/", []string{"-z", "s/b/B/"}, "a\x00b\x00", false},
			{"sed -E 's/(a|b)+/<&>/g'", []string{"-E", "s/(a|b)+/<&>/g"}, "aab c\n", false},
			{"sed '$!c gone'", []string{"$!c gone"}, "x\ny\n", false},
			{"sed p", []string{"p"}, "x", false},
			{"sed -n '/x/{p;p}'", []string{"-n", "/x/{p;p}"}, "x\ny\n", false},
		},
		"sort": {
			{"sort", []string{}, lines, false},
			{"sort -f", []string{"-f"}, "b\nA\na\nB\n", false},
			{"sort -fs", []string{"-fs"}, "b\nA\na\nB\n", false},
			{"sort -b", []string{"-b"}, "  b\na\n c\n", false},
			{"sort -h", []string{"-h"}, "1G\n10K\n2M\n500\n-1K\n1.5K\n", false},
			{"sort -V", []string{"-V"}, "v1.10\nv1.2\nv1.9a\nv1.9\na-1.0~rc1\na-1.0\n.hidden\nfile.tar.gz\nfile.tar\n", false},
			{"sort -s -k 1,1", []string{"-s", "-k", "1,1"}, "b 2\na 1\nb 1\n", false},
			{"sort -k 2,2h", []string{"-k", "2,2h"}, "x 2K\ny 1M\nz 3\n", false},
			{"sort -t . -k 2,2Vr", []string{"-t", ".", "-k", "2,2Vr"}, "a.1\nb.10\nc.2\n", false},
			{"sort -k 1.2b,1.3", []string{"-k", "1.2b,1.3"}, " xb\n ya\n", false},
			{"sort -c", []string{"-c"}, "a\nc\nb\n", true},
			{"sort -cu", []string{"-cu"}, "a\na\n", true},
			{"sort -n -u", []string{"-n", "-u"}, "1\n01\n2\n", false},
		},
		"uniq": {
			{"uniq -c", []string{"-c"}, "a\na\nb\n", false},
			{"uniq -c -d", []string{"-c", "-d"}, "x\nx\nx\ny\n", false},
			{"uniq -i", []string{"-i"}, "a\nA\nb\n", false},
			{"uniq -f 1", []string{"-f", "1"}, "1 x\n2 x\n3 y\n", false},
			{"uniq -s 2", []string{"-s", "2"}, "aax\nbbx\nccy\n", false},
			{"uniq -w 2", []string{"-w", "2"}, "abX\nabY\nac\n", false},
			{"uniq -z", []string{"-z"}, "a\x00a\x00b\x00", false},
		},
		"seq": {
			{"seq -s , 4", []string{"-s", ",", "4"}, "", false},
			{"seq -w 8 10", []string{"-w", "8", "10"}, "", false},
			{"seq -w -1 1", []string{"-w", "-1", "1"}, "", false},
			{"seq 10 -3 1", []string{"10", "-3", "1"}, "", false},
			{"seq 1 0.5 2", []string{"1", "0.5", "2"}, "", false},
			{"seq 0.1 0.1 0.3", []string{"0.1", "0.1", "0.3"}, "", false},
			{"seq -f %03g 2", []string{"-f", "%03g", "2"}, "", false},
			{"seq 5 1", []string{"5", "1"}, "", false},
		},
		"cut": {
			{"cut -d : -f 2 --complement", []string{"-d", ":", "-f", "2", "--complement"}, "a:b:c\n", false},
			{"cut -d : -f 1,3 --output-delimiter=' | '", []string{"-d", ":", "-f", "1,3", "--output-delimiter= | "}, "a:b:c\n", false},
			{"cut -b 1-2,4-5 --output-delimiter=:", []string{"-b", "1-2,4-5", "--output-delimiter=:"}, "abcdef\n", false},
			{"cut -z -f 1 -d ,", []string{"-z", "-f", "1", "-d", ","}, "a,b\x00c,d\x00", false},
		},
		"tr": {
			{"tr -c a-z _", []string{"-c", "a-z", "_"}, "ab-c 1\n", false},
			{"tr -t abc xy", []string{"-t", "abc", "xy"}, "abc\n", false},
			{"tr abc 'x[y*]'", []string{"abc", "x[y*]"}, "abc\n", false},
			{"tr a-c '[x*2]z'", []string{"a-c", "[x*2]z"}, "abc\n", false},
			{"tr '[=a=]' b", []string{"[=a=]", "b"}, "aa\n", false},
			{"tr -s '[:lower:]' '[:upper:]'", []string{"-s", "[:lower:]", "[:upper:]"}, "aabbccDD\n", false},
			{"tr -ds a-c x-z", []string{"-ds", "a-c", "x-z"}, "abxxcyyz\n", false},
		},
		"paste": {
			{"paste - -", []string{"-", "-"}, "a\nb\nc\n", false},
			{"paste -s -d ',;' -", []string{"-s", "-d", ",;", "-"}, "a\nb\nc\nd\n", false},
		},
		"fold": {
			{"fold -w 5 -s", []string{"-w", "5", "-s"}, "ab cd ef\n", false},
			{"fold -w 4", []string{"-w", "4"}, "a\tb\n", false},
			{"fold -b -w 2", []string{"-b", "-w", "2"}, "abcde\n", false},
		},
		"tac": {
			{"tac", []string{}, "a\nb\nc\n", false},
			{"tac", []string{}, "a\nb", false},
			{"tac -s ,", []string{"-s", ","}, "a,b,c,", false},
		},
		"echo": {
			{"echo -- -n", []string{"--", "-n"}, "", false},
			{"echo -e 'a\\tb'", []string{"-e", "a\\tb"}, "", false},
		},
		"xargs": {
			{"xargs echo", []string{"echo"}, "a b\nc\n", false},
			{"xargs echo", []string{"echo"}, "", false},
			{"xargs -r echo", []string{"-r", "echo"}, "", false},
			{"xargs -n 2 echo", []string{"-n", "2", "echo"}, "1 2 3 4 5", false},
			{"xargs -L 1 echo", []string{"-L", "1", "echo"}, "a \nb\nc\n", false},
			{"xargs -d , echo", []string{"-d", ",", "echo"}, "a b,c", false},
			{"xargs -E stop echo", []string{"-E", "stop", "echo"}, "a stop b", false},
			{"xargs -I {} echo [{}]", []string{"-I", "{}", "echo", "[{}]"}, "x\n y\n", false},
			{"xargs -t echo", []string{"-t", "echo"}, "a b", true},
		},
	}
	for name, group := range cases {
		reference := gnuTool(t, name)
		for _, c := range group {
			t.Run(c.source, func(t *testing.T) {
				h := newHarness(t)
				h.nested = echoChild{}
				cmd := exec.Command(reference, c.argv...)
				cmd.Args[0] = name
				cmd.Env = []string{"LC_ALL=C", "PATH=" + filepath.Dir(reference) + ":/usr/bin:/bin"}
				cmd.Stdin = strings.NewReader(c.stdin)
				var stdout, stderr bytes.Buffer
				cmd.Stdout, cmd.Stderr = &stdout, &stderr
				status := 0
				if err := cmd.Run(); err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatalf("GNU %s: %v", c.source, err)
					}
					status = exit.ExitCode()
				}
				r := h.run(c.source, c.stdin)
				r.expectOutput(t, c.source, stdout.String(), status)
				if c.stderr && r.stderr != stderr.String() {
					t.Fatalf("%s stderr %q, GNU %q", c.source, r.stderr, stderr.String())
				}
			})
		}
	}
}
