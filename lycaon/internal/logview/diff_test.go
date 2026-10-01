package logview

import (
	"bytes"
	"strings"
	"testing"
)

func TestRenderTurnDiffShowsAddition(t *testing.T) {
	turn1 := LLMRecord{Messages: []LLMMessage{
		{Role: "user", Content: rawStr(t, "alpha")},
	}}
	turn2 := LLMRecord{Messages: []LLMMessage{
		{Role: "user", Content: rawStr(t, "alpha")},
		{Role: "user", Content: rawStr(t, "beta added")},
	}}
	a := &Agent{Turns: []LLMRecord{turn1, turn2}}

	var buf bytes.Buffer
	d := NewDisplay(DefaultConfig(), false)
	d.RenderTurnDiff(&buf, a, 1)
	out := buf.String()

	if !strings.Contains(out, "diff: turn 1 → turn 2") {
		t.Errorf("missing header\n%s", out)
	}
	foundAdd := false
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "+") && strings.Contains(line, "beta added") {
			foundAdd = true
		}
	}
	if !foundAdd {
		t.Errorf("diff should mark 'beta added' as added\n%s", out)
	}
}

func TestRenderTurnDiffFirstTurn(t *testing.T) {
	a := &Agent{Turns: []LLMRecord{{Messages: []LLMMessage{{Role: "user", Content: rawStr(t, "x")}}}}}
	var buf bytes.Buffer
	NewDisplay(DefaultConfig(), false).RenderTurnDiff(&buf, a, 0)
	if !strings.Contains(buf.String(), "no previous turn") {
		t.Errorf("first turn diff = %q", buf.String())
	}
}
