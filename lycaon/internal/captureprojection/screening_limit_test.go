package captureprojection

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestScreeningLimitClassifiesDetectorWorkBudget(t *testing.T) {
	matcher, err := secretmatch.BuildMatcher(secretmatch.Bundled())
	testutil.FailErr(t, "build bundled matcher", err)
	projector := New(matcher, nil)
	memory := &streamMemoryFixture{}
	options := streamOptionsFixture(memory)
	options.Bounds.DetectorWork = 1
	var out bytes.Buffer
	metadata, err := projector.ScreenStream(t.Context(), Scope{}, "", strings.NewReader("ordinary command output\n"), &out, options)
	if err == nil || metadata.Complete || out.Len() != 0 || memory.outstanding != 0 {
		t.Fatalf("work budget did not fail closed: metadata=%+v err=%v out=%d leased=%d", metadata, err, out.Len(), memory.outstanding)
	}
	limit := ScreeningLimit(err)
	if limit == nil || limit.Resource != "screen_work_units" || limit.Maximum != 1 || limit.Observed <= limit.Maximum {
		t.Fatalf("ScreeningLimit(%v) = %+v, want screen_work_units > 1", err, limit)
	}
}
