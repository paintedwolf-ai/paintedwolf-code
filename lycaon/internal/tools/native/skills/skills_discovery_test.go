package skills_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/coordinator/turnload"
	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	skilltools "github.com/lycaon/lycaon/internal/tools/native/skills"
)

func TestSkillDiscoveryOutageToExactRead(t *testing.T) {
	for _, fault := range []error{decide.ErrUnavailable, decide.ErrDeadline, decide.ErrEngine} {
		t.Run(fmt.Sprint(fault), func(t *testing.T) {
			roster := make([]skills.Skill, 43)
			for i := range roster {
				roster[i] = skills.Skill{Name: fmt.Sprintf("skill-%02d", i), Description: "Procedure description", Body: "Private procedure body", Project: true}
			}
			d := &decidetest.Fake{Err: fault}
			tool := &skilltools.SkillsReadTool{
				Skills: func(context.Context, tools.ToolContext) []skills.Skill { return roster },
				Lookup: func(ctx context.Context, _ tools.ToolContext, need string, catalog []skills.Skill) turnload.LookupOutcome {
					return turnload.LookupSkills(ctx, d, turnload.LookupSpec{DeadlineMS: 1000, ReadAt: 3}, need, catalog)
				},
			}
			out := &tools.ToolInvocationOut{}
			tctx := tools.ToolContext{SessionID: "s", Out: out}
			args := map[string]any{"need": "perform the procedure"}
			count := 0
			for {
				raw, err := tool.Run(t.Context(), args, tctx)
				testutil.FailErr(t, "discover skills", err)
				var result struct {
					Discovery *turnload.Discovery `json:"discovery"`
				}
				testutil.FailErr(t, "decode discovery", json.Unmarshal([]byte(raw), &result))
				if result.Discovery == nil || out.Skill != nil || strings.Contains(raw, "Private procedure body") {
					t.Fatalf("discovery read a skill: %s", raw)
				}
				if count == 0 {
					want := "ranking_unavailable"
					if result.Discovery.Status != want {
						t.Fatalf("status=%s", result.Discovery.Status)
					}
				}
				for _, entry := range result.Discovery.Entries {
					if entry.Name != roster[count].Name || entry.Description == "" {
						t.Fatalf("entry=%+v", entry)
					}
					count++
				}
				if result.Discovery.NextNeed == "" {
					break
				}
				args["need"] = result.Discovery.NextNeed
			}
			if count != len(roster) || len(d.Ranks) != 1 {
				t.Fatalf("entries=%d calls=%d", count, len(d.Ranks))
			}
			raw, err := tool.Run(t.Context(), map[string]any{"need": "skill-42"}, tctx)
			testutil.FailErr(t, "read exact skill", err)
			if !strings.Contains(raw, "Private procedure body") || out.Skill == nil || out.Skill.Name != "skill-42" || len(d.Ranks) != 1 {
				t.Fatalf("exact retry=%s", raw)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			out.Skill = nil
			if _, err := tool.Run(ctx, map[string]any{"need": "skill-01"}, tctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled=%v", err)
			}
			if out.Skill != nil {
				t.Fatal("canceled read activated skill")
			}
			if _, err := tool.Run(t.Context(), map[string]any{"need": "discovery:invalid", "resource": "reference.md"}, tctx); err == nil {
				t.Fatal("resource browsing must reject")
			}
		})
	}
}

func TestSkillHealthyRankingHasNoDiscovery(t *testing.T) {
	for _, score := range []float64{0, 4} {
		t.Run(fmt.Sprint(score), func(t *testing.T) {
			roster := []skills.Skill{{Name: "procedure", Description: "A procedure", Body: "Private instructions", Project: true}}
			d := &decidetest.Fake{Scores: []float64{score}}
			tool := &skilltools.SkillsReadTool{
				Skills: func(context.Context, tools.ToolContext) []skills.Skill { return roster },
				Lookup: func(ctx context.Context, _ tools.ToolContext, need string, catalog []skills.Skill) turnload.LookupOutcome {
					return turnload.LookupSkills(ctx, d, turnload.LookupSpec{DeadlineMS: 1000, ReadAt: 3}, need, catalog)
				},
			}
			out := &tools.ToolInvocationOut{}
			raw, err := tool.Run(t.Context(), map[string]any{"need": "do the work"}, tools.ToolContext{Out: out})
			if score == 0 {
				if err == nil || raw != "" || out.Skill != nil {
					t.Fatalf("no match output=%q error=%v", raw, err)
				}
			} else {
				testutil.FailErr(t, "healthy selection", err)
				if raw != "Skill: procedure\n\nPrivate instructions\n\nSkill directory: \n" || out.Skill == nil {
					t.Fatalf("healthy response=%q", raw)
				}
			}
			if len(d.Ranks) != 1 {
				t.Fatalf("rank calls=%d", len(d.Ranks))
			}
		})
	}
}
