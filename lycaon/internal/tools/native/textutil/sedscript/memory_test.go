package sedscript

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/textsearch"
	"github.com/lycaon/lycaon/internal/toolcontract"
)

func admittedMachine(t *testing.T, script string, record, working int64, out io.Writer) (*Machine, *toolcontract.ProgramMemory) {
	t.Helper()
	posix, err := textsearch.NewPOSIXEngine()
	testutil.FailErr(t, "create regex owner", err)
	parseMemory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1})
	testutil.FailErr(t, "create parse admission", err)
	program, err := Parse(t.Context(), parseMemory, script, false, engine{posix})
	testutil.FailErr(t, "parse script", err)
	memory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: working, State: 1})
	testutil.FailErr(t, "create actual admission", err)
	var releases []func()
	reserve := func(n int64) error {
		release, err := memory.Reserve(t.Context(), n)
		if err != nil {
			return err
		}
		releases = append(releases, release)
		return nil
	}
	t.Cleanup(func() {
		for _, release := range releases {
			release()
		}
		program.Close()
		if parseMemory.InUse() != (toolcontract.ProgramMemoryUsage{}) || memory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatal("sed storage admission leaked")
		}
	})
	m, err := NewMachine(t.Context(), program, Config{Quiet: true, Delimiter: '\n', SpaceLimit: record, StepLimit: 100000, Reserve: reserve, Stdout: out})
	testutil.FailErr(t, "bind admitted machine", err)
	return m, memory
}

func TestTransformRefusalPreservesPatternAndPriorOutput(t *testing.T) {
	for _, tc := range []struct {
		name, script, input string
		record, working     int64
		wantLimit           string
		maximum, observed   int64
	}{
		{"record expansion", `p;s/\(.*\)/\1\1\1\1\1\1/`, "aaaaaaaa\n", 8, 128, "record_bytes", 8, 16},
		{"working expansion", `p;s/a/aa/g`, "aaaa\n", 32, 8, "working", 8, 10},
		{"translation", "p;y/a/😀/", "a\n", 32, 2, "working", 2, 5},
		{"next record", "p;N", "aa\nbb\n", 4, 32, "record_bytes", 4, 5},
		{"hold join", "p;h;G", "aa\n", 4, 32, "record_bytes", 4, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			m, memory := admittedMachine(t, tc.script, tc.record, tc.working, &out)
			_, err := m.Stream(&lines{text: tc.input, delimiter: '\n'}, nil)
			var record *LimitError
			var budget *toolcontract.ProgramMemoryLimitError
			if tc.wantLimit == "record_bytes" {
				if !errors.As(err, &record) || record.Resource != tc.wantLimit || record.Maximum != tc.maximum || record.Observed != tc.observed {
					t.Fatalf("record refusal: %+v", err)
				}
			} else if !errors.As(err, &budget) || budget.Account != tc.wantLimit || budget.Maximum != tc.maximum || budget.Observed != tc.observed {
				t.Fatalf("working refusal: %+v", err)
			}
			first := strings.Split(tc.input, "\n")[0]
			if string(m.space.pattern) != first || out.String() != first+"\n" || int64(cap(m.space.pattern)) > tc.record || int64(cap(m.space.scratch)) > tc.record || memory.InUse().Working > tc.working {
				t.Fatalf("refused transform committed or allocated refused growth: pattern=%q out=%q capacities=%d/%d usage=%+v", m.space.pattern, out.String(), cap(m.space.pattern), cap(m.space.scratch), memory.InUse())
			}
		})
	}
}

func TestRepeatedTransformsReuseAdmittedBacking(t *testing.T) {
	var out bytes.Buffer
	m, memory := admittedMachine(t, "s/a/b/g;h;G;y/b/B/;s/B/a/g\na tail", 32, 256, &out)
	for range 10000 {
		_, err := m.Stream(&lines{text: "aaaa\n", delimiter: '\n'}, nil)
		testutil.FailErr(t, "stream repeated transform", err)
	}
	// Only backing growth is charged. Ten thousand cycles retain the same
	// three spaces, rather than reserving every temporary result ever made.
	if out.String() != strings.Repeat("tail\n", 10000) || string(m.space.pattern) != "aaaa\naaaa" || memory.InUse().Working > 3*3*32 || memory.InUse().State != 0 || cap(m.space.pattern) > 32 || cap(m.space.hold) > 32 || cap(m.space.scratch) > 32 {
		t.Fatalf("cumulative transform storage: %+v spaces=%d/%d/%d", memory.InUse(), cap(m.space.pattern), cap(m.space.hold), cap(m.space.scratch))
	}
}

