package inject

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// SourceChangeFile is one file another actor changed since the session's
// previous turn, grouped from the ledger effects in that window.
type SourceChangeFile struct {
	// Path is the canonical display path (@label-qualified off the primary root).
	Path string `json:"path,omitempty"`
	// Actor is the sourceledger.ActorClass spelling for the file's newest
	// foreign effect in the window.
	Actor string `json:"actor,omitempty"`
	// Detail qualifies a foreign agent effect with its recorded label.
	Detail string `json:"detail,omitempty"`
	// Op is the newest effect's recorded change op.
	Op string `json:"op,omitempty"`
	// At is the newest effect's UTC clock time, fixed for byte-stability.
	At string `json:"at,omitempty"`
	// Effects counts the window's foreign effects on this file.
	Effects int `json:"effects,omitempty"`
	// WorkingSet marks a path the session read or wrote. Briefs stored without
	// the field render through the unflagged branch.
	WorkingSet bool `json:"working_set,omitempty"`
}

// SourceGitLine is one observed git ref movement inside the turn window,
// stated from the ledger's transition record.
type SourceGitLine struct {
	// Root is the display label of the moved root; empty for the primary root.
	Root string `json:"root,omitempty"`
	// Kind is the SourceGitChangeKind wire spelling (checkout, commit, …).
	Kind string `json:"kind,omitempty"`
	// FromRef / ToRef are short branch names around the movement.
	FromRef string `json:"from_ref,omitempty"`
	ToRef   string `json:"to_ref,omitempty"`
	// FromCommit / ToCommit are abbreviated commit ids.
	FromCommit string `json:"from_commit,omitempty"`
	ToCommit   string `json:"to_commit,omitempty"`
	// Detail is git's own subject for the movement.
	Detail string `json:"detail,omitempty"`
	// At is the movement's UTC clock time, fixed for byte-stability.
	At string `json:"at,omitempty"`
}

// SourceChangeBrief is the host-stated record of what other actors changed
// between the previous turn boundary and this one. Empty means the window held
// no foreign change or no baseline exists — the builder never guesses. It is
// stored on the turn it opened, so its JSON shape is durable.
type SourceChangeBrief struct {
	// Files itemizes foreign changes up to the row cap, working set first.
	Files []SourceChangeFile `json:"files,omitempty"`
	// Git itemizes observed ref movements in the window, oldest first, so file
	// changes below read against their cause.
	Git []SourceGitLine `json:"git,omitempty"`
	// OtherFiles / OtherEffects count the foreign changes that did not fit in
	// Files.
	OtherFiles   int `json:"other_files,omitempty"`
	OtherEffects int `json:"other_effects,omitempty"`
	// Truncated reports that the window overflowed the effect cap, so the
	// counts above are a floor, not a total.
	Truncated bool `json:"truncated,omitempty"`
}

// Empty reports whether the brief carries nothing worth injecting.
func (b SourceChangeBrief) Empty() bool {
	return len(b.Files) == 0 && len(b.Git) == 0 && b.OtherFiles == 0 && !b.Truncated
}

// OtherMayIncludeWorkingSet reports whether the counted files could be in the
// working set. Rows list the working set first, so an unflagged last row means
// every working-set file was itemized.
func (b SourceChangeBrief) OtherMayIncludeWorkingSet() bool {
	return b.OtherFiles > 0 && len(b.Files) > 0 && b.Files[len(b.Files)-1].WorkingSet
}

// RenderSourceChangesBlock renders guidance/source-changes.md via Binding stem.
// When renderer is nil, returns empty (fail closed — no Go prose builder).
func RenderSourceChangesBlock(
	ctx context.Context,
	renderer *prompts.InjectRenderer,
	sessionID string,
	brief SourceChangeBrief,
) string {
	if renderer == nil || brief.Empty() {
		return ""
	}
	files := make([]map[string]any, 0, len(brief.Files))
	for _, f := range brief.Files {
		files = append(files, map[string]any{
			"path": f.Path, "actor": f.Actor, "detail": f.Detail,
			"op": f.Op, "at": f.At, "effects": f.Effects, "working_set": f.WorkingSet,
		})
	}
	git := make([]map[string]any, 0, len(brief.Git))
	for _, g := range brief.Git {
		git = append(git, map[string]any{
			"root": g.Root, "kind": g.Kind, "from_ref": g.FromRef, "to_ref": g.ToRef,
			"from_commit": g.FromCommit, "to_commit": g.ToCommit,
			"detail": g.Detail, "at": g.At,
		})
	}
	block, err := anchor.RenderInform(
		ctx, anchor.InjectSourceChanges, anchor.MatchContext{Surface: "coordinator", SessionID: sessionID},
		renderer, map[string]any{
			"files": files, "git": git, "other_files": brief.OtherFiles,
			"other_effects": brief.OtherEffects, "truncated": brief.Truncated,
			"other_may_include_working_set": brief.OtherMayIncludeWorkingSet(),
		},
	)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(block)
}
