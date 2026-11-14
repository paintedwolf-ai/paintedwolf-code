package survey

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/sourcebranch"
	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/tools/native/sourceview"
	"github.com/lycaon/lycaon/pkg/api"
)

const (
	overviewEffectCap       = 400
	overviewTruncatedNote   = "the window overflowed the effect cap, so this list is a floor; source_history(path) has one file's full record"
	overviewOpenWindowsNote = "commands in open_windows have not settled, so this list may be incomplete"
	overviewNoRowsNote      = "the ledger recorded nothing on your branch this session; a file it never tracked may still have changed"
)

// sourceHistoryOverviewFile is one file's newest change in the session window.
type sourceHistoryOverviewFile struct {
	Path string `json:"path"`
	Op   string `json:"op"`
	// Unrecorded marks a tree change with no ledger row, so the fields below it are empty.
	Unrecorded bool   `json:"unrecorded,omitempty"`
	Actor      string `json:"actor,omitempty"`
	Detail     string `json:"detail,omitempty"`
	At         string `json:"at,omitempty"`
	Effects    int    `json:"effects,omitempty"`
	VersionID  string `json:"version_id,omitempty"`
	// Branch appears only when the caller's roots are on different branches.
	Branch   string `json:"branch,omitempty"`
	WindowID string `json:"window_id,omitempty"`
	Settled  *bool  `json:"settled,omitempty"`
	// DuringYourCommand states the caller's own window was open, not that its command wrote the change.
	DuringYourCommand bool `json:"during_your_command,omitempty"`
}

// sourceHistoryOpenWindow is a command window whose effects may not be listed yet.
type sourceHistoryOpenWindow struct {
	ID      string `json:"id"`
	Running bool   `json:"running"`
}

func (t *SourceHistoryTool) runOverview(ctx context.Context, tctx tools.ToolContext) (string, error) {
	ledger, err := historyLedger(tctx)
	if err != nil {
		return "", err
	}
	resp := sourceHistoryResponse{Mode: "overview"}
	floor, known, err := sessionWindow(ctx, tctx, ledger)
	if err != nil {
		return "", err
	}
	if !known {
		resp.Note = sourceHistoryWindowUnknownNote
		return marshalSourceHistory("", resp)
	}
	rootBranches, err := callerRootBranches(tctx)
	if err != nil {
		return "", fmt.Errorf("source history overview branch: %w", err)
	}
	branch, sameBranch := sharedBranch(rootBranches)

	// Listing on both sides of the read keeps a row from claiming settled when
	// its window opened after the first listing, and keeps the floor note when a
	// window settled during the read.
	listWindows := func() []sourceledger.SessionWindow {
		if ledger.Windows == nil {
			return nil
		}
		return ledger.Windows.OpenSessionWindows(tctx.Identity.ProjectID, tctx.Identity.SessionID)
	}
	settleBudget := t.settleBudget
	if settleBudget == 0 {
		settleBudget = tools.WindowSettleBudget
	}
	tools.AwaitClosingWindows(ctx, listWindows(), settleBudget)
	beforeRead := listWindows()
	effects, err := ledger.Files.EffectsBetweenOnBranch(ctx, tctx.Identity.ProjectID, rootBranches, floor, 0, overviewEffectCap+1)
	if err != nil {
		return "", fmt.Errorf("source history overview: %w", err)
	}
	open := mergeOpenWindows(beforeRead, listWindows())

	resp.Recorded = true
	resp.Since = &sourceHistorySince{Ordinal: floor}
	resp.Branch = branch.String()
	if len(effects) > overviewEffectCap {
		effects = effects[:overviewEffectCap]
		resp.Truncated = true
	}
	resp.Files, err = overviewFiles(ctx, ledger.Files, effects, overviewRowFacts{
		projectID: tctx.Identity.ProjectID, roots: tctx.Source.Roots, sessionID: tctx.Identity.SessionID,
		statesWindows: ledger.Windows != nil, open: open, namesBranch: !sameBranch,
		activeRootID: tctx.Source.ActiveRootID,
	})
	if err != nil {
		return "", fmt.Errorf("source history overview path: %w", err)
	}
	var notes []string
	if tctx.Source.SourceWorkspaceKind == api.SourceWorkspaceKindWorker {
		if !t.addTreeChanges(ctx, tctx, ledger, rootBranches, effects, &resp) {
			notes = append(notes, overviewTreeUnknownNote)
		}
	}
	resp.Count = len(resp.Files)
	resp.OpenWindows = open
	if len(resp.Files) == 0 {
		notes = append(notes, overviewNoRowsNote)
	}
	if resp.Truncated {
		notes = append(notes, overviewTruncatedNote)
	}
	if len(open) > 0 {
		notes = append(notes, overviewOpenWindowsNote)
	}
	resp.Note = strings.Join(notes, "; ")
	return marshalSourceHistory("", resp)
}

