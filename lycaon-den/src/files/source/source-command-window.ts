import type { components, SourceCommandWindow } from "../../api/types.ts";
import { isKnownOutsideRoot, outsideHostPath } from "./outside-namespace.ts";

type FolderCoverage = components["schemas"]["SourceWindowFolderCoverage"];
type GapReason = components["schemas"]["SourceWindowGapReason"];
type BoundaryReason = components["schemas"]["SourceWindowBoundaryReason"];

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

export const GAP_REASONS: Record<GapReason, string> = {
  unconfined: "the command ran without confinement, so it could have changed files anywhere",
  baseline_incomplete: "its starting state was not recorded before the command started",
  canceled: "the observation was canceled",
  io_error: "it could not be read",
  settlement_failed: "its changes could not be compared",
  time_budget: "the observation ran out of time",
};

export const BOUNDARY_REASONS: Record<BoundaryReason, string> = {
  scope: "outside the source scope",
  directory_cap: "too many entries in one folder",
  subtree_cap: "too many entries below it",
  walk_budget: "the entry limit was reached",
  unreadable: "could not be read",
};

/** One folder a command window did not fully compare. */
export type CommandCoverageNotice = {
  key: string;
  folder: string;
  /** What the reader cannot rely on; never "no changes". */
  text: string;
  /** For a partial folder, what was skipped and why. */
  skipped: readonly { path: string; reason: string }[];
  /** Skipped folders past the listed ones. */
  more: number;
};

function coverageFolder(entry: FolderCoverage, rootLabel?: (rootId: string) => string | undefined): string {
  if (isKnownOutsideRoot(entry.root_id)) {
    return entry.path ? outsideHostPath(entry.path) : "Everything outside the project";
  }
  const label = rootLabel?.(entry.root_id) ?? "The project folder";
  return entry.path ? `${label}/${entry.path}` : label;
}

/** The folders a window left uncompared, in the reader's terms. */
export function commandWindowCoverageNotices(
  window: SourceCommandWindow,
  rootLabel?: (rootId: string) => string | undefined,
): CommandCoverageNotice[] {
  const notices: CommandCoverageNotice[] = [];
  for (const entry of window.coverage ?? []) {
    if (entry.coverage === "complete") continue;
    const folder = coverageFolder(entry, rootLabel);
    const key = `${entry.root_id}:${entry.path}`;
    if (entry.coverage === "not_observed") {
      const reason = entry.reason ? GAP_REASONS[entry.reason] : "no reason was recorded";
      notices.push({ key, folder, text: `${folder} was not observed: ${reason}.`, skipped: [], more: 0 });
      continue;
    }
    notices.push({
      key,
      folder,
      text: `Not every folder in ${folder} was compared. Changes in these folders are unknown:`,
      skipped: (entry.boundaries ?? []).map((boundary) => ({
        path: boundary.path === "." ? folder : boundary.path,
        reason: BOUNDARY_REASONS[boundary.reason],
      })),
      more: entry.omitted_boundaries ?? 0,
    });
  }
  return notices;
}

/** Whether a window left some folder uncompared. */
export function commandWindowHasGaps(window: SourceCommandWindow): boolean {
  return (window.coverage ?? []).some((entry) => entry.coverage !== "complete");
}
