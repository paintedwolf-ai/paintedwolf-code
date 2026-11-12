import type { ProjectSummary } from "../project/project-summary.ts";

export type HomeSection = "recents" | "drafts" | "starred" | "all";

export const DEFAULT_HOME_SECTION: HomeSection = "recents";

export const ALL_PROJECTS_PAGE_SIZE = 12;

/** Card grids cap at this count; overflow links to All projects. */
export const HOME_GRID_CAP = 12;

export type ProjectListSortKey = "name" | "updated";
export type ProjectListSortDir = "asc" | "desc";

export function defaultProjectListSortDir(key: ProjectListSortKey): ProjectListSortDir {
  return key === "updated" ? "desc" : "asc";
}

export function nextProjectListSortDir(
  currentKey: ProjectListSortKey,
  nextKey: ProjectListSortKey,
  currentDir: ProjectListSortDir,
): ProjectListSortDir {
  if (currentKey === nextKey) {
    return currentDir === "asc" ? "desc" : "asc";
  }
  return defaultProjectListSortDir(nextKey);
}

export function sortProjectSummaries(
  rows: ProjectSummary[],
  key: ProjectListSortKey,
  dir: ProjectListSortDir,
): ProjectSummary[] {
  const mul = dir === "asc" ? 1 : -1;
  return [...rows].sort((a, b) => {
    if (key === "name") {
      const byName = a.displayName.localeCompare(b.displayName, undefined, {
        sensitivity: "base",
      });
      if (byName !== 0) return mul * byName;
      return a.id.localeCompare(b.id);
    }
    const aMs = a.lastActivityAtMs ?? 0;
    const bMs = b.lastActivityAtMs ?? 0;
    if (aMs !== bMs) return mul * (aMs - bMs);
    const byName = a.displayName.localeCompare(b.displayName, undefined, {
      sensitivity: "base",
    });
    if (byName !== 0) return byName;
    return a.id.localeCompare(b.id);
  });
}

/** Recents lists saved projects; Drafts and Starred filter by state; All includes every project. */
export function projectsForSection(
  summaries: ProjectSummary[],
  section: HomeSection,
): ProjectSummary[] {
  switch (section) {
    case "drafts":
      return summaries.filter((s) => s.isDraft);
    case "starred":
      return summaries.filter((s) => s.starred);
    case "all":
      return summaries;
    case "recents":
    default:
      return summaries.filter((s) => !s.isDraft);
  }
}

/** Holds the prior rows while an idea materializes so the new draft does not flash on Home before chat opens. */
export function retainedHomeSummaries(
  previous: ProjectSummary[] | undefined,
  next: ProjectSummary[],
  materializing: boolean,
): ProjectSummary[] {
  return materializing && previous ? previous : next;
}

export function draftCount(summaries: ProjectSummary[]): number {
  return summaries.reduce((n, s) => (s.isDraft ? n + 1 : n), 0);
}

export function sectionTitle(section: HomeSection): string {
  switch (section) {
    case "drafts":
      return "Drafts";
    case "starred":
      return "Starred";
    case "all":
      return "All projects";
    case "recents":
    default:
      return "Recents";
  }
}

export function sectionEmptyHint(section: HomeSection): string {
  switch (section) {
    case "drafts":
      return "No drafts. Start one by describing an idea above.";
    case "starred":
      return "No starred projects yet. Star a project to pin it here.";
    default:
      return "No projects yet.";
  }
}
