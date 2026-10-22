package commandprogram

import (
	"bytes"
	"os"
	"slices"
	"testing"
)

func TestEvaluateFileDescriptorDuplicationAndOrdering(t *testing.T) {
	cases := []struct {
		name    string
		program string
		stdout  string
		stderr  string
		status  int
	}{
		{"trailing >&2", "echo err >&2", "", "err\n", 0},
		{"trailing 1>&2", "echo err 1>&2", "", "err\n", 0},
		{"leading >&2", ">&2 echo err", "", "err\n", 0},
		{"middle >&2", "echo >&2 err", "", "err\n", 0},
		{">&2 bypasses pipe", "echo err >&2 | cat", "", "err\n", 0},
		{"2>&1 into pipe", "echo err 2>&1 1>&2 | cat", "err\n", "", 0},
		{"swap stdout and stderr via fd 3", "{ echo out; echo err >&2; } 3>&1 1>&2 2>&3", "err\n", "out\n", 0},
		{"duplicate input fd 3<&0 and 0<&3", "cat 3< input.txt 0<&3", "from file\n", "", 0},
		{"unopened descriptor fails ordinary status 1", "echo hi 1>&9; echo status=$?", "status=1\n", "", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newEvalFixture(t)
			var stderr bytes.Buffer
			f.ports.Streams.Error = &stderr
			out, err := f.run(t, t.Context(), c.program)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if f.stdout.String() != c.stdout || stderr.String() != c.stderr || out.Status != c.status {
				t.Fatalf("stdout=%q stderr=%q status=%d, want stdout=%q stderr=%q status=%d", f.stdout.String(), stderr.String(), out.Status, c.stdout, c.stderr, c.status)
			}
		})
	}
}

func TestEvaluateArithmeticCommandsAndLoops(t *testing.T) {
	f := newEvalFixture(t)
	program := `
((1 + 2))
echo s1=$?
((1 - 1))
echo s2=$?
((i = 0))
while ((i < 3)); do
	echo iter=$i
	((i++))
done
((a = 2, b = ++a * 3, b += 1))
echo a=$a b=$b
`
	out, err := f.run(t, t.Context(), program)
	if err != nil || out.Status != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	want := []string{"echo s1=0", "echo s2=1", "echo iter=0", "echo iter=1", "echo iter=2", "echo a=3 b=10"}
	if got := f.log.argv(); !slices.Equal(got, want) {
		t.Fatalf("dispatched %q, want %q", got, want)
	}
}

func TestEvaluateIndexedArraysAndParameterSlices(t *testing.T) {
	f := newEvalFixture(t)
	program := `
arr=(alpha "beta gamma" delta)
arr+=(epsilon)
arr[1]="BETA GAMMA"
echo count=${#arr[@]} len1=${#arr[1]} first=${arr[0]}
for item in "${arr[@]}"; do
	echo "item:$item"
done
echo "star:${arr[*]}"
echo "slice:${arr[@]:1:2}"
word=abcdef
echo "str:${word:1:3}:${word: -2}:${word:1:-1}"
s="a/b/c"
echo " ops:${s//\//-}:${s#*/}:${s%/*}"
f() {
	echo "pos:${@:2:2}"
}
f one two three four
`
	out, err := f.run(t, t.Context(), program)
	if err != nil || out.Status != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	want := []string{
		"echo count=4 len1=10 first=alpha",
		"echo item:alpha",
		"echo item:BETA GAMMA",
		"echo item:delta",
		"echo item:epsilon",
		"echo star:alpha BETA GAMMA delta epsilon",
		"echo slice:BETA GAMMA delta",
		"echo str:bcd:ef:bcde",
		"echo  ops:a-b-c:b/c:a/b",
		"echo pos:two three",
	}
	if got := f.log.argv(); !slices.Equal(got, want) {
		t.Fatalf("dispatched %q, want %q", got, want)
	}
}

func TestEvaluateTrapExitRunsOnceAndPreservesStatus(t *testing.T) {
	t.Run("normal and explicit exit", func(t *testing.T) {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), `trap 'echo cleanup $?' EXIT; echo body; ( echo sub ); exit 7; echo never`)
		if err != nil || out.Status != 7 || !out.Exited {
			t.Fatalf("out=%+v err=%v", out, err)
		}
		want := []string{"echo body", "echo sub", "echo cleanup 7"}
		if got := f.log.argv(); !slices.Equal(got, want) {
			t.Fatalf("dispatched %q, want %q", got, want)
		}
	})
	t.Run("errexit triggers exit trap", func(t *testing.T) {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), `set -e; trap 'echo trapped $?' 0; echo start; grep z; echo never`)
		if err != nil || out.Status != 1 || !out.Exited {
			t.Fatalf("out=%+v err=%v", out, err)
		}
		want := []string{"echo start", "grep z", "echo trapped 1"}
		if got := f.log.argv(); !slices.Equal(got, want) {
			t.Fatalf("dispatched %q, want %q", got, want)
		}
	})
	t.Run("trap dash clears exit trap", func(t *testing.T) {
		f := newEvalFixture(t)
		out, err := f.run(t, t.Context(), `trap 'echo cleanup' EXIT; trap - EXIT; echo done`)
		if err != nil || out.Status != 0 {
			t.Fatalf("out=%+v err=%v", out, err)
		}
		if got := f.log.argv(); !slices.Equal(got, []string{"echo done"}) {
			t.Fatalf("dispatched %q", got)
		}
	})
}

func TestEvaluateProcessSubstitutionRecordsChildrenAndCleansScratch(t *testing.T) {
	f := newEvalFixture(t)
	scratch := t.TempDir()
	f.ports.ScratchDir = scratch
	out, err := f.run(t, t.Context(), `echo <(echo left)`)
	if err != nil || out.Status != 0 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	foundProcSubst := false
	for _, child := range out.Children {
		if child.Command == "<(" && child.Executed && child.Result.Status == 0 {
			foundProcSubst = true
		}
	}
	if !foundProcSubst {
		t.Fatalf("missing <( child receipt in %+v", out.Children)
	}
	entries, err := os.ReadDir(scratch)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch files not cleaned up: %v %v", entries, err)
	}
}
