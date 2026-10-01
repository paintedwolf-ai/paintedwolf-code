import type { SourceCommandWindow } from "../../api/types.ts";

const COMMAND_LINE_LIMIT = 96;

/** One-line command text: whitespace collapsed, long lines cut. */
export function commandWindowLine(window: SourceCommandWindow): string {
  const line = window.command_line.replace(/\s+/g, " ").trim();
  if (line.length <= COMMAND_LINE_LIMIT) return line;
  return `${line.slice(0, COMMAND_LINE_LIMIT - 1)}…`;
}

/** Rail-style label for a command step: the tool that ran it. */
export function commandWindowAction(window: SourceCommandWindow): string {
  return window.tool_name.trim() || "command";
}

/** What the window claims: observation while it ran, not authorship. */
export function commandWindowObservation(): string {
  return "Files observed changing while this command ran. The command may not have written them all.";
}

/** Where the window stands, when that changes what the reader can expect. */
export function commandWindowStateNote(window: SourceCommandWindow): string | null {
  switch (window.state) {
    case "running":
      return "Still running; more files may land in this step.";
    case "interrupted":
      return "The app stopped before this command finished. Later changes are not in this step.";
    case "ended":
      return null;
  }
}

/** How the ending pass selected files, in the reader's terms. */
export function commandWindowAdmissionLabel(window: SourceCommandWindow): string | null {
  switch (window.admission_mode) {
    case "scope":
      return "Files inside the source scope";
    case "scope_bounded":
      return "Files inside the source scope; a budget left some folders unobserved";
    case undefined:
    case "":
      return null;
    default:
      return window.admission_mode;
  }
}

/** Effect-level test for a change a command window covered. */
export function effectObservedInCommand(effect: { cause: string }): boolean {
  return effect.cause === "command_window";
}
