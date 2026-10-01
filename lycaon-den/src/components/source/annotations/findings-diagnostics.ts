/** Maps security findings to marked ranges and keyboard navigation. */

import {
  nextDiagnostic,
  previousDiagnostic,
  setDiagnostics,
  type Action,
  type Diagnostic,
} from "@codemirror/lint";
import { EditorView, type Command } from "@codemirror/view";
import type { Text } from "@codemirror/state";
import type { FindingLevel, SecurityFinding } from "../../../api/types.ts";
import {
  findingLevelLabel,
  findingLocationsIn,
  type FindingLocationHit,
} from "./knowing-gutter-model.ts";

export type FindingDiagnosticHandlers = {
  /** Open the finding in the scans surface. */
  onOpen?: (finding: SecurityFinding) => void;
  /** Run the fix command for the selected finding span. */
  onFix?: () => void;
};

/** Unscored findings use the lowest diagnostic severity. */
function diagnosticSeverity(level: FindingLevel): Diagnostic["severity"] {
  switch (level) {
    case "critical":
    case "high":
      return "error";
    case "medium":
      return "warning";
    case "low":
      return "info";
    case "info":
    case "unknown":
      return "hint";
  }
}

/** Columns are 1-based with an exclusive end; absent columns exclude indentation. */
function rangeOf(hit: FindingLocationHit, doc: Text): { from: number; to: number } | null {
  if (hit.line > doc.lines) return null;
  const startLine = doc.line(hit.line);
  const endLineNumber = Math.min(
    Math.max(hit.location.end_line ?? hit.line, hit.line),
    doc.lines,
  );
  const endLine = doc.line(endLineNumber);

  const startColumn = hit.location.start_column;
  const endColumn = hit.location.end_column;
  if (startColumn == null) {
    const indent = startLine.text.length - startLine.text.trimStart().length;
    const from = startLine.from + indent;
    return endLine.to > from ? { from, to: endLine.to } : null;
  }

  const from = Math.min(
    startLine.from + Math.max(0, startColumn - 1),
    startLine.to,
  );
  const to =
    endColumn == null
      ? endLine.to
      : Math.min(endLine.from + Math.max(0, endColumn - 1), endLine.to);
  // Extend zero-width reports to the line end for a visible mark.
  if (to <= from) return endLine.to > from ? { from, to: endLine.to } : null;
  return { from, to };
}

function actionsFor(
  finding: SecurityFinding,
  handlers: FindingDiagnosticHandlers,
): Action[] {
  const actions: Action[] = [];
  if (handlers.onOpen) {
    actions.push({
      name: "Open finding",
      apply: () => handlers.onOpen?.(finding),
    });
  }
  if (handlers.onFix) {
    actions.push({
      name: "Fix this finding",
      apply: (view, from, to) => {
        // The fix command identifies the finding from the selected span.
        view.dispatch({ selection: { anchor: from, head: to } });
        view.focus();
        handlers.onFix?.();
      },
    });
  }
  return actions;
}

/** Diagnostics for every finding location that lands in this buffer. */
export function findingDiagnostics(
  path: string,
  findings: readonly SecurityFinding[],
  doc: Text,
  handlers: FindingDiagnosticHandlers = {},
): Diagnostic[] {
  const out: Diagnostic[] = [];
  for (const hit of findingLocationsIn(path, findings)) {
    const range = rangeOf(hit, doc);
    if (!range) continue;
    const { finding } = hit;
    out.push({
      ...range,
      severity: diagnosticSeverity(finding.level),
      markClass: `cm-den-finding cm-den-finding-${finding.level}`,
      source: `${finding.tool.name} · ${finding.rule_id}`,
      message: `${findingLevelLabel(finding.level)}: ${finding.message}`,
      actions: actionsFor(finding, handlers),
    });
  }
  // Keyboard navigation follows document order.
  out.sort((a, b) => a.from - b.from || a.to - b.to);
  return out;
}

/** Replace the buffer's finding diagnostics. An empty list clears them. */
export function setFindingDiagnostics(
  view: EditorView,
  path: string,
  findings: readonly SecurityFinding[],
  handlers: FindingDiagnosticHandlers = {},
): void {
  view.dispatch(
    setDiagnostics(
      view.state,
      findingDiagnostics(path, findings, view.state.doc, handlers),
    ),
  );
}

/** Move to the next finding and announce it. */
export const goToNextFinding: Command = (view) => {
  if (!nextDiagnostic(view)) return false;
  announceFindingAtCursor(view);
  return true;
};

export const goToPreviousFinding: Command = (view) => {
  if (!previousDiagnostic(view)) return false;
  announceFindingAtCursor(view);
  return true;
};

function announceFindingAtCursor(view: EditorView): void {
  const line = view.state.doc.lineAt(view.state.selection.main.from);
  view.dispatch({
    effects: EditorView.announce.of(`Finding on line ${line.number}`),
  });
}
