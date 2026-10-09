package report

import (
	"fmt"
	"strings"
	"testing"

	"github.com/johnfercher/go-tree/node"
	"github.com/johnfercher/maroto/v2/pkg/core"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestMdToRows_CommonNodes(t *testing.T) {
	md := strings.Join([]string{
		"# Title",
		"",
		"A paragraph with **bold** and *italic*.",
		"",
		"- item one",
		"- item two",
		"",
		"```go",
		"fmt.Println(1)",
		"```",
		"",
		"See [docs](https://example.com).",
	}, "\n")

	blocks := mdToBlocks(testMeasurer(t), md)
	if len(blocks) == 0 {
		t.Fatal("want blocks from synthesis markdown")
	}
	joined := joinRowValues(blocks)
	for _, want := range []string{"Title", "item one", "fmt.Println", "docs", "https://example.com"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("row text %q: want substring %q", joined, want)
		}
	}
}

func TestMdToBlocks_GFMTableRendersAsTable(t *testing.T) {
	md := "| Area | Result |\n| --- | --- |\n| egress | pass |\n\n> quoted exotic"
	joined := joinRowValues(mdToBlocks(testMeasurer(t), md))
	for _, want := range []string{"Area", "Result", "egress", "pass", "quoted exotic"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("markdown table text %q: want substring %q", joined, want)
		}
	}
}

func TestMdToBlocks_EmptySafe(t *testing.T) {
	if blocks := mdToBlocks(testMeasurer(t), ""); len(blocks) != 0 {
		t.Fatalf("empty md: got %d blocks want 0", len(blocks))
	}
	if blocks := mdToBlocks(testMeasurer(t), "   \n\n"); len(blocks) != 0 {
		t.Fatalf("whitespace md: got %d blocks want 0", len(blocks))
	}
}

func TestRender_EmptySynthesisSafe(t *testing.T) {
	input := ReportInput{
		ReportHeader: ReportHeader{
			Title:       "Empty synth",
			RunID:       "run_empty",
			Project:     "acme/app",
			CompletedAt: "2026-07-08T15:04:05Z",
			HeadSHA:     "abc",
		},
		Synthesis: "",
	}
	pdf, err := Render(input)
	testutil.FailErr(t, "render empty synthesis", err)
	if len(pdf) < 100 {
		t.Fatalf("pdf too small: %d", len(pdf))
	}
	if !strings.HasPrefix(string(pdf), "%PDF") {
		t.Fatal("want PDF magic")
	}
}

func joinRowValues(blocks []block) string {
	var b strings.Builder
	for _, blk := range blocks {
		for _, r := range blk.rows {
			collectStructure(r.row.GetStructure(), &b)
		}
	}
	return b.String()
}

func collectStructure(n *node.Node[core.Structure], b *strings.Builder) {
	if n == nil {
		return
	}
	data := n.GetData()
	switch v := data.Value.(type) {
	case string:
		b.WriteString(v)
		b.WriteByte(' ')
	case fmt.Stringer:
		b.WriteString(v.String())
		b.WriteByte(' ')
	}
	for _, next := range n.GetNexts() {
		collectStructure(next, b)
	}
}
