//go:build darwin || linux

package textutil

import (
	"reflect"
	"strings"
	"testing"
)

func TestGeneratorsGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"seq 3", "", "1\n2\n3\n", 0},
		{"seq 2 4", "", "2\n3\n4\n", 0},
		{"seq 10 -3 1", "", "10\n7\n4\n1\n", 0},
		{"seq -2 0", "", "-2\n-1\n0\n", 0},
		{"seq 1 0.5 2", "", "1.0\n1.5\n2.0\n", 0},
		{"seq 0.1 0.1 0.3", "", "0.1\n0.2\n0.3\n", 0},
		{"seq -w 8 10", "", "08\n09\n10\n", 0},
		{"seq -w -1 1", "", "-1\n00\n01\n", 0},
		{"seq -s , 3", "", "1,2,3\n", 0},
		{"seq -f %03g 2", "", "001\n002\n", 0},
		{"seq 5 1", "", "", 0},
		{"seq 1e1 1e1", "", "10\n", 0},
	})
	r := newHarness(t).run("seq 1 0 3", "")
	r.expectRefusal(t, "zero increment", "COMMAND_OPERAND_INVALID")
}

func TestYesStopsAtTheStreamCeiling(t *testing.T) {
	h := newHarness(t)
	h.owners.limits["stream_throughput_bytes"] = 1 << 16
	r := h.run("yes a b", "")
	reject := r.expectRefusal(t, "unbounded yes", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "stream_throughput_bytes" || !strings.HasPrefix(r.stdout, "a b\na b\n") {
		t.Fatalf("yes: %+v %q", reject.Data, r.stdout[:min(len(r.stdout), 16)])
	}
}

func TestLineUtilitiesGolden(t *testing.T) {
	h := newHarness(t)
	h.seed("left", "1 a\n2 b\n4 d\n")
	h.seed("right", "1 x\n3 y\n4 z\n4 w\n")
	h.seed("one", "a\nb\nd\n")
	h.seed("two", "b\nc\nd\n")
	h.goldens(t, []golden{
		{"tac", "a\nb\nc\n", "c\nb\na\n", 0},
		{"tac", "a\nb", "ba\n", 0},
		{"tac -s ,", "a,b,c,", "c,b,a,", 0},
		{"rev", "abc\naé\n", "cba\néa\n", 0},
		{"rev", "x\xff", "\xffx", 0},
		{"fold -w 3", "abcdefg\n", "abc\ndef\ng\n", 0},
		{"fold -w 5 -s", "ab cd ef\n", "ab \ncd ef\n", 0},
		{"fold -b -w 2", "éa\n", "\xc3\xa9\na\n", 0},
		{"paste -s -d , -", "a\nb\nc\n", "a,b,c\n", 0},
		{"paste - -", "a\nb\nc\n", "a\tb\nc\t\n", 0},
		{"paste one two", "", "a\tb\nb\tc\nd\td\n", 0},
		{"paste -d '\\0' one two", "", "ab\nbc\ndd\n", 0},
		{"comm one two", "", "a\n\t\tb\n\tc\n\t\td\n", 0},
		{"comm -12 one two", "", "b\nd\n", 0},
		{"comm -3 one two", "", "a\n\tc\n", 0},
		{"join left right", "", "1 a x\n4 d z\n4 d w\n", 0},
		{"join -a 1 -a 2 left right", "", "1 a x\n2 b\n3 y\n4 d z\n4 d w\n", 0},
		{"join -v 2 left right", "", "3 y\n", 0},
		{"join -o 1.2,2.2,0 -e - -a 1 left right", "", "a x 1\nb - 2\nd z 4\nd w 4\n", 0},
		{"join -t , -1 2 -2 1 - right", "x,1\n", "", 0},
		{"column -t", "a b\nccc d\n\neeee\n", "a     b\nccc   d\neeee\n", 0},
		{"column -t -s , -o ' | '", "a,b\ncc,d\n", "a  | b\ncc | d\n", 0},
		{"column -c 20", "a\nb\nc\nd\n", "a\tc\nb\td\n", 0},
	})
}

func TestSortGNUForms(t *testing.T) {
	h := newHarness(t)
	h.goldens(t, []golden{
		{"sort -h", "1G\n10K\n2M\n500\n-1K\n", "-1K\n500\n10K\n2M\n1G\n", 0},
		{"sort -V", "v1.10\nv1.2\nv1.9a\nv1.9\n", "v1.2\nv1.9\nv1.9a\nv1.10\n", 0},
		{"sort -V", "a-1.0~rc1\na-1.0\n", "a-1.0~rc1\na-1.0\n", 0},
		{"sort -f", "b\nA\na\nB\n", "A\na\nB\nb\n", 0},
		{"sort -fs", "b\nA\na\nB\n", "A\na\nb\nB\n", 0},
		{"sort -b", "  b\na\n c\n", "a\n  b\n c\n", 0},
		{"sort -s -k 1,1", "b 2\na 1\nb 1\n", "a 1\nb 2\nb 1\n", 0},
		{"sort -k 2,2h", "x 2K\ny 1M\nz 3\n", "z 3\nx 2K\ny 1M\n", 0},
		{"sort -t . -k 2,2Vr", "a.1\nb.10\nc.2\n", "b.10\nc.2\na.1\n", 0},
		{"sort -c", "a\nb\n", "", 0},
	})
	r := h.run("sort -c", "a\nc\nb\n")
	r.expectOutput(t, "disorder", "", 1)
	if r.stderr != "sort: -:3: disorder: b\n" {
		t.Fatalf("stderr %q", r.stderr)
	}
	h.run("sort -cu", "a\na\n").expectOutput(t, "unique disorder", "", 1)
	h.seed("data", "c\na\nb\n")
	h.run("sort -o data data", "").expectOutput(t, "-o onto its input", "", 0)
	if h.read("data") != "a\nb\nc\n" {
		t.Fatalf("data %q", h.read("data"))
	}
	h.run("sort -n -h", "1\n").expectRefusal(t, "two numeric kinds", "COMMAND_OPERAND_INVALID")
}

func TestXargsGNUForms(t *testing.T) {
	cases := []struct {
		source, stdin string
		batches       [][]string
		stderr        string
	}{
		{"xargs -L 2 echo", "a b\nc\nd e\n", [][]string{{"echo", "a", "b", "c"}, {"echo", "d", "e"}}, ""},
		{"xargs -L 1 echo", "a \nb\nc\n", [][]string{{"echo", "a", "b"}, {"echo", "c"}}, ""},
		{"xargs -d , echo", "a b,c\n", [][]string{{"echo", "a b", "c\n"}}, ""},
		{`xargs -d '\n' -n 1 echo`, "a b\nc\n", [][]string{{"echo", "a b"}, {"echo", "c"}}, ""},
		{"xargs -t echo", "a b", [][]string{{"echo", "a", "b"}}, "echo a b\n"},
		{"xargs -P 3 -n 1 echo", "1 2 3 4 5", [][]string{{"echo", "1"}, {"echo", "2"}, {"echo", "3"}, {"echo", "4"}, {"echo", "5"}}, ""},
		{"xargs echo", "", [][]string{{"echo"}}, ""},
		{"xargs -r echo", " \n", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.source, func(t *testing.T) {
			h := newHarness(t)
			log := &batchLog{}
			h.nested = log
			r := h.run(c.source, c.stdin)
			var want strings.Builder
			for _, batch := range c.batches {
				want.WriteString(strings.Join(batch, "|") + "\n")
			}
			r.expectOutput(t, c.source, want.String(), 0)
			got := log.sorted()
			if !reflect.DeepEqual(got, c.batches) || r.stderr != c.stderr {
				t.Fatalf("batches %q stderr %q, want %q %q", got, r.stderr, c.batches, c.stderr)
			}
		})
	}
	h := newHarness(t)
	h.nested = &batchLog{status: map[string]int{"2": 3, "4": 255}}
	h.run("xargs -n 1 echo", "1 2 3 4 5").expectOutput(t, "GNU statuses", "echo|1\necho|2\necho|3\necho|4\n", 124)
	h.nested = &batchLog{status: map[string]int{"2": 3}}
	h.run("xargs -P 2 -n 1 echo", "1 2 3").expectOutput(t, "parallel status", "echo|1\necho|2\necho|3\n", 123)
}

// Spellings that GNU, BSD/macOS and busybox give the same operation are all
// accepted; GNU wins only where one spelling means different things.
func TestEveryDialectSpelling(t *testing.T) {
	h := newHarness(t)
	h.seed("one", "a\nB\nd\n")
	h.seed("two", "b\nc\nD\n")
	h.seed("left", "A 1\nb 2\n")
	h.seed("right", "a x\nB y\n")
	h.seed("prog.awk", "{ print $2 }\n")
	h.goldens(t, []golden{
		{"seq -t '!\\n' 3", "", "1\n2\n3!\n", 0},
		{"column -x -c 20", "a\nb\nc\nd\n", "a\tb\nc\td\n", 0},
		{"comm -i -12 one two", "", "B\nd\n", 0},
		{"join -i left right", "", "A 1 x\nb 2 y\n", 0},
		{"cut -w -f 2", "a  b\tc\nsolo\n", "b\nsolo\n", 0},
		{"cut -w -f 1,3 -s", "x y z\nsolo\n", "x\tz\n", 0},
		{"awk -f prog.awk", "a b\nc d\n", "b\nd\n", 0},
	})
	cases := []struct {
		source, stdin string
		batches       [][]string
	}{
		{"xargs -J % cp % dest", "a b", [][]string{{"cp", "a", "b", "dest"}}},
		{"xargs -E stop echo", "a stop b", [][]string{{"echo", "a"}}},
		{"xargs -estop echo", "a stop b", [][]string{{"echo", "a"}}},
		{"xargs -i echo [{}]", "x\ny\n", [][]string{{"echo", "[x]"}, {"echo", "[y]"}}},
		{"xargs -i@ echo @", "x\n", [][]string{{"echo", "x"}}},
		{"xargs -l echo", "a b\nc\n", [][]string{{"echo", "a", "b"}, {"echo", "c"}}},
		{"xargs -s 6 echo", "a b c", [][]string{{"echo", "a", "b"}, {"echo", "c"}}},
		{"xargs -o echo", "a", [][]string{{"echo", "a"}}},
	}
	for _, c := range cases {
		log := &batchLog{}
		h.nested = log
		var want strings.Builder
		for _, batch := range c.batches {
			want.WriteString(strings.Join(batch, "|") + "\n")
		}
		h.run(c.source, c.stdin).expectOutput(t, c.source, want.String(), 0)
		if !reflect.DeepEqual(log.batches, c.batches) {
			t.Fatalf("%s batches %q, want %q", c.source, log.batches, c.batches)
		}
	}
	h.nested = &batchLog{}
	h.run("xargs -x -n 2 -s 6 echo", "aa bb").expectRefusal(t, "-x size", "COMMAND_LIMIT_EXCEEDED")
}