// callerRootBranches names the branch the caller records on in each root. A
// worktree binding can place roots on different branches.
func callerRootBranches(tctx tools.ToolContext) (map[string]sourcebranch.ID, error) {
	branches := make(map[string]sourcebranch.ID, len(tctx.Source.Roots))
	for _, root := range tctx.Source.Roots {
		branch, err := tctx.SourceBranch(root.ID)
		if err != nil {
			return nil, err
		}
		branches[root.ID] = branch
	}
	return branches, nil
}

func sharedBranch(rootBranches map[string]sourcebranch.ID) (sourcebranch.ID, bool) {
	var shared sourcebranch.ID
	first := true
	for _, branch := range rootBranches {
		if !first && branch != shared {
			return sourcebranch.Trunk, false
		}
		shared, first = branch, false
	}
	return shared, true
}

// mergeOpenWindows keeps every window seen open around the read. A later listing
// wins because a running window can start closing between the two.
func mergeOpenWindows(before, after []sourceledger.SessionWindow) []sourceHistoryOpenWindow {
	running := make(map[string]bool, len(before)+len(after))
	order := make([]string, 0, len(before)+len(after))
	for _, listed := range [][]sourceledger.SessionWindow{before, after} {
		for _, window := range listed {
			if _, seen := running[window.ID]; !seen {
				order = append(order, window.ID)
			}
			running[window.ID] = !window.Closing
		}
	}
	out := make([]sourceHistoryOpenWindow, 0, len(order))
	for _, id := range order {
		out = append(out, sourceHistoryOpenWindow{ID: id, Running: running[id]})
	}
	return out
}

type overviewRowFacts struct {
	projectID     string
	roots         []projectroot.RootRef
	sessionID     string
	statesWindows bool
	open          []sourceHistoryOpenWindow
	namesBranch   bool
	activeRootID  string
}

// overviewFiles groups effects per file. Effects arrive newest first, so the
// first effect per file is its newest and first-seen order is newest-file first.
func overviewFiles(ctx context.Context, files tools.SourceFileHistory, effects []sourceledger.Effect, facts overviewRowFacts) ([]sourceHistoryOverviewFile, error) {
	open := make(map[string]bool, len(facts.open))
	for _, window := range facts.open {
		open[window.ID] = true
	}
	rows := make(map[string]int, len(effects))
	var out []sourceHistoryOverviewFile
	for _, effect := range effects {
		if at, seen := rows[effect.FileID]; seen {
			out[at].Effects++
			continue
		}
		rows[effect.FileID] = len(out)
		rootID, path, err := overviewRowPath(ctx, files, facts.projectID, effect)
		if err != nil {
			return nil, err
		}
		row := sourceHistoryOverviewFile{
			Path:      projectroot.DisplayPath(facts.roots, facts.activeRootID, rootID, path),
			Op:        string(effect.Op),
			Actor:     string(effect.ActorClassFor(facts.sessionID)),
			Detail:    effect.ActorDisplay(facts.sessionID),
			At:        effect.TS.UTC().Format(sourceview.StampTimeLayout),
			Effects:   1,
			VersionID: effect.AfterVersionID,

			DuringYourCommand: effect.DuringCommandOf(facts.sessionID),
		}
		if facts.namesBranch {
			row.Branch = effect.BranchID.String()
		}
		if facts.statesWindows && effect.CommandWindowID != "" {
			settled := !open[effect.CommandWindowID]
			row.WindowID, row.Settled = effect.CommandWindowID, &settled
		}
		out = append(out, row)
	}
	return out, nil
}

// overviewRowPath names where the file is now, since a directory move rewrites no
// descendant's effects. A deleted file is named where it last was.
func overviewRowPath(ctx context.Context, files tools.SourceFileHistory, projectID string, effect sourceledger.Effect) (rootID, path string, err error) {
	head, err := files.ResolveHeadByFile(ctx, projectID, effect.BranchID, effect.FileID)
	if errors.Is(err, sourceledger.ErrHistoryNotFound) {
		return effect.RootID, effect.Path, nil
	}
	if err != nil {
		return "", "", err
	}
	if head.State == "absent" {
		return effect.RootID, effect.Path, nil
	}
	return head.RootID, head.Path, nil
}
