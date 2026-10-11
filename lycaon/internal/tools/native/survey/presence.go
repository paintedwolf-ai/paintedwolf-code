package survey

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/lycaon/lycaon/internal/agentpresence"
	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
	"github.com/lycaon/lycaon/pkg/api"
)

// readCapture collects the lines one read mode returned.
type readCapture struct {
	whole bool
	spans []agentpresence.Span
}

func (c *readCapture) lines(start, end int) {
	if start >= 1 && end >= start {
		c.spans = append(c.spans, agentpresence.Span{StartLine: start, EndLine: end})
	}
}

// record appends the captured spans to the invocation's source reads. Reads
// served by an editor document carry its identity so presence anchors that revision.
func (c *readCapture) record(tctx tools.ToolContext, target agentpresence.Target, editor *tools.EditorDocumentText) {
	if tctx.Effects.Out == nil || (!c.whole && len(c.spans) == 0) {
		return
	}
	read := agentpresence.Read{Target: target, Extent: api.AgentPresenceExtentRange, Spans: c.spans}
	if c.whole {
		read.Extent, read.Spans = api.AgentPresenceExtentWholeFile, nil
	}
	if editor != nil {
		read.Document = agentpresence.Document{ID: editor.ID, Revision: editor.Revision}
	}
	tctx.Effects.Out.SourceReads = append(tctx.Effects.Out.SourceReads, read)
}

// reportGrepScope reports a single-root search below the root as the call's target.
func reportGrepScope(tctx tools.ToolContext, targets []grepTarget) {
	if tctx.Effects.Presence == nil || len(targets) != 1 {
		return
	}
	target := targets[0]
	rel, err := filepath.Rel(target.root.Path, target.fullRoot)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || strings.TrimSpace(target.root.ID) == "" {
		return
	}
	tctx.Effects.Presence.Target(agentpresence.Target{RootID: target.root.ID, Path: filepath.ToSlash(rel)}, api.AgentActivityKindReading)
}

// recordGrepMatches captures the matches a search returned. Each match's path
// is the host's own display path, resolved back to its root; each entry
// contributes one span per occurrence of the pattern on its line.
func recordGrepMatches(ctx context.Context, boundary *sandbox.Boundary, tctx tools.ToolContext, re *regexp.Regexp, structural bool, matches []grepMatch) {
	if tctx.Effects.Out == nil || len(matches) == 0 {
		return
	}
	var reads []agentpresence.Read
	targets := map[string]*agentpresence.Target{}
	current := ""
	for _, m := range matches {
		target, seen := targets[m.Path]
		if !seen {
			target = nil
			if resolved, err := projectpaths.ResolveRead(ctx, boundary, tctx, m.Path); err == nil && !resolved.External && resolved.Root.ID != "" && resolved.ScopeRel != "" {
				target = &agentpresence.Target{RootID: resolved.Root.ID, Path: filepath.ToSlash(resolved.ScopeRel)}
			}
			targets[m.Path] = target
		}
		if target == nil {
			continue
		}
		if m.Path != current || len(reads) == 0 {
			reads = append(reads, agentpresence.Read{Target: *target, Extent: api.AgentPresenceExtentMatches})
			current = m.Path
		}
		read := &reads[len(reads)-1]
		spans := grepMatchSpans(re, structural, m)
		read.Spans = append(read.Spans, spans...)
		read.ItemSpans = append(read.ItemSpans, len(spans))
	}
	tctx.Effects.Out.SourceReads = append(tctx.Effects.Out.SourceReads, reads...)
}

// grepMatchSpans returns one character span per pattern occurrence on a text
// match's line. Structural matches name a starting line and cover it whole.
func grepMatchSpans(re *regexp.Regexp, structural bool, m grepMatch) []agentpresence.Span {
	if m.Line < 1 {
		return nil
	}
	if structural || re == nil {
		return []agentpresence.Span{{StartLine: m.Line, EndLine: m.Line}}
	}
	locations := re.FindAllStringIndex(m.Content, -1)
	spans := make([]agentpresence.Span, 0, len(locations))
	for _, loc := range locations {
		if loc[1] <= loc[0] {
			continue
		}
		start, end := utf16Offset(m.Content, loc[0]), utf16Offset(m.Content, loc[1])
		spans = append(spans, agentpresence.Span{StartLine: m.Line, EndLine: m.Line, StartCharacter: &start, EndCharacter: &end})
	}
	if len(spans) == 0 {
		spans = append(spans, agentpresence.Span{StartLine: m.Line, EndLine: m.Line})
	}
	return spans
}

// utf16Offset converts a byte offset within s to UTF-16 code units.
func utf16Offset(s string, byteOffset int) int {
	units := 0
	for _, r := range s[:byteOffset] {
		if r >= 0x10000 {
			units += 2
		} else {
			units++
		}
	}
	return units
}
