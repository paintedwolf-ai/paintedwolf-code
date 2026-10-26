//go:build darwin || linux

package textutil

import (
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestEchoGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"echo alpha beta", "", "alpha beta\n", 0},
		{"echo", "", "\n", 0},
		{"echo -n a", "", "a", 0},
		{`echo 'a\nb'`, "", "a\\nb\n", 0},
		{`echo -e 'a\tb\x41\0101\\'`, "", "a\tbAA\\\n", 0},
		{`echo -e 'stop\cnever' more`, "", "stop", 0},
		{`echo -eE 'a\nb'`, "", "a\\nb\n", 0},
		{`echo -Ee 'a\nb'`, "", "a\nb\n", 0},
		{"echo -ne 'x\\n'", "", "x\n", 0},
		{"echo -x -n", "", "-x -n\n", 0},
		{"echo a -n", "", "a -n\n", 0},
		{"echo -- -n", "", "-- -n\n", 0},
		{`echo '$(rm -rf /)' ';' '|'`, "", "$(rm -rf /) ; |\n", 0},
	})
}

func TestPrintfGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{`printf '%s\n' alpha`, "", "alpha\n", 0},
		{`printf '%s-%s\n' a b c`, "", "a-b\nc-\n", 0},
		{`printf 'plain'`, "", "plain", 0},
		{`printf '%d %i %o %u %x %X\n' 42 -7 8 3 255 255`, "", "42 -7 10 3 ff FF\n", 0},
		{`printf '%5d|%-5d|%05d|%+d\n' 7 7 7 7`, "", "    7|7    |00007|+7\n", 0},
		{`printf '%x %o\n' 0x1f 017`, "", "1f 17\n", 0},
		{`printf '%d\n' "'A"`, "", "65\n", 0},
		{`printf '%u\n' -1`, "", "18446744073709551615\n", 0},
		{`printf '%f %.2f %e %g %G\n' 1.5 2.345 1234.5 0.0001 1e20`, "", "1.500000 2.35 1.234500e+03 0.0001 1E+20\n", 0},
		{`printf '%g\n' 1234567`, "", "1.23457e+06\n", 0},
		{`printf '%5s|%-5s|%.2s\n' ab ab abcdef`, "", "   ab|ab   |ab\n", 0},
		{`printf '%.1s|%3c\n' é xyz`, "", "\xc3|  x\n", 0},
		{`printf '%b|%s\n' 'a\tb' 'a\tb'`, "", "a\tb|a\\tb\n", 0},
		{`printf '%b' 'x\cy' 'never'`, "", "x", 0},
		{`printf '\101\x42\n\\%%\n'`, "", "AB\n\\%\n", 0},
		{`printf '%d %s|\n'`, "", "0 |\n", 0},
		{`printf -- '%s\n' -x`, "", "-x\n", 0},
		{`printf '%s\n' '$(id)' '; rm'`, "", "$(id)\n; rm\n", 0},
		{`printf 'x\0y'`, "", "x\x00y", 0},
	})
}

func TestPrintfRefusalsAndInvalidNumbers(t *testing.T) {
	h := newHarness(t)
	for _, source := range []string{`printf '%q' x`, `printf '%'`, `printf '%n' x`, `printf`} {
		h.run(source, "").expectRefusal(t, source, "COMMAND_OPERAND_INVALID")
	}
	r := h.run(`printf '%d|%d\n' 12abc zz`, "")
	r.expectOutput(t, "invalid numbers", "12|0\n", 1)
	if r.stderr != "printf: 12abc: invalid number\nprintf: zz: invalid number\n" {
		t.Fatalf("stderr %q", r.stderr)
	}
}

func TestTrGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"tr abc xyz", "aabbcc-d\n", "xxyyzz-d\n", 0},
		{"tr a-z A-Z", "hello, World\n", "HELLO, WORLD\n", 0},
		{"tr '[:lower:]' '[:upper:]'", "mixed Case 9\n", "MIXED CASE 9\n", 0},
		{"tr abc x", "abcd\n", "xxxd\n", 0},
		{"tr -d '[:digit:]'", "a1b22c333\n", "abc\n", 0},
		{"tr -s ' '", "a   b  c\n", "a b c\n", 0},
		{"tr -s ab xy", "aabbab\n", "xyxy\n", 0},
		{"tr -ds a ' '", "a  ba  c\n", " b c\n", 0},
		{`tr '\n' ,`, "a\nb\n", "a,b,", 0},
		{`tr 'a\-z' xyz`, "a-z m\n", "xyz m\n", 0},
		{`tr '\101' z`, "AB\n", "zB\n", 0},
		{"tr x y", "\xff\xfex\x00\n", "\xff\xfey\x00\n", 0},
		{"tr -c a-z _", "ab-c 1\n", "ab_c___", 0},
		{"tr -cd '[:digit:]'", "a1b2\n", "12", 0},
		{"tr -t abc xy", "abc\n", "xyc\n", 0},
		{"tr abc 'x[y*]'", "abc\n", "xyy\n", 0},
		{"tr a-c '[x*2]z'", "abc\n", "xxz\n", 0},
		{"tr '[=a=]' b", "aa\n", "bb\n", 0},
		{"tr -cs '[:alnum:]' '\n'", "a  b,,c\n", "a\nb\nc\n", 0},
	})
}

func TestTrRefusals(t *testing.T) {
	h := newHarness(t)
	for _, source := range []string{"tr a", "tr -d a b", "tr a b c", "tr z-a x", "tr 'é' e", "tr '[:nope:]' x", "tr a ''", "tr"} {
		h.run(source, "x").expectRefusal(t, source, "COMMAND_OPERAND_INVALID")
	}
	h.run("tr -x a b", "x").expectRefusal(t, "tr -x", "COMMAND_OPTION_UNSUPPORTED")
}

func TestCutGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"cut -d : -f 2", "a:b:c\nx:y\nnone\n", "b\ny\nnone\n", 0},
		{"cut -f 1,3", "a\tb\tc\n", "a\tc\n", 0},
		{"cut -d , -f 3,1", "a,b,c,d\n", "a,c\n", 0},
		{"cut -d , -f 2-", "a,b,c,d\n", "b,c,d\n", 0},
		{"cut -d , -f -2,4", "a,b,c,d\n", "a,b,d\n", 0},
		{"cut -d , -f 2,2,1-2", "a,b,c\n", "a,b\n", 0},
		{"cut -d , -f 5", "a,b\n", "\n", 0},
		{"cut -b 2-3", "abcd\nx\n", "bc\n\n", 0},
		{"cut -b 1", "é\n", "\xc3\n", 0},
		{"cut -c 1,3", "aéb\n", "ab\n", 0},
		{"cut -c 2", "aéb\n", "é\n", 0},
		{"cut -b 1-2", "no final newline", "no\n", 0},
		{"cut -d : -f 2 -s", "a:b\nnone\n", "b\n", 0},
		{"cut -d : -f 2 --complement", "a:b:c\n", "a:c\n", 0},
		{"cut -d : -f 1,3 --output-delimiter=' | '", "a:b:c\n", "a | c\n", 0},
		{"cut -b 1-2,4-5 --output-delimiter=:", "abcdef\n", "ab:de\n", 0},
		{"cut -c 2- --complement", "aéb\n", "a\n", 0},
		{"cut -n -b 1", "ab\n", "a\n", 0},
		{"cut -z -f 1 -d ,", "a,b\x00c,d\x00", "a\x00c\x00", 0},
	})
}

