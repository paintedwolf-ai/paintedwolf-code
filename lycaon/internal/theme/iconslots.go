package theme

import "sort"

// The host defines the closed icon slot vocabulary.

// IconPaint is the slot's fixed paint mode.
type IconPaint string

const (
	// PaintStroke outlines the geometry. Most glyphs.
	PaintStroke IconPaint = "stroke"
	// PaintFill fills it. Dots, carets, and solid markers.
	PaintFill IconPaint = "fill"
)

// IconSlot is one host-defined place a glyph appears.
type IconSlot struct {
	// ID is the author-facing slot name.
	ID string
	// Description is one line for the generated schema and docs.
	Description string
	// Group buckets the slot in the generated reference.
	Group string
	Paint IconPaint
	// Weight is the base stroke width in grid units.
	Weight float64
}

// IconViewBox is the shared authoring grid.
const IconViewBox = "0 0 16 16"

const (
	weightHairline = 1.1
	weightRegular  = 1.25
	weightStrong   = 1.5
)

var iconSlots = []IconSlot{
	// Shell — the rail and the window chrome.
	{ID: "search", Group: "shell", Description: "Search.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "recents", Group: "shell", Description: "Recently opened.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "drafts", Group: "shell", Description: "Draft sessions.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "starred", Group: "shell", Description: "Starred projects.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "all-projects", Group: "shell", Description: "The full project list.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "sidebar-collapse", Group: "shell", Description: "Collapse the nav rail.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "sidebar-expand", Group: "shell", Description: "Expand the nav rail.", Paint: PaintStroke, Weight: weightRegular},

	{ID: "back", Group: "shell", Description: "Return to the previous list.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "project-folder", Group: "shell", Description: "A project's folder.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "project-switcher", Group: "shell", Description: "Switch project.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "grip", Group: "shell", Description: "Drag handle.", Paint: PaintFill},

	{ID: "new-session", Group: "shell", Description: "Start a chat.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "new-project", Group: "shell", Description: "Add a project.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "session", Group: "shell", Description: "A chat session.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "surface", Group: "shell", Description: "A panel or stage.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "new-window", Group: "shell", Description: "Open in a new window.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "terminal", Group: "shell", Description: "A shell prompt.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "shortcuts", Group: "shell", Description: "Keyboard shortcuts.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "launcher", Group: "shell", Description: "Jump to anything.", Paint: PaintStroke, Weight: weightRegular},

	// Stages — the Context destinations.
	{ID: "stage-files", Group: "stage", Description: "Files stage.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "stage-cost", Group: "stage", Description: "Cost stage.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "stage-artifacts", Group: "stage", Description: "Artifacts stage.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "stage-blueprints", Group: "stage", Description: "Blueprints stage.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "stage-extensions", Group: "stage", Description: "Extensions stage.", Paint: PaintStroke, Weight: weightHairline},

	// Chat rail — the tabs above a session.
	{ID: "progress", Group: "chat", Description: "Progress checklist.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "workflows", Group: "chat", Description: "Workflow runs.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "git", Group: "chat", Description: "Source control.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "workers", Group: "chat", Description: "Workers in flight.", Paint: PaintStroke, Weight: weightRegular},

	// Workflow marks use the same slot vocabulary.
	{ID: "route", Group: "chat", Description: "Planning workflow.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "radar", Group: "chat", Description: "Reconnaissance workflow.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "bug", Group: "chat", Description: "Bug-hunting workflow.", Paint: PaintStroke, Weight: weightRegular},

	{ID: "queue", Group: "chat", Description: "Queued prompts.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "send-next", Group: "chat", Description: "Send the next queued prompt.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "pause", Group: "chat", Description: "Pause the queue.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "play", Group: "chat", Description: "Resume the queue.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "busy", Group: "chat", Description: "Work in flight.", Paint: PaintStroke, Weight: weightStrong},

	// Actions — what a row or a message offers.
	{ID: "copy", Group: "action", Description: "Copy to clipboard.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "check", Group: "action", Description: "Confirmation and completion.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "more", Group: "action", Description: "Overflow menu.", Paint: PaintFill},
	{ID: "edit", Group: "action", Description: "Edit or rename.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "pin", Group: "action", Description: "Pin.", Paint: PaintStroke, Weight: weightRegular},

	{ID: "chevron-down", Group: "disclosure", Description: "Open downward.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "chevron-up", Group: "disclosure", Description: "Open upward.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "unfold", Group: "disclosure", Description: "Show folded content in place.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "expand", Group: "disclosure", Description: "Fill the window.", Paint: PaintStroke, Weight: weightRegular},

	// Editor — the Files stage's own controls.
	{ID: "find", Group: "editor", Description: "Search within the open document.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "goto-line", Group: "editor", Description: "Jump to a line number.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "editor-editing", Group: "editor", Description: "The current document is editable here.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-version", Group: "editor", Description: "A historical document version.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-worker-draft", Group: "editor", Description: "A worker's isolated draft.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-read-only", Group: "editor", Description: "A file without owner-write permission.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-editing-elsewhere", Group: "editor", Description: "A document edited in another window.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-preview", Group: "editor", Description: "Rendered document preview.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-deleted", Group: "editor", Description: "A deleted current document.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-view-only", Group: "editor", Description: "A document available only for viewing.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "editor-restore", Group: "editor", Description: "Restore the selected historical version.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "editor-open-current", Group: "editor", Description: "Open the current project file from a draft or version.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "elevated-access", Group: "security", Description: "Revoke saved elevated access for the current chat.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "editor-unlock", Group: "editor", Description: "Make a read-only file editable.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "editor-takeover", Group: "editor", Description: "Move the editing lease to this window.", Paint: PaintStroke, Weight: weightStrong},

	{ID: "file", Group: "editor", Description: "A file in the tree.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "folder", Group: "editor", Description: "A folder in the tree.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "folder-open", Group: "editor", Description: "An expanded folder in the tree.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "new-file", Group: "editor", Description: "Create a file.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "new-folder", Group: "editor", Description: "Create a folder.", Paint: PaintStroke, Weight: weightRegular},

	// Result kinds.
	{ID: "code", Group: "result", Description: "A code hit.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "evidence", Group: "result", Description: "A gathered document.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "claim", Group: "result", Description: "An assertion the agent made.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "tool", Group: "result", Description: "A tool call.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "file-group", Group: "result", Description: "A summary of grouped file changes.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "diff", Group: "result", Description: "A comparison of what changed.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "artifact", Group: "result", Description: "A produced artifact.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "outcome", Group: "result", Description: "A settled result.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "network", Group: "result", Description: "An observed connection.", Paint: PaintStroke, Weight: weightHairline},

	// Trust — the marks on consent and risk surfaces.
	{ID: "shield", Group: "trust", Description: "A capability boundary.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "high-risk", Group: "trust", Description: "A high-risk action.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "impact", Group: "trust", Description: "What an action will touch.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "external-content", Group: "trust", Description: "Content from outside the project.", Paint: PaintStroke, Weight: weightHairline},

	{ID: "settings", Group: "shell", Description: "Settings.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "dismiss", Group: "action", Description: "Dismiss or close.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "delete", Group: "action", Description: "Delete.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "link", Group: "action", Description: "An external link.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "escalate", Group: "action", Description: "Escalate to the coordinator.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "rewind", Group: "action", Description: "Rewind to an earlier turn.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "undo", Group: "action", Description: "Undo the last change.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "redo", Group: "action", Description: "Redo the undone change.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "walk", Group: "action", Description: "Walk file history in time order.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "walk-first", Group: "action", Description: "Move to the first Walk step.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "walk-previous", Group: "action", Description: "Move to the previous Walk step.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "walk-next", Group: "action", Description: "Move to the next Walk step.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "walk-latest", Group: "action", Description: "Move to the latest Walk step.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "walk-refresh", Group: "action", Description: "Show newly arrived walk steps.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "plus", Group: "action", Description: "Add.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "chain", Group: "action", Description: "Bind two items so they travel together.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "stop", Group: "action", Description: "Stop the run.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "arrow-up", Group: "action", Description: "Move to the previous item.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "arrow-down", Group: "action", Description: "Move to the next item.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "pending", Group: "action", Description: "Not started yet.", Paint: PaintStroke, Weight: weightStrong},

	{ID: "sparkle", Group: "shell", Description: "New from an idea.", Paint: PaintFill},
	{ID: "open-folder", Group: "shell", Description: "Open an existing folder.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "clone-repo", Group: "shell", Description: "Clone a repository.", Paint: PaintStroke, Weight: weightHairline},
	{ID: "upload", Group: "action", Description: "Send or submit.", Paint: PaintStroke, Weight: weightStrong},
	{ID: "scope", Group: "shell", Description: "The sidebar scope picker.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "scope-off", Group: "shell", Description: "The scope picker with scoping off.", Paint: PaintStroke, Weight: weightRegular},

	// Settings — the section marks.
	{ID: "settings-general", Group: "settings", Description: "General section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-providers", Group: "settings", Description: "Model providers section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-approvals", Group: "settings", Description: "Approvals section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-budgets", Group: "settings", Description: "Budgets section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-trust", Group: "settings", Description: "Trust section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-secrets", Group: "settings", Description: "Secrets section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-mcp", Group: "settings", Description: "MCP section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-scanners", Group: "settings", Description: "Scanners section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-web-research", Group: "settings", Description: "Web research section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-host-resources", Group: "settings", Description: "Host resources section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-tests", Group: "settings", Description: "Provider tests section.", Paint: PaintStroke, Weight: weightRegular},
	{ID: "settings-advanced", Group: "settings", Description: "Advanced section.", Paint: PaintStroke, Weight: weightRegular},
}

var iconSlotByID = func() map[string]IconSlot {
	out := make(map[string]IconSlot, len(iconSlots))
	for _, slot := range iconSlots {
		out[slot.ID] = slot
	}
	return out
}()

// IconSlots returns the vocabulary in declaration order (grouped).
func IconSlots() []IconSlot { return append([]IconSlot(nil), iconSlots...) }

// IconSlotByID looks one up.
func IconSlotByID(id string) (IconSlot, bool) {
	slot, ok := iconSlotByID[id]
	return slot, ok
}

// IconSlotIDs returns the ids, sorted, for diagnostics that list them.
func IconSlotIDs() []string {
	out := make([]string, 0, len(iconSlots))
	for _, slot := range iconSlots {
		out = append(out, slot.ID)
	}
	sort.Strings(out)
	return out
}
