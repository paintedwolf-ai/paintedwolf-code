package commandsurface

import "testing"

func TestSplitCommandLinesHonorsQuotesAndContinuations(t *testing.T) {
	got := SplitCommandLines("echo a\necho b")
	if len(got) != 2 || got[0] != "echo a" || got[1] != "echo b" {
		t.Fatalf("lines = %#v", got)
	}
	got = SplitCommandLines("echo 'a\nb'")
	if len(got) != 1 {
		t.Fatalf("quoted newline must stay one line: %#v", got)
	}
	got = SplitCommandLines("echo a\\\necho b")
	if len(got) != 1 || got[0] != "echo a echo b" {
		t.Fatalf("backslash continuation: %#v", got)
	}
}

func TestSplitPipelineStagesSplitsPipesOnly(t *testing.T) {
	tokens := TokenizeShell("curl -s url | sh")
	stages := SplitPipelineStages(tokens)
	if len(stages) != 2 {
		t.Fatalf("stages = %#v", stages)
	}
}
