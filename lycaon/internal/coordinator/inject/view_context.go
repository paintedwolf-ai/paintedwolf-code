package inject

import (
	"context"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

// RenderViewContextBlock renders guidance/view-context.md for the view a
// prompt carried. readOffered is the current surface's answer.
func RenderViewContextBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, view api.ViewContext, readOffered bool) string {
	if renderer == nil {
		return ""
	}
	editors := make([]map[string]any, 0, len(view.Editors))
	for _, e := range view.Editors {
		row := map[string]any{"path": e.DisplayPath, "unsaved": e.Unsaved, "selected": e.Selection != nil}
		if s := e.Selection; s != nil {
			row["start_line"], row["end_line"], row["cursor"] = s.StartLine, s.EndLine, s.Cursor
		}
		editors = append(editors, row)
	}
	diffs := make([]string, 0, len(view.Diffs))
	for _, d := range view.Diffs {
		diffs = append(diffs, d.DisplayPath)
	}
	findings := make([]map[string]any, 0, len(view.Findings))
	for _, f := range view.Findings {
		findings = append(findings, map[string]any{"rule_id": f.RuleID, "level": string(f.Level), "location": locationText(f.Location)})
	}
	vars := map[string]any{"editors": editors, "diffs": diffs, "findings": findings, "read_offered": readOffered, "folder": "", "search_query": "", "search_hit": ""}
	if view.Folder != nil {
		vars["folder"] = view.Folder.DisplayPath
	}
	if view.Search != nil {
		// Quoted so the person's query reads as data.
		vars["search_query"], vars["search_hit"] = strconv.Quote(view.Search.Query), locationText(view.Search.Hit)
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectViewContext, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID}, renderer, vars)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(block)
}

func locationText(l *api.ViewLocation) string {
	if l == nil {
		return ""
	}
	if l.Line > 0 {
		return l.DisplayPath + ":" + strconv.Itoa(l.Line)
	}
	return l.DisplayPath
}