func TestCutRefusals(t *testing.T) {
	h := newHarness(t)
	for _, c := range []struct{ source, stdin string }{
		{"cut -f 0", ""}, {"cut -f 3-1", ""}, {"cut -f a", ""}, {"cut -f -", ""}, {"cut -b 1 -c 1", ""},
		{"cut -d : -b 1", ""}, {"cut -d ab -f 1", ""}, {"cut", ""}, {"cut -c 1", "ok\n\xff\xfe\n"},
	} {
		h.run(c.source, c.stdin).expectRefusal(t, c.source, "COMMAND_OPERAND_INVALID")
	}
}

func TestUniqGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"uniq", "a\na\nb\na\n", "a\nb\na\n", 0},
		{"uniq -c", "a\na\nb\n", "      2 a\n      1 b\n", 0},
		{"uniq -d", "a\na\nb\nc\nc\n", "a\nc\n", 0},
		{"uniq -u", "a\na\nb\nc\nc\n", "b\n", 0},
		{"uniq -du", "a\na\nb\n", "", 0},
		{"uniq -c -d", "x\nx\nx\ny\n", "      3 x\n", 0},
		{"uniq -i", "a\nA\nb\n", "a\nb\n", 0},
		{"uniq -f 1", "1 x\n2 x\n3 y\n", "1 x\n3 y\n", 0},
		{"uniq -s 2", "aax\nbbx\nccy\n", "aax\nccy\n", 0},
		{"uniq -w 2", "abX\nabY\nac\n", "abX\nac\n", 0},
		{"uniq -z", "a\x00a\x00b\x00", "a\x00b\x00", 0},
		{"uniq", "", "", 0},
		{"uniq", "a\na", "a\n", 0},
		{"uniq", "a\x00\na\x00\n\xff\n", "a\x00\n\xff\n", 0},
	})
}

func TestUniqOperands(t *testing.T) {
	h := newHarness(t)
	h.seed("in.txt", "q\nq\nr\n")
	h.run("uniq in.txt", "ignored").expectOutput(t, "uniq INPUT", "q\nr\n", 0)
	h.run("uniq - ", "s\ns\n").expectOutput(t, "uniq -", "s\n", 0)
	h.run("uniq in.txt out.txt", "").expectOutput(t, "uniq OUTPUT", "", 0)
	if got := h.read("out.txt"); got != "q\nr\n" {
		t.Fatalf("OUTPUT holds %q", got)
	}
	h.seed("out.txt", "old\n")
	h.run("uniq -c - out.txt", "s\ns\n").expectOutput(t, "uniq stdin to OUTPUT", "", 0)
	if got := h.read("out.txt"); got != "      2 s\n" {
		t.Fatalf("OUTPUT holds %q", got)
	}
	h.run("uniq a b c", "").expectRefusal(t, "uniq too many", "COMMAND_OPERAND_INVALID")
	r := h.run("uniq missing.txt", "")
	r.expectOutput(t, "uniq missing", "", 1)
	if r.stderr != "uniq: missing.txt: No such file or directory\n" {
		t.Fatalf("stderr %q", r.stderr)
	}
}

// Every file operand is pinned through the execution revision owner and its
// pin recorded under the child's execution before its bytes are used.
func TestFileOperandsArePinned(t *testing.T) {
	h := newHarness(t)
	h.seed("one.txt", "b\na\n")
	h.seed("two.txt", "c\n")
	r := h.run("sort one.txt - two.txt", "d\n")
	r.expectOutput(t, "pinned sort", "a\nb\nc\nd\n", 0)
	pins := h.pins.executions[r.execution]
	if len(pins) != 2 || pins[0].Path != "one.txt" || pins[1].Path != "two.txt" || pins[0].Role != toolcontract.SourceRevision || pins[0].ContentBytes != 4 {
		t.Fatalf("pins %+v", pins)
	}
	r = h.run("cut -b 1 two.txt missing.txt", "")
	r.expectOutput(t, "missing operand keeps earlier pins", "c\n", 1)
	if got := h.pins.executions[r.execution]; len(got) != 1 || got[0].Path != "two.txt" {
		t.Fatalf("pins %+v", got)
	}
}
