package turnload

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/decide/decidetest"
	"github.com/lycaon/lycaon/internal/skills"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDiscoveryPaginationAndScope(t *testing.T) {
	entries := make([]DiscoveryEntry, 53)
	for i := range entries {
		entries[i] = DiscoveryEntry{Name: fmt.Sprintf("tool_%03d", 52-i), Description: strings.Repeat("界", 900)}
	}
	cursor := ""
	count := 0
	for {
		page, err := Discover("tools/session", "inspect", cursor, "catalog", "", entries)
		testutil.FailErr(t, "page catalog", err)
		encoded, err := json.Marshal(page)
		testutil.FailErr(t, "encode page", err)
		if len(encoded) > DiscoveryPageBytes || len(page.Entries) > DiscoveryPageSize || page.Total != len(entries) {
			t.Fatalf("page = %+v", page)
		}
		for _, entry := range page.Entries {
			if entry.Name != fmt.Sprintf("tool_%03d", count) || !utf8.ValidString(entry.Description) || utf8.RuneCountInString(entry.Description) > CardMaxRunes {
				t.Fatalf("entry %d = %+v", count, entry)
			}
			count++
		}
		if page.NextNeed == "" {
			break
		}
		_, cursor, err = ParseDiscoveryNeed(page.NextNeed)
		testutil.FailErr(t, "parse continuation", err)
	}
	if count != len(entries) || entries[0].Name != "tool_052" || utf8.RuneCountInString(entries[0].Description) != 900 {
		t.Fatal("pagination lost entries or mutated input")
	}
	for _, tc := range []struct {
		scope, need, cursor string
		entries             []DiscoveryEntry
	}{
		{"skills/session", "inspect", cursor, entries},
		{"tools/session", "different", cursor, entries},
		{"tools/session", "inspect", "invalid", entries},
		{"tools/session", "inspect", strings.Repeat("x", 65537), entries},
		{"tools/session", "inspect", cursor, entries[1:]},
	} {
		if _, err := Discover(tc.scope, tc.need, tc.cursor, "catalog", "", tc.entries); err == nil {
			t.Fatalf("accepted invalid cursor for %+v", tc)
		}
	}
	empty, err := Discover("tools", "inspect", "", "catalog", "", nil)
	testutil.FailErr(t, "empty catalog", err)
	if empty.Entries == nil || len(empty.Entries) != 0 || empty.NextNeed != "" {
		t.Fatalf("empty page = %+v", empty)
	}
}

func TestRankingFailurePreservesExactAccess(t *testing.T) {
	for _, tc := range []struct {
		name        string
		err         error
		unavailable bool
		failure     RankingFailure
	}{
		{"missing", nil, true, RankingUnavailable},
		{"disabled", decide.ErrDisabled, false, RankingDisabled},
		{"deadline", decide.ErrDeadline, false, RankingTimeout},
		{"context deadline", context.DeadlineExceeded, false, RankingTimeout},
		{"fault", errors.New("broken engine"), false, RankingFault},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &decidetest.Fake{Err: tc.err, Unavailable: tc.unavailable}
			cards := []ToolCard{{Name: "tool_a"}, {Name: "tool_b"}}
			roster := []skills.Skill{{Name: "skill_a"}, {Name: "skill_b"}}
			out := ResolveRequest(t.Context(), d, RequestSpec{DeadlineMS: 1000}, "inspect", cards)
			if out.Failure != tc.failure || len(out.Loaded()) != 0 {
				t.Fatalf("request = %+v", out)
			}
			lookup := LookupSkills(t.Context(), d, LookupSpec{DeadlineMS: 1000}, "inspect", roster)
			if lookup.Failure != tc.failure || len(lookup.Names()) != 0 {
				t.Fatalf("lookup = %+v", lookup)
			}
			before := len(d.Ranks)
			exact := ResolveRequest(t.Context(), d, RequestSpec{}, "TOOL_A", cards)
			skill := LookupSkills(t.Context(), d, LookupSpec{}, "skill_a", roster)
			if !slices.Equal(exact.Loaded(), []string{"tool_a"}) || !slices.Equal(skill.Names(), []string{"skill_a"}) || len(d.Ranks) != before {
				t.Fatalf("exact retry changed selection or invoked ranker: tools=%v skills=%v ranks=%d, want %d", exact.Loaded(), skill.Names(), len(d.Ranks), before)
			}
		})
	}
}

