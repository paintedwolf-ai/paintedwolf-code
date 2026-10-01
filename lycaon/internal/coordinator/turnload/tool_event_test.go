package turnload

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/skills"
)

func toolEventRoster() []skills.Skill {
	return []skills.Skill{
		{Name: "verify-visual-change", Description: "Capture the running page to prove a visual change."},
		{Name: "commit-in-logical-groups", Description: "Stage and commit related changes together."},
		{Name: "work-with-containers", Description: "Build and run containers."},
	}
}

func TestRankToolEventReadsPointsOrLeavesTheTurnAlone(t *testing.T) {
	spec := ToolEventSpec{DeadlineMS: 500, ReadAt: 3.4, PointerAt: 2.8, Margin: 0.3}
	tool := ToolCard{Name: "capture_page", Description: "Open a page and capture it."}
	cases := []struct {
		name         string
		scores       []float64
		read, points string
	}{
		{"clear read", []float64{3.6, 1.0, 1.2}, "verify-visual-change", ""},
		{"pointer band", []float64{3.0, 1.0, 1.2}, "", "verify-visual-change"},
		{"close second reads nothing", []float64{3.6, 3.5, 1.2}, "", ""},
		{"below the pointer bar", []float64{2.0, 1.0, 1.2}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := &decidetest.Fake{Scores: c.scores}
			got := RankToolEvent(context.Background(), fake, spec, "Build me a chess game", tool, toolEventRoster())
			if got.Abstained || got.Tool != "capture_page" {
				t.Fatalf("outcome = %+v", got)
			}
			read, pointer := "", ""
			if got.Read != nil {
				read = got.Read.Name
			}
			if got.Pointer != nil {
				pointer = got.Pointer.Name
			}
			if read != c.read || pointer != c.points {
				t.Fatalf("read=%q pointer=%q, want %q %q", read, pointer, c.read, c.points)
			}
			need := fake.Ranks[0].Task
			if !strings.Contains(need, "Build me a chess game") || !strings.Contains(need, "First tool called: capture_page — Open a page") {
				t.Fatalf("ranked text = %q", need)
			}
		})
	}
}

func TestRankToolEventAbstainsWithoutAnEngineOrARequest(t *testing.T) {
	spec := ToolEventSpec{DeadlineMS: 500, ReadAt: 3.4, PointerAt: 2.8}
	tool := ToolCard{Name: "git_commit"}
	if got := RankToolEvent(context.Background(), decide.Absent{}, spec, "commit it", tool, toolEventRoster()); !got.Abstained || len(got.Names()) != 0 {
		t.Fatalf("absent engine = %+v", got)
	}
	if got := RankToolEvent(context.Background(), &decidetest.Fake{Scores: []float64{4, 0, 0}}, spec, "", tool, toolEventRoster()); !got.Abstained {
		t.Fatalf("empty request = %+v", got)
	}
}

func TestLedgerClaimsOneToolEventPerTurn(t *testing.T) {
	ledger := NewLedger()
	opening := TurnOpening{OpeningMessageID: "m1", Request: "Build me a chess game", Loadable: []string{"capture_page", "git_commit"}}
	ledger.BeginTurn("s", opening, Decision{})
	if _, ok := ledger.ClaimToolEvent("s", "read"); ok {
		t.Fatal("a floor tool must not claim the turn")
	}
	request, ok := ledger.ClaimToolEvent("s", "capture_page")
	if !ok || request != "Build me a chess game" {
		t.Fatalf("claim = %q %v", request, ok)
	}
	if _, ok := ledger.ClaimToolEvent("s", "git_commit"); ok {
		t.Fatal("a second loadable tool must not claim the same turn")
	}
	ledger.SetPointer("s", &SkillRank{Name: "verify-visual-change", Score: 3.0})
	if p := ledger.Pointer("s"); p == nil || p.Name != "verify-visual-change" {
		t.Fatalf("pointer = %+v", p)
	}
	standing := ledger.Standing("s")
	if standing.ToolEvent != "capture_page" || standing.Pointer == nil {
		t.Fatalf("standing = %+v", standing)
	}
	restored := NewLedger()
	restored.Restore("s", standing, nil)
	if restored.Pointer("s") == nil {
		t.Fatal("restore dropped the pointer")
	}
	// A new turn opens the claim again and clears the pointer.
	ledger.BeginTurn("s", TurnOpening{OpeningMessageID: "m2", Request: "Now commit it", Loadable: []string{"git_commit"}}, Decision{})
	if ledger.Pointer("s") != nil {
		t.Fatal("pointer leaked into the next turn")
	}
	if _, ok := ledger.ClaimToolEvent("s", "git_commit"); !ok {
		t.Fatal("the next turn must accept a claim")
	}
	// A turn that already read a skill takes no tool event.
	ledger.BeginTurn("s", TurnOpening{OpeningMessageID: "m3", Request: "Again", Loadable: []string{"git_commit"}}, Decision{})
	ledger.SetPreload("s", &SkillPreload{Name: "commit-in-logical-groups", Body: "body"})
	if _, ok := ledger.ClaimToolEvent("s", "git_commit"); ok {
		t.Fatal("a preloaded turn must not claim a tool event")
	}
}

func TestRankingSelectNeedsABarAndAMargin(t *testing.T) {
	ranking := Ranking{Ranks: []SkillRank{{Name: "a", Score: 3.5}, {Name: "b", Score: 3.3}}}
	if _, ok := ranking.Select(3.2, 0.3); ok {
		t.Fatal("a runner-up inside the margin must block the selection")
	}
	if top, ok := ranking.Select(3.2, 0.1); !ok || top.Name != "a" {
		t.Fatalf("select = %+v %v", top, ok)
	}
	if _, ok := ranking.Select(0, 0); ok {
		t.Fatal("a zero bar disables selection")
	}
	if _, ok := (Ranking{Ranks: []SkillRank{{Name: "a", Score: 4.5}}}).Select(3, 0); ok {
		t.Fatal("a score above the rubric ceiling is invalid")
	}
}
