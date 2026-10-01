/** Selection menus add labels and progress copy to declared contribution commands. */

export const SELECTION_VERB_IDS = [
  "explain",
  "summarize",
  "document",
  "addTest",
  "rewriteStructurally",
  "fixFinding",
] as const;

export type SelectionVerbId = (typeof SELECTION_VERB_IDS)[number];

export type SelectionVerbDef = {
  id: SelectionVerbId;
  /** Stock contribution command whose editor action this verb runs. */
  commandId: string;
  label: string;
  /** Context-menu label. */
  menuLabel: string;
  testId: string;
  /** Progress line shown while the turn runs. */
  progressLabel: string;
  /** Hides the row unless a finding annotates the range. */
  requiresFinding?: boolean;
};

export const SELECTION_VERBS: readonly SelectionVerbDef[] = [
  {
    id: "explain",
    commandId: "painted-wolf/platform:editor-verb-explain",
    label: "Explain in context",
    menuLabel: "Explain in context",
    progressLabel: "Explaining selection…",
    testId: "files-ctx-editor-explain",
  },
  {
    id: "summarize",
    commandId: "painted-wolf/platform:editor-verb-summarize",
    label: "Give me the gist",
    menuLabel: "Give me the gist",
    progressLabel: "Summarizing selection…",
    testId: "files-ctx-editor-summarize",
  },
  {
    id: "document",
    commandId: "painted-wolf/platform:editor-verb-document",
    label: "Document selection",
    menuLabel: "Document this",
    progressLabel: "Documenting selection…",
    testId: "files-ctx-editor-document",
  },
  {
    id: "addTest",
    commandId: "painted-wolf/platform:editor-verb-add-test",
    label: "Add test for selection",
    menuLabel: "Add test",
    progressLabel: "Adding test…",
    testId: "files-ctx-editor-add-test",
  },
  {
    id: "rewriteStructurally",
    commandId: "painted-wolf/platform:editor-verb-rewrite-structurally",
    label: "Rewrite structurally",
    menuLabel: "Rewrite structurally",
    progressLabel: "Rewriting structurally…",
    testId: "files-ctx-editor-rewrite",
  },
  {
    id: "fixFinding",
    commandId: "painted-wolf/platform:editor-verb-fix-finding",
    label: "Fix this finding",
    menuLabel: "Fix this finding",
    progressLabel: "Fixing finding…",
    testId: "files-ctx-editor-fix-finding",
    requiresFinding: true,
  },
] as const;

export function selectionVerbById(id: SelectionVerbId): SelectionVerbDef {
  const v = SELECTION_VERBS.find((x) => x.id === id);
  if (!v) throw new Error(`unknown selection verb: ${id}`);
  return v;
}

export function askSelectionVerbs(args: {
  hasFinding: boolean;
}): SelectionVerbDef[] {
  return SELECTION_VERBS.filter((v) => {
    if (v.requiresFinding && !args.hasFinding) return false;
    return true;
  });
}
