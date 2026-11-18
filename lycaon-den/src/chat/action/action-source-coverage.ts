import type { ActionFact } from "../../api/types.ts";
import { BOUNDARY_REASONS, GAP_REASONS } from "../../files/source/source-command-window.ts";

type FolderCoverage = NonNullable<ActionFact["source_coverage"]>[number];

export function activityCoverageNotices(coverage: readonly FolderCoverage[] = []): string[] {
  const notices = new Set<string>();
  for (const entry of coverage) {
    const folder = entry.folder === "/" && entry.reason === "unconfined" ? "Everything outside the project" : entry.folder;
    if (entry.coverage === "not_observed") {
      const reason = entry.reason ? GAP_REASONS[entry.reason] : "no reason was recorded";
      notices.add(`${folder} was not observed: ${reason}.`);
    } else if (entry.coverage === "partial") {
      const skipped = (entry.boundaries ?? []).map(({ path, reason }) =>
        `${path === "." ? folder : path} (${BOUNDARY_REASONS[reason]})`);
      if (entry.omitted_boundaries) skipped.push(`${entry.omitted_boundaries.toLocaleString()} more`);
      notices.add(skipped.length
        ? `Not every folder in ${folder} was compared. Skipped ${skipped.join(", ")}.`
        : `Not every folder in ${folder} was compared.`);
    }
  }
  return [...notices];
}
