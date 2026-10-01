import type { ProjectSummary } from "../project/project-summary.ts";

export type HomeSection = "recents" | "drafts" | "starred" | "all";

export const DEFAULT_HOME_SECTION: HomeSection = "recents";

/** Page size for the All projects list view. */
export const ALL_PROJECTS_PAGE_SIZE = 12;

/**
 * Card-grid sections (Recents, Drafts, Starred) render at most this many
 * cards; the overflow lives in the paged All projects table.
 */
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

/** Recents and All list saved projects; Starred honors explicit stars in either state. */
export function projectsForSection(
  summaries: ProjectSummary[],
  section: HomeSection,
): ProjectSummary[] {
  const saved = summaries.filter((s) => !s.isDraft);
  switch (section) {
    case "drafts":
      return summaries.filter((s) => s.isDraft);
    case "starred":
      return summaries.filter((s) => s.starred);
    case "all":
    case "recents":
    default:
      return saved;
  }
}

/**
 * Home keeps the rows it showed while a submitted idea becomes a project. The
 * draft record lands in the registry before the shell leaves Home, and a card
 * or count that appears for it would paint the transition twice.
 */
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
