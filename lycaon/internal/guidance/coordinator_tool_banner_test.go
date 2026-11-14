package guidance

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRenderSynthesizedSurveyPreambleRepoResearcher(t *testing.T) {
	setTestRenderer(t)
	out, err := RenderSynthesizedSurveyPreamble(context.Background(), "repo-researcher")
	testutil.FailErr(t, "RenderSynthesizedSurveyPreamble failed", err)
	if strings.Contains(out, "GameFiles") {
		t.Fatalf("repo-researcher preamble must not mention GameFiles: %q", out)
	}
	if !strings.Contains(out, "Lockfile") {
		t.Fatalf("preamble = %q", out)
	}
}
