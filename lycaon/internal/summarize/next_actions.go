package summarize

import (
	"context"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/decide"
	"github.com/lycaon/lycaon/internal/textrank"
)

// finalizeNextActions ranks and bounds follow-up calls.
func finalizeNextActions(ctx context.Context, rr decide.Reranker, task string, actions []NextAction, pack ContextPack, caps Caps) []NextAction {
	maxN := caps.Pack.NextActionsMax
	if maxN <= 0 {
		maxN = DefaultCaps().Pack.NextActionsMax
	}
	if maxN <= 0 {
		maxN = 3
	}

	candidates := make([]NextAction, 0, len(actions)+8)
	seen := map[string]bool{}
	add := func(a NextAction) {
		a = normalizeNextAction(a)
		if a.Tool == "summarize" {
			a.Task = task
		}
		if a.Tool == "" {
			return
		}
		if strings.EqualFold(a.Tool, "grep") {
			if a.Pattern == "" {
				return
			}
		} else if a.Path == "" && len(a.Paths) == 0 {
			return
		}
		key := nextActionKey(a)
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, a)
	}
	for _, a := range actions {
		add(a)
	}
	for _, a := range nextActionsFromRollupSkeleton(pack) {
		add(a)
	}
	if len(candidates) == 0 {
		return nil
	}

	scores := nextActionTaskScores(ctx, rr, task, candidates)
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if scores != nil {
			sa, sb := scores[nextActionKey(a)], scores[nextActionKey(b)]
			if sa != sb {
				return sa > sb
			}
		}
		pa, pb := nextActionBreadthTieBreak(a), nextActionBreadthTieBreak(b)
		if pa != pb {
			return pa > pb
		}
		return a.Path < b.Path
	})

	if len(candidates) > maxN {
		candidates = candidates[:maxN]
	}
	return candidates
}

func normalizeNextAction(a NextAction) NextAction {
	a.Tool = strings.TrimSpace(a.Tool)
	a.Path = strings.TrimSpace(a.Path)
	for i := range a.Paths {
		a.Paths[i] = strings.TrimSpace(a.Paths[i])
	}
	a.Why = strings.TrimSpace(a.Why)
	a.Lines = strings.TrimSpace(a.Lines)
	a.Pattern = strings.TrimSpace(a.Pattern)
	if a.Tool == "" && a.Path != "" {
		a.Tool = "read"
	}
	if a.Why == "" {
		switch a.Tool {
		case "summarize":
			a.Why = "summarize for depth"
		case "grep":
			a.Why = "grep for remaining matches"
		default:
			a.Why = "read for detail"
		}
	}
	return a
}

func nextActionKey(a NextAction) string {
	return strings.ToLower(a.Tool) + "\x00" + a.Path + "\x00" + strings.Join(a.Paths, "\x00") + "\x00" + a.Lines + "\x00" + a.Pattern + "\x00" + a.Cursor
}

func nextActionBreadthTieBreak(a NextAction) int {
	if strings.EqualFold(a.Tool, "summarize") {
		if a.TargetKind == SubtreeKindDir {
			return 3
		}
		return 2
	}
	if strings.EqualFold(a.Tool, "grep") {
		return 1
	}
	return 0
}

func nextActionTaskScores(ctx context.Context, rr decide.Reranker, task string, actions []NextAction) map[string]float64 {
	if len(taskQueryTerms(task)) == 0 || len(actions) < 2 {
		return nil
	}
	docs := make([][]textrank.Field, len(actions))
	for i, a := range actions {
		docs[i] = []textrank.Field{
			{Text: a.Path + " " + strings.Join(a.Paths, " "), Weight: 1.5},
			{Text: a.Pattern, Weight: 1.25},
		}
	}
	scores := rerank(ctx, rr, decide.SiteSummarizeNextActions, task, taskFieldScores(task, docs), func() []string {
		texts := make([]string, len(actions))
		for i, a := range actions {
			texts[i] = nextActionText(a)
		}
		return texts
	})
	out := make(map[string]float64, len(actions))
	for i, a := range actions {
		out[nextActionKey(a)] = scores[i]
	}
	return out
}

// nextActionText is what the engine reads for one follow-up call.
func nextActionText(a NextAction) string {
	var b strings.Builder
	b.WriteString("Next: ")
	b.WriteString(a.Tool)
	if paths := strings.Join(append([]string{a.Path}, a.Paths...), " "); strings.TrimSpace(paths) != "" {
		b.WriteString(" ")
		b.WriteString(strings.TrimSpace(paths))
	}
	if a.Pattern != "" {
		b.WriteString(" pattern=")
		b.WriteString(a.Pattern)
	}
	if a.Lines != "" {
		b.WriteString(" lines=")
		b.WriteString(a.Lines)
	}
	if a.Why != "" {
		b.WriteString("\n")
		b.WriteString(boundRunes(a.Why, rerankExcerptRunes))
	}
	return b.String()
}

// nextActionsFromRollupSkeleton turns undrilled directory_rollup skeleton rows
// into summarize zoom candidates when allocate did not already emit them.
func nextActionsFromRollupSkeleton(pack ContextPack) []NextAction {
	var out []NextAction
	seen := map[string]bool{}
	for _, s := range pack.Skeleton {
		if s.Kind != KindDirectoryRollup || s.Path == "" || strings.HasPrefix(s.Path, "+") {
			continue
		}
		if seen[s.Path] {
			continue
		}
		seen[s.Path] = true
		out = append(out, NextAction{
			Tool: "summarize", Path: s.Path,
			Why: "Explore this area",
		})
	}
	return out
}
