const ISSUE_COPY: Record<string, string> = {
  unrecorded_change: "This file changed without complete version authorship. Review its history before restoring it.",
  workflow_binding_changed: "This turn changed the workflow’s blueprint binding. Rewind cannot safely restore that workflow state.",
  history_limit: "This rewind exceeds the retained history limit. Choose a more recent ask.",
  capture_incomplete: "This turn exceeded the capture limit. Rewind cannot verify every affected file.",
  command_authorship_unavailable: "These changes were observed during a command, and their authorship is uncertain. Review the file versions before restoring them.",
  shared_contribution: "This change includes another contributor’s work. Review its file history before choosing what to restore.",
  unsaved_agent_change: "This turn has an unsaved editor change. Save or resolve the document first.",
  version_unavailable: "The exact earlier file contents are unavailable. This rewind cannot restore the complete turn.",
  intervening_change: "Another contributor changed this file between the selected turns. Review its file history first.",
  later_change: "This file has newer recorded changes. Review its file history first.",
  working_file_changed: "The file has changed since this turn. Preserve or resolve those edits first.",
  workspace_unavailable: "The original workspace is unavailable. Reattach it before rewinding.",
  destination_occupied: "The original name is occupied by another file. Resolve that file before rewinding.",
  document_has_unsaved_changes: "The open document has unsaved or conflicting edits. Save or resolve them first.",
};

export function rewindIssueCopy(code: string): string {
  return ISSUE_COPY[code] ?? "The host cannot safely restore this file. Review its history before continuing.";
}
