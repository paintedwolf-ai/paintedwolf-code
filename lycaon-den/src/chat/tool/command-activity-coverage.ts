import { BOUNDARY_REASONS, GAP_REASONS } from "../../files/source/source-command-window.ts";
import type { ToolPartView } from "./tool-part-model.ts";

/** One folder as a command result's source_window states it. */
type FolderFact = {
  folder?: unknown;
  coverage?: unknown;
  reason?: unknown;
  boundaries?: unknown;
  omitted_boundaries?: unknown;
};

/** The host folder that stands for everything beyond the project. */
const OUTSIDE_EVERYTHING = "/";

function folderCoverage(output: string | undefined): FolderFact[] {
  const text = output?.trim();
  if (!text?.startsWith("{")) return [];
  try {
    const parsed = JSON.parse(text) as { source_window?: { coverage?: unknown } };
    const coverage = parsed.source_window?.coverage;
    return Array.isArray(coverage) ? coverage.filter((entry): entry is FolderFact => !!entry && typeof entry === "object") : [];
  } catch {
    return [];
  }
}

function folderLabel(folder: unknown): string {
  if (typeof folder !== "string" || !folder) return "A folder";
  return folder === OUTSIDE_EVERYTHING ? "Everything outside the project" : folder;
}

function boundaryList(entry: FolderFact): string {
  const boundaries = Array.isArray(entry.boundaries) ? entry.boundaries as { path?: unknown; reason?: unknown }[] : [];
  const parts = boundaries.map((boundary) => {
    const reason = BOUNDARY_REASONS[boundary.reason as keyof typeof BOUNDARY_REASONS];
    const path = typeof boundary.path === "string" ? boundary.path : "";
    return reason ? `${path} (${reason})` : path;
  }).filter(Boolean);
  const more = typeof entry.omitted_boundaries === "number" && entry.omitted_boundaries > 0 ? entry.omitted_boundaries : 0;
  if (more) parts.push(`${more.toLocaleString()} more`);
  return parts.join(", ");
}

/**
 * What the command's observation could not see, one line per folder from
 * each call's source_window: partial folders with what was skipped and why,
 * and folders not observed with the reason. A complete folder says nothing.
 */
export function activityCoverageNotices(calls: readonly ToolPartView[]): string[] {
  const notices: string[] = [];
  for (const call of calls) {
    for (const entry of folderCoverage(call.output)) {
      const folder = folderLabel(entry.folder);
      let notice: string | undefined;
      if (entry.coverage === "not_observed") {
        const reason = GAP_REASONS[entry.reason as keyof typeof GAP_REASONS] ?? "no reason was recorded";
        notice = `${folder} was not observed: ${reason}.`;
      } else if (entry.coverage === "partial") {
        const skipped = boundaryList(entry);
        notice = skipped ? `Not every folder in ${folder} was compared; skipped ${skipped}.` : `Not every folder in ${folder} was compared.`;
      }
      if (notice && !notices.includes(notice)) notices.push(notice);
    }
  }
  return notices;
}