func TestAppendQueueAdmissionStopsBranchBeforeGrowth(t *testing.T) {
	m, memory := admittedMachine(t, "p\n:again\na text\nb again", 32, 512, io.Discard)
	_, err := m.Stream(&lines{text: "a\n", delimiter: '\n'}, nil)
	var budget *toolcontract.ProgramMemoryLimitError
	if !errors.As(err, &budget) || budget.Account != "working" || budget.Maximum != 512 || budget.Observed <= 512 || len(m.appends) != 4 || cap(m.appends) != 4 || memory.InUse().Working != 449 {
		t.Fatalf("append queue admission: %v len/cap=%d/%d usage=%+v", err, len(m.appends), cap(m.appends), memory.InUse())
	}
}

func TestListingUsesOneBoundedAdmittedBuffer(t *testing.T) {
	var out bytes.Buffer
	m, memory := admittedMachine(t, "l 0", 8192, 32768, &out)
	input := strings.Repeat("\x00", 8192) + "\n"
	for range 2 {
		_, err := m.Stream(&lines{text: input, delimiter: '\n'}, nil)
		testutil.FailErr(t, "list escaped record", err)
	}
	want := strings.Repeat("\\000", 8192) + "$\n"
	if out.String() != want+want || memory.InUse().Working != 8192+4096 || cap(m.listing) != 4096 {
		t.Fatalf("listing bytes=%d usage=%+v listing=%d", out.Len(), memory.InUse(), cap(m.listing))
	}
}

func TestParseAdmissionChargesAndReleasesEveryScriptStructure(t *testing.T) {
	posix, err := textsearch.NewPOSIXEngine()
	testutil.FailErr(t, "create regex owner", err)
	if _, err := Parse(t.Context(), nil, "p", false, engine{posix}); err == nil {
		t.Fatal("expected nil memory to refuse")
	}

	richScript := strings.Join([]string{
		":loop",
		"1,10{",
		`/alpha/,/omega/s/\(foo\)\(bar\)/\U\2\E-\1-literal/gw out1.txt`,
		"y/abcdef/ABCDEF/",
		"a\\nappended line with escaped \\\\ and text",
		"r read1.txt",
		"w out2.txt",
		"t loop",
		"}",
	}, "\n")

	fullMemory, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: 1 << 20, State: 1})
	testutil.FailErr(t, "create full memory", err)
	program, err := Parse(t.Context(), fullMemory, richScript, false, engine{posix})
	testutil.FailErr(t, "parse rich script", err)
	held := fullMemory.InUse().Working
	if held <= parserBaseBytes {
		t.Fatalf("expected admitted script structures to hold working bytes, got %d", held)
	}
	program.Close()
	program.Close()
	if fullMemory.InUse() != (toolcontract.ProgramMemoryUsage{}) {
		t.Fatalf("closed program retained memory: %+v", fullMemory.InUse())
	}

	// Any ceiling below the required working bytes must fail with a typed
	// ProgramMemoryLimitError and release all partial leases before returning.
	for _, ceiling := range []int64{1, parserBaseBytes, parserBaseBytes + commandStructBytes, held / 2, held - 1} {
		tight, err := toolcontract.NewProgramMemory(toolcontract.ProgramMemoryLimits{Working: ceiling, State: 1})
		testutil.FailErr(t, "create tight memory", err)
		prog, err := Parse(t.Context(), tight, richScript, false, engine{posix})
		var limit *toolcontract.ProgramMemoryLimitError
		if prog != nil || !errors.As(err, &limit) || limit.Account != "working" || limit.Maximum != ceiling || limit.Observed <= ceiling {
			t.Fatalf("ceiling %d: prog=%v err=%v", ceiling, prog, err)
		}
		if tight.InUse() != (toolcontract.ProgramMemoryUsage{}) {
			t.Fatalf("ceiling %d leaked %+v after refusal", ceiling, tight.InUse())
		}
	}
}
