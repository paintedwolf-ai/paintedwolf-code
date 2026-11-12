package api

import "strconv"

// MapText returns a copy with every free-text field passed through f, named
// by its field path. Identities, levels and line numbers are not text.
func (v ViewContext) MapText(f func(field, value string) string) ViewContext {
	at := func(list string, i int) string { return "view_context." + list + "[" + strconv.Itoa(i) + "]" }
	location := func(field string, l *ViewLocation) *ViewLocation {
		if l == nil {
			return nil
		}
		out := *l
		out.Path, out.DisplayPath = f(field+".path", l.Path), f(field+".display_path", l.DisplayPath)
		return &out
	}
	out := v
	out.Editors = make([]ViewEditor, len(v.Editors))
	for i, e := range v.Editors {
		e.Path, e.DisplayPath = f(at("editors", i)+".path", e.Path), f(at("editors", i)+".display_path", e.DisplayPath)
		out.Editors[i] = e
	}
	out.Diffs = make([]ViewPath, len(v.Diffs))
	for i, d := range v.Diffs {
		d.Path, d.DisplayPath = f(at("diffs", i)+".path", d.Path), f(at("diffs", i)+".display_path", d.DisplayPath)
		out.Diffs[i] = d
	}
	if v.Folder != nil {
		folder := *v.Folder
		folder.Path, folder.DisplayPath = f("view_context.folder.path", folder.Path), f("view_context.folder.display_path", folder.DisplayPath)
		out.Folder = &folder
	}
	if v.Search != nil {
		out.Search = &ViewSearch{Query: f("view_context.search.query", v.Search.Query), Hit: location("view_context.search.hit", v.Search.Hit)}
	}
	out.Findings = make([]ViewFinding, len(v.Findings))
	for i, finding := range v.Findings {
		finding.Location = location(at("findings", i)+".location", finding.Location)
		out.Findings[i] = finding
	}
	return out
}