type malformedRanker struct {
	decidetest.Fake
	scores []float64
}

func (d *malformedRanker) Rank(context.Context, decide.Head, string, []string) ([]float64, decide.Engine, error) {
	return d.scores, decide.Engine{}, nil
}

func TestMalformedRankingFallsBack(t *testing.T) {
	for _, scores := range [][]float64{nil, {1, 2}, {-1}, {5}, {math.NaN()}, {math.Inf(1)}} {
		d := &malformedRanker{scores: scores}
		request := ResolveRequest(t.Context(), d, RequestSpec{DeadlineMS: 1000, LoadAt: 2, MaxLoads: 1}, "inspect", []ToolCard{{Name: "tool"}})
		lookup := LookupSkills(t.Context(), d, LookupSpec{DeadlineMS: 1000, ReadAt: 2}, "inspect", []skills.Skill{{Name: "skill"}})
		if request.Failure != RankingFault || lookup.Failure != RankingFault || len(request.Loaded()) != 0 || len(lookup.Names()) != 0 {
			t.Fatalf("invalid scores %v selected a result: %+v %+v", scores, request, lookup)
		}
	}
}

func TestExpiredRankingCannotSelect(t *testing.T) {
	d := &decidetest.Fake{Scores: []float64{4}}
	request := ResolveRequest(t.Context(), d, RequestSpec{DeadlineMS: -1, LoadAt: 2, MaxLoads: 1}, "inspect", []ToolCard{{Name: "tool"}})
	lookup := LookupSkills(t.Context(), d, LookupSpec{DeadlineMS: -1, ReadAt: 2}, "inspect", []skills.Skill{{Name: "skill"}})
	if request.Failure != RankingTimeout || lookup.Failure != RankingTimeout || len(request.Loaded()) != 0 || len(lookup.Names()) != 0 {
		t.Fatalf("late scores selected a result: %+v %+v", request, lookup)
	}
}

func TestDiscoveryNeedParsing(t *testing.T) {
	need, cursor, err := ParseDiscoveryNeed("inspect source")
	testutil.FailErr(t, "ordinary need", err)
	if need != "inspect source" || cursor != "" {
		t.Fatalf("ordinary need changed: %q %q", need, cursor)
	}
	for _, invalid := range []string{"discovery:", "discovery:not-base64", "discovery:" + strings.Repeat("x", 65537)} {
		if _, _, err := ParseDiscoveryNeed(invalid); err == nil {
			t.Fatal("invalid continuation accepted")
		}
	}
}

func TestDiscoveryRejectsAnUnpageableContinuation(t *testing.T) {
	entries := make([]DiscoveryEntry, DiscoveryPageSize+1)
	for i := range entries {
		entries[i].Name = fmt.Sprintf("tool_%02d", i)
	}
	_, err := Discover("tools/session", strings.Repeat("x", DiscoveryPageBytes), "", "no_match", "", entries)
	if err == nil || !strings.Contains(err.Error(), "cannot fit") {
		t.Fatalf("unbounded discovery accepted: %v", err)
	}
}

func TestDiscoveryPreservesRankedOrderAcrossPages(t *testing.T) {
	entries := make([]DiscoveryEntry, 25)
	for i := range entries {
		entries[i] = DiscoveryEntry{Name: fmt.Sprintf("tool_%02d", i), Description: fmt.Sprintf("Tool %d", i)}
	}
	order := []string{"tool_24", "tool_24", "tool_22"}
	page1, err := Discover("tools/session", "create files", "", "no_match", "", entries, order...)
	testutil.FailErr(t, "page 1", err)
	if len(page1.Entries) != DiscoveryPageSize || page1.Entries[0].Name != "tool_24" || page1.Entries[1].Name != "tool_22" || page1.Entries[2].Name != "tool_00" {
		t.Fatalf("page 1 did not place ranked entries first: %+v", page1.Entries[:3])
	}
	need, cursor, err := ParseDiscoveryNeed(page1.NextNeed)
	testutil.FailErr(t, "parse cursor", err)
	page2, err := Discover("tools/session", need, cursor, "catalog", "", entries)
	testutil.FailErr(t, "page 2", err)
	if len(page2.Entries) != 5 || page2.Entries[len(page2.Entries)-1].Name != "tool_23" {
		t.Fatalf("page 2 lost rank order: %+v", page2.Entries)
	}
}

