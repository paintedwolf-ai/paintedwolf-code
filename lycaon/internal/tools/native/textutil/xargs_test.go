//go:build darwin || linux

package textutil

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/toolcontract"
)

func TestXargsBatchesAreLiteralChildren(t *testing.T) {
	cases := []struct {
		source, stdin string
		batches       [][]string
	}{
		{"xargs", "a b\nc\n", [][]string{{"echo", "a", "b", "c"}}},
		{"xargs printf %s", "x  y\t z", [][]string{{"printf", "%s", "x", "y", "z"}}},
		{"xargs -n 2 echo", "1 2 3 4 5", [][]string{{"echo", "1", "2"}, {"echo", "3", "4"}, {"echo", "5"}}},
		{"xargs echo", `'a b' "c 'd'" e\ f g\\h`, [][]string{{"echo", "a b", "c 'd'", "e f", `g\h`}}},
		{"xargs -0 echo", "a b\x00c;d\x00'q\"\x00\x00\n\x00", [][]string{{"echo", "a b", "c;d", `'q"`, "", "\n"}}},
		{"xargs -0 -n 1 echo", "x\x00y", [][]string{{"echo", "x"}, {"echo", "y"}}},
		{"xargs -I {} echo pre{}post {}", "one\n  two words\n\n", [][]string{{"echo", "preonepost", "one"}, {"echo", "pretwo wordspost", "two words"}}},
		{"xargs -0 -I @ echo @", "a\nb\x00c", [][]string{{"echo", "a\nb"}, {"echo", "c"}}},
		{"xargs -- echo -n", "$(id) ; | > x", [][]string{{"echo", "-n", "$(id)", ";", "|", ">", "x"}}},
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
			if !reflect.DeepEqual(log.batches, c.batches) {
				t.Fatalf("batches %q, want %q", log.batches, c.batches)
			}
		})
	}
}

func TestXargsEmptyInputRunsNothingWithRunIfNonEmptyOrReplace(t *testing.T) {
	for _, c := range []struct{ source, stdin string }{
		{"xargs -r echo", ""}, {"xargs -r echo", " \n\t\n"}, {"xargs -0r echo", ""}, {"xargs -I x echo x", "\n\n"},
	} {
		h := newHarness(t)
		log := &batchLog{}
		h.nested = log
		h.run(c.source, c.stdin).expectOutput(t, c.source, "", 0)
		if len(log.batches) != 0 {
			t.Fatalf("%s %q created child executions %v", c.source, c.stdin, log.batches)
		}
	}
	h := newHarness(t)
	h.run("xargs -r echo", "").expectOutput(t, "empty input without a runtime", "", 0)
}

func TestXargsBatchStatusesAndHostStops(t *testing.T) {
	h := newHarness(t)
	log := &batchLog{status: map[string]int{"2": 3}}
	h.nested = log
	h.run("xargs -n 1 echo", "1 2 3").expectOutput(t, "ordinary failure continues", "echo|1\necho|2\necho|3\n", 123)
	log = &batchLog{stop: "2"}
	h.nested = log
	r := h.run("xargs -n 1 echo", "1 2 3")
	if r.result.Terminal != toolcontract.ProducerRefused || r.result.StopCode != "PERMISSION_DENIED" || r.err == nil {
		t.Fatalf("host stop: %+v %v", r.result, r.err)
	}
	if len(log.batches) != 2 || r.stdout != "echo|1\n" {
		t.Fatalf("batches after a host stop %v stdout %q", log.batches, r.stdout)
	}
}

func TestXargsRefusals(t *testing.T) {
	h := newHarness(t)
	h.nested = &batchLog{}
	for _, c := range []struct{ source, stdin, code string }{
		{"xargs -n 0 echo", "a", "COMMAND_OPERAND_INVALID"},
		{"xargs -n x echo", "a", "COMMAND_OPERAND_INVALID"},
		{"xargs -n 1 -I {} echo {}", "a", "COMMAND_OPERAND_INVALID"},
		{"xargs -I '' echo", "a", "COMMAND_OPERAND_INVALID"},
		{"xargs echo", "'unterminated", "COMMAND_OPERAND_INVALID"},
		{"xargs echo", "\"split\nline\"", "COMMAND_OPERAND_INVALID"},
		{"xargs -P x echo", "a", "COMMAND_OPERAND_INVALID"},
		{"xargs -n 1 -L 1 echo", "a", "COMMAND_OPERAND_INVALID"},
		{"xargs -d ab echo", "a", "COMMAND_OPERAND_INVALID"},
	} {
		h.run(c.source, c.stdin).expectRefusal(t, c.source, c.code)
	}
	if len(h.nested.(*batchLog).batches) != 0 {
		t.Fatal("a refused xargs ran a batch")
	}
	h.nested = nil
	h.run("xargs echo", "a").expectRefusal(t, "no nested runtime", "COMMAND_NATIVE_FORM_UNSUPPORTED")
}

func TestXargsArgumentLimits(t *testing.T) {
	h := newHarness(t)
	log := &batchLog{}
	h.nested = log
	limit := int(h.owners.limits["argument_bytes"])
	at, over := strings.Repeat("a", limit), strings.Repeat("a", limit+1)
	h.run("xargs -0 true", at).expectOutput(t, "argument at limit", "true|"+at+"\n", 0)
	reject := h.run("xargs -0 true", over).expectRefusal(t, "argument over limit", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "argument_bytes" {
		t.Fatalf("limit %+v", reject.Data)
	}
	h.owners.limits["argv_bytes"] = 10
	log.batches = nil
	h.run("xargs -0 e", "abc\x00def\x00ghi\x00j").expectOutput(t, "argv split", "e|abc|def|ghi\ne|j\n", 0)
	h.owners.limits["child_executions"] = 1
	h.run("xargs -n 1 e", "a b").expectRefusal(t, "child executions", "COMMAND_LIMIT_EXCEEDED")
}
