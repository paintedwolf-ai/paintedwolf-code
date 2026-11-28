package guidance

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

func TestOffenderIdentityDistinguishesHandlesOnSharedPath(t *testing.T) {
	t.Parallel()

	type coordCase struct {
		name      string
		line      int
		endLine   int
		wantCoord string
	}
	coords := []coordCase{
		{name: "range", line: 10, endLine: 20, wantCoord: ":10-20"},
		{name: "single_line", line: 10, endLine: 0, wantCoord: ":10"},
		{name: "no_line", line: 0, endLine: 0, wantCoord: ""},
	}

	type subjectCase struct {
		name       string
		handle     string
		wantPrefix string
	}
	subjects := []subjectCase{
		{name: "worker_1", handle: "w1:read#1", wantPrefix: "w1:read#1 src/app.go"},
		{name: "worker_2", handle: "w2:read#1", wantPrefix: "w2:read#1 src/app.go"},
		{name: "unprefixed", handle: "read#1", wantPrefix: "read#1 src/app.go"},
		{name: "path_only", handle: "", wantPrefix: "src/app.go"},
	}

	for _, coord := range coords {
		t.Run(coord.name, func(t *testing.T) {
			t.Parallel()
			seenOffenders := make(map[string]string, len(subjects))
			seenKeys := make(map[string]string, len(subjects))

			for _, subj := range subjects {
				res := evidence.Resolution{
					Handle:  subj.handle,
					Path:    "src/app.go",
					Line:    coord.line,
					EndLine: coord.endLine,
				}
				finding := WorkerFindingInput{
					Evidence: subj.handle,
					Path:     "src/app.go",
					Line:     coord.line,
					EndLine:  coord.endLine,
				}
				offender := FormatResolutionOffender(res, finding)
				wantOffender := subj.wantPrefix + coord.wantCoord
				if offender != wantOffender {
					t.Errorf("%s: FormatResolutionOffender = %q, want %q", subj.name, offender, wantOffender)
				}
				if prev, dup := seenOffenders[offender]; dup {
					t.Errorf("offender collision %q between %s and %s", offender, prev, subj.name)
				}
				seenOffenders[offender] = subj.name

				key := GroundingOffenderKey(InvestCitationUnverifiableCode, OffenderHintData([]string{offender}))
				if prev, dup := seenKeys[key]; dup {
					t.Errorf("key collision %q between %s and %s", key, prev, subj.name)
				}
				seenKeys[key] = subj.name
			}
		})
	}
}

func TestOffenderIdentityHandleOnlyEmptyPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		res     evidence.Resolution
		finding WorkerFindingInput
		want    string
	}{
		{
			name:    "worker_1_range",
			res:     evidence.Resolution{Handle: "w1:read#2", Line: 5, EndLine: 9},
			finding: WorkerFindingInput{Evidence: "w1:read#2", Line: 5, EndLine: 9},
			want:    "w1:read#2:5-9",
		},
		{
			name:    "worker_2_range",
			res:     evidence.Resolution{Handle: "w2:read#2", Line: 5, EndLine: 9},
			finding: WorkerFindingInput{Evidence: "w2:read#2", Line: 5, EndLine: 9},
			want:    "w2:read#2:5-9",
		},
		{
			name:    "worker_1_single_line",
			res:     evidence.Resolution{Handle: "w1:read#2", Line: 5},
			finding: WorkerFindingInput{Evidence: "w1:read#2", Line: 5},
			want:    "w1:read#2:5",
		},
		{
			name:    "worker_1_bare_handle",
			res:     evidence.Resolution{Handle: "w1:read#2"},
			finding: WorkerFindingInput{Evidence: "w1:read#2"},
			want:    "w1:read#2",
		},
		{
			name:    "fallback_from_finding_handle_on_range",
			res:     evidence.Resolution{Line: 3, EndLine: 3},
			finding: WorkerFindingInput{Evidence: "w1:read#1", Line: 3},
			want:    "w1:read#1:3",
		},
	}

	seenKeys := make(map[string]string, len(cases))
	for _, tc := range cases {
		offender := FormatResolutionOffender(tc.res, tc.finding)
		if offender != tc.want {
			t.Errorf("%s: FormatResolutionOffender = %q, want %q", tc.name, offender, tc.want)
		}
		key := GroundingOffenderKey(InvestCitationUnverifiableCode, OffenderHintData([]string{offender}))
		if prev, dup := seenKeys[key]; dup {
			t.Errorf("key collision %q between %s and %s", key, prev, tc.name)
		}
		seenKeys[key] = tc.name
	}
}

func TestOffenderIdentityIdenticalTupleProducesIdenticalKey(t *testing.T) {
	t.Parallel()

	res := evidence.Resolution{Handle: "w1:read#1", Path: "src/app.go", Line: 10, EndLine: 20}
	finding := WorkerFindingInput{Evidence: "w1:read#1", Path: "src/app.go", Line: 10, EndLine: 20}

	offA := FormatResolutionOffender(res, finding)
	offB := FormatResolutionOffender(res, finding)
	if offA != offB {
		t.Fatalf("identical tuple produced different offenders: %q vs %q", offA, offB)
	}
	keyA := GroundingOffenderKey(InvestCitationUnverifiableCode, OffenderHintData([]string{offA}))
	keyB := GroundingOffenderKey(InvestCitationUnverifiableCode, OffenderHintData([]string{offB}))
	if keyA != keyB {
		t.Fatalf("identical tuple produced different keys: %q vs %q", keyA, keyB)
	}
}

func TestOffenderIdentityPathOnlyIgnoresResolvedHandle(t *testing.T) {
	t.Parallel()

	res := evidence.Resolution{Handle: "read#1", Path: "src/app.go", Line: 10, EndLine: 20}
	finding := WorkerFindingInput{Path: "src/app.go", Line: 10, EndLine: 20}
	if got := FormatResolutionOffender(res, finding); got != "src/app.go:10-20" {
		t.Fatalf("path-only citation = %q, want %q", got, "src/app.go:10-20")
	}
}
