//go:build darwin || linux

package textutil

import (
	"os/exec"
	"strings"
	"testing"
)

func TestAwkGolden(t *testing.T) {
	newHarness(t).goldens(t, []golden{
		{"awk '{print $2}'", "a b\nc d\n", "b\nd\n", 0},
		{"awk -F : '{print $NF, NR}'", "a:b:c\nx\n", "c 1\nx 2\n", 0},
		{"awk -v n=2 'NR==n'", "a\nb\nc\n", "b\n", 0},
		{"awk 'BEGIN{print \"start\"} END{print NR}'", "a\nb\n", "start\n2\n", 0},
		{"awk '/start/,/end/'", "x\nstart\na\nend\ny\n", "start\na\nend\n", 0},
		{`awk '{gsub(/a+/, "<&>"); print}'`, "baaab aa\n", "b<aaa>b <aa>\n", 0},
		{`awk '{n=split($0, parts, ","); print n, parts[2], toupper(parts[1]), length($0)}'`, "ab,cd,e\n", "3 cd AB 7\n", 0},
		{`awk '{printf "%-4s|%3d|%.2f\n", $1, $2, $3}'`, "ab 7 3.14159\n", "ab  |  7|3.14\n", 0},
		{`awk 'match($0, /x*y+/) {print RSTART, RLENGTH}'`, "aaxxyyb\n", "3 4\n", 0},
		{`awk '{sub(/b/, "B"); print}'`, "abcb\n", "aBcb\n", 0},
		{`awk 'BEGIN {exit 3}'`, "", "", 3},
		{`awk 'length($0) > 1'`, "é\nx\n", "é\n", 0},
	})
}

func TestAwkReadsPinnedFilesByName(t *testing.T) {
	h := newHarness(t)
	h.seed("one", "a\nb\n")
	h.seed("two", "c\n")
	r := h.run(`awk '{print FILENAME ":" FNR ":" $0}' one ./two`, "")
	r.expectOutput(t, "files", "one:1:a\none:2:b\n./two:1:c\n", 0)
	if len(h.pins.executions[r.execution]) != 2 {
		t.Fatalf("pins %+v", h.pins.executions[r.execution])
	}
	missing := h.run(`awk '{print}' missing one`, "")
	missing.expectOutput(t, "missing file", "a\nb\n", 2)
}

func TestAwkRefusesUnsafeAndRE2OnlyConstructsBeforeReading(t *testing.T) {
	h := newHarness(t)
	for _, source := range []string{
		`awk 'BEGIN{system("id")}'`,
		`awk '{print | "sort"}'`,
		`awk 'BEGIN{"date" | getline x}'`,
		`awk 'BEGIN{getline x < "/etc/passwd"}'`,
		`awk '{print > "out"}'`,
		`awk '{printf "%s", $0 >> "out"}'`,
		`awk 'function f() { system("x") } {f()}'`,
		`awk '/\d+/'`,
		`awk '/(?i)a/'`,
		`awk '/a+?/'`,
	} {
		h.run(source, "never read\n").expectRefusal(t, source, "COMMAND_NATIVE_FORM_UNSUPPORTED")
	}
	for _, source := range []string{`awk '{print'`, `awk -v 1x=2 '{}'`, `awk -v novalue '{}'`, "awk"} {
		h.run(source, "").expectRefusal(t, source, "COMMAND_OPERAND_INVALID")
	}
	r := h.run(`awk '{print 1/0}'`, "a\n")
	r.expectOutput(t, "runtime error", "", 2)
	if !strings.Contains(r.stderr, "division by zero") {
		t.Fatalf("stderr %q", r.stderr)
	}
	if r := h.run(`awk 'BEGIN{print ENVIRON["HOME"] "|"}'`, ""); r.stdout != "|\n" {
		t.Fatalf("ENVIRON leaked %q", r.stdout)
	}
}

func TestAwkWorkAndAllocationBudgets(t *testing.T) {
	h := newHarness(t)
	h.owners.limits["query_work_steps"] = 100
	h.run(`awk 'BEGIN{for(i=0;i<99;i++);}'`, "").expectOutput(t, "steps within limit", "", 0)
	reject := h.run(`awk 'BEGIN{while(1);}'`, "").expectRefusal(t, "unbounded loop", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "query_work_steps" {
		t.Fatalf("limit %+v", reject.Data)
	}
	h.owners.limits["query_work_steps"] = 1 << 20
	h.owners.limits["evaluation_memory_bytes"] = 1024
	reject = h.run(`awk 'BEGIN{s="x"; for(i=0;i<20;i++) s = s s; print length(s)}'`, "").expectRefusal(t, "string growth", "COMMAND_LIMIT_EXCEEDED")
	if reject.Data["limit_id"] != "evaluation_memory_bytes" {
		t.Fatalf("limit %+v", reject.Data)
	}
	h.run(`awk 'BEGIN{printf "%1000000000d", 1}'`, "").expectRefusal(t, "wide printf", "COMMAND_LIMIT_EXCEEDED")
}

// The host's awk (BSD awk or GNU awk) agrees on these programs under LC_ALL=C.
func TestAwkDifferentialAgainstHost(t *testing.T) {
	h := newHarness(t)
	input := "alpha 3 x:y\nbeta 10 p:q\ngamma 7 r:s\n"
	for _, program := range []string{
		`{print $1}`,
		`$2 > 5 {print $1, $2 * 2}`,
		`{sum += $2} END {print sum, NR}`,
		`{split($3, a, ":"); print a[2] a[1]}`,
		`/a$/ {print NR": "$0}`,
		`{gsub(/a/, "A"); print}`,
		`{print length($1), substr($1, 2, 3), index($1, "m")}`,
		`{printf "%5s|%-3d|\n", $1, $2}`,
		`NR==2, NR==3`,
	} {
		cmd := exec.Command(hostTool(t, "awk"), program)
		cmd.Env = []string{"LC_ALL=C", "PATH=/usr/bin:/bin"}
		cmd.Stdin = strings.NewReader(input)
		want, err := cmd.Output()
		if err != nil {
			t.Fatalf("host awk %s: %v", program, err)
		}
		h.run("awk '"+program+"'", input).expectOutput(t, program, string(want), 0)
	}
}
