package guidance_test

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
)

// TestOffenderIdentityInjectiveInvariant proves GB10 I4 (first case GB05-F2):
// distinct citations produce distinct offender keys, and every key names its
// handle or path and its coordinate.
func TestOffenderIdentityInjectiveInvariant(t *testing.T) {
	t.Parallel()

	type citationForm struct {
		name    string
		res     evidence.Resolution
		finding guidance.WorkerFindingInput
		// wantSubject is the handle (when supplied) or path that must appear in the key.
		wantSubject string
		// wantCoord is the coordinate substring that must appear in the key when non-empty.
		wantCoord string
	}

	// First cases (GB05-F2): distinct worker-prefixed handles and plain handles
	// citing the same path and line range must never collide onto one offender key.
	forms := []citationForm{
		{
			name:        "worker_1_ranged_on_shared_path",
			res:         evidence.Resolution{Handle: "w1:read#1", Path: "src/app.go", Line: 10, EndLine: 20},
			finding:     guidance.WorkerFindingInput{Evidence: "w1:read#1", Path: "src/app.go", Line: 10, EndLine: 20},
			wantSubject: "w1:read#1",
			wantCoord:   "10-20",
		},
		{
			name:        "worker_2_ranged_on_shared_path",
			res:         evidence.Resolution{Handle: "w2:read#1", Path: "src/app.go", Line: 10, EndLine: 20},
			finding:     guidance.WorkerFindingInput{Evidence: "w2:read#1", Path: "src/app.go", Line: 10, EndLine: 20},
			wantSubject: "w2:read#1",
			wantCoord:   "10-20",
		},
		{
			name:        "unprefixed_handle_ranged_on_shared_path",
			res:         evidence.Resolution{Handle: "read#1", Path: "src/app.go", Line: 10, EndLine: 20},
			finding:     guidance.WorkerFindingInput{Evidence: "read#1", Path: "src/app.go", Line: 10, EndLine: 20},
			wantSubject: "read#1",
			wantCoord:   "10-20",
		},
		{
			name:        "path_only_ranged_on_shared_path",
			res:         evidence.Resolution{Path: "src/app.go", Line: 10, EndLine: 20},
			finding:     guidance.WorkerFindingInput{Path: "src/app.go", Line: 10, EndLine: 20},
			wantSubject: "src/app.go",
			wantCoord:   "10-20",
		},
		{
			name:        "worker_1_single_line_on_shared_path",
			res:         evidence.Resolution{Handle: "w1:read#1", Path: "src/app.go", Line: 10},
			finding:     guidance.WorkerFindingInput{Evidence: "w1:read#1", Path: "src/app.go", Line: 10},
			wantSubject: "w1:read#1",
			wantCoord:   ":10",
		},
		{
			name:        "worker_2_single_line_on_shared_path",
			res:         evidence.Resolution{Handle: "w2:read#1", Path: "src/app.go", Line: 10},
			finding:     guidance.WorkerFindingInput{Evidence: "w2:read#1", Path: "src/app.go", Line: 10},
			wantSubject: "w2:read#1",
			wantCoord:   ":10",
		},
		{
			name:        "worker_1_handle_only_ranged",
			res:         evidence.Resolution{Handle: "w1:read#2", Line: 5, EndLine: 9},
			finding:     guidance.WorkerFindingInput{Evidence: "w1:read#2", Line: 5, EndLine: 9},
			wantSubject: "w1:read#2",
			wantCoord:   "5-9",
		},
		{
			name:        "worker_2_handle_only_ranged",
			res:         evidence.Resolution{Handle: "w2:read#2", Line: 5, EndLine: 9},
			finding:     guidance.WorkerFindingInput{Evidence: "w2:read#2", Line: 5, EndLine: 9},
			wantSubject: "w2:read#2",
			wantCoord:   "5-9",
		},
	}

	// Deterministic generated forms spanning worker-prefixed handles, paths,
	// single lines, ranges, and excerpts.
	rng := rand.New(rand.NewPCG(20261127, 0x14))
	for i := range 20 {
		worker := fmt.Sprintf("leg-%d", i%4)
		handle := fmt.Sprintf("%s:read#%d", worker, 1+i)
		path := fmt.Sprintf("internal/gen/f_%d.go", i%5)
		start := 1 + rng.IntN(80)
		end := start + 1 + rng.IntN(15)
		excerpt := fmt.Sprintf("tok_%d", i)

		forms = append(forms,
			citationForm{
				name:        fmt.Sprintf("gen_%d_handle_path_range", i),
				res:         evidence.Resolution{Handle: handle, Path: path, Line: start, EndLine: end},
				finding:     guidance.WorkerFindingInput{Evidence: handle, Path: path, Line: start, EndLine: end},
				wantSubject: handle,
				wantCoord:   fmt.Sprintf("%d-%d", start, end),
			},
			citationForm{
				name:        fmt.Sprintf("gen_%d_handle_path_line_excerpt", i),
				res:         evidence.Resolution{Handle: handle, Path: path, Line: start, Excerpt: excerpt},
				finding:     guidance.WorkerFindingInput{Evidence: handle, Path: path, Line: start, Excerpt: excerpt},
				wantSubject: handle,
				wantCoord:   ":" + strconv.Itoa(start),
			},
			citationForm{
				name:        fmt.Sprintf("gen_%d_path_range_only", i),
				res:         evidence.Resolution{Path: path, Line: start, EndLine: end + i + 1},
				finding:     guidance.WorkerFindingInput{Path: path, Line: start, EndLine: end + i + 1},
				wantSubject: path,
				wantCoord:   fmt.Sprintf("%d-%d", start, end+i+1),
			},
		)
	}

	seenKeys := make(map[string]string, len(forms))
	for _, f := range forms {
		offender := guidance.FormatResolutionOffender(f.res, f.finding)
		key := guidance.GroundingOffenderKey(
			guidance.InvestCitationUnverifiableCode,
			guidance.OffenderHintData([]string{offender}),
		)
		if !strings.Contains(key, f.wantSubject) {
			t.Errorf("%s: key %q does not name subject %q (offender=%q)", f.name, key, f.wantSubject, offender)
		}
		if f.wantCoord != "" && !strings.Contains(key, f.wantCoord) {
			t.Errorf("%s: key %q does not name coordinate %q (offender=%q)", f.name, key, f.wantCoord, offender)
		}
		if prev, dup := seenKeys[key]; dup {
			t.Errorf("non-injective offender key %q: collision between %s and %s", key, prev, f.name)
		} else {
			seenKeys[key] = f.name
		}
	}
}
