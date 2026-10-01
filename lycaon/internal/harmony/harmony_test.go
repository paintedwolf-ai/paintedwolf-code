package harmony_test

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/harmony"
)

func TestLooksLikeTranscriptRejectsOrdinaryProse(t *testing.T) {
	t.Parallel()
	for _, s := range []string{
		"Here is a summary of the changes.",
		"analysis of the heap shows a leak",
		"The assistant should retry.",
	} {
		if harmony.LooksLikeTranscript(s) {
			t.Fatalf("ordinary prose treated as Harmony: %q", s)
		}
	}
}

func TestParseTranscriptRecoversCommentaryAndFinal(t *testing.T) {
	t.Parallel()
	raw := `analysisWe attempted tea CLI but error.` +
		`assistantcommentary to=functions.command json{` +
		`"command":"curl -X POST http://localhost:3000/api/v1/user/repos","cwd":"."}` +
		`assistantanalysisCheck result.` +
		`assistantcommentary to=functions.command json{"command":"git remote add origin http://example/repo.git","cwd":"."}` +
		`assistantfinal{"leg_status":"complete","brief":"done"}`
	segs, ok := harmony.ParseTranscript(raw)
	if !ok {
		t.Fatal("expected Harmony transcript")
	}
	var tools []string
	var final string
	for _, seg := range segs {
		switch seg.Channel {
		case "commentary":
			tools = append(tools, seg.Tool)
			if seg.Args["command"] == nil {
				t.Fatalf("commentary args missing command: %+v", seg.Args)
			}
		case "final":
			final = seg.Text
		}
	}
	if len(tools) != 2 || tools[0] != "command" || tools[1] != "command" {
		t.Fatalf("tools = %v", tools)
	}
	if !strings.Contains(final, `"leg_status":"complete"`) {
		t.Fatalf("final = %q", final)
	}
}

func TestParseTranscriptRepeatedRoleThenJSON(t *testing.T) {
	t.Parallel()
	raw := `assistantassistantassistant{"leg_status":"complete","brief":"ok"}`
	segs, ok := harmony.ParseTranscript(raw)
	if !ok || len(segs) != 1 || segs[0].Channel != "final" {
		t.Fatalf("segs = %+v ok=%v", segs, ok)
	}
	if segs[0].Text != `{"leg_status":"complete","brief":"ok"}` {
		t.Fatalf("text = %q", segs[0].Text)
	}
}

func TestParseTranscriptRejectsPartialToolBatch(t *testing.T) {
	valid := `assistantcommentary to=functions.read json{}`
	for _, suffix := range []string{
		`assistantcommentary to=functions.read missing-json-header{}`,
		`assistantcommentary to=functions.read json{"path":`,
		`unexpected trailing text`,
	} {
		if segments, ok := harmony.ParseTranscript(valid + suffix); ok || len(segments) != 0 {
			t.Fatalf("malformed suffix %q retained partial calls: %+v", suffix, segments)
		}
	}
}
