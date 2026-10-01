import type {
  SourceFileCommit,
  SourceGitChange,
  SourceGitHistoryState,
} from "../../api/types.ts";
import type { FileVersionHistory } from "./file-version.ts";

/** True when commit history produced a definitive result. */
export function gitLaneSettled(state: SourceGitHistoryState): boolean {
  switch (state) {
    case "available":
    case "not_tracked":
    case "no_repository":
      return true;
    case "not_requested":
    case "timed_out":
    case "failed":
      return false;
  }
}

/** Copy for a Git lane that could not answer; null once it answered or was not asked. */
export function gitLaneNotice(state: SourceGitHistoryState): string | null {
  switch (state) {
    case "timed_out":
      return "Git history took too long to read in this repository. Earlier commits may exist.";
    case "failed":
      return "Couldn’t read Git history for this file. Earlier commits may exist.";
    default:
      return null;
  }
}

/** A lineage commit plus its predecessor's blob, the preview's before side. */
export type FileHistoryCommit = {
  commit: SourceFileCommit;
  previousBlobOid: string | null;
};

type FileHistoryRows = {
  rows: FileHistoryRow[];
  /** Commits whose bytes the working file holds. */
  currentCommits: readonly SourceFileCommit[];
};

export type FileHistoryRow =
  | {
    kind: "version";
    version: FileVersionHistory["versions"][number];
    /** Commits whose blob equals this state — one row, both identities. */
    commits: readonly SourceFileCommit[];
  }
  | {
    kind: "arrival";
    change: SourceGitChange;
    commits: readonly FileHistoryCommit[];
  }
  | { kind: "boundary"; trackedSince: string }
  | { kind: "commit"; entry: FileHistoryCommit };

export function shortCommit(commit: string): string {
  return commit.trim().slice(0, 7);
}

export function fileCommitMeta(commit: SourceFileCommit): string {
  const parts: string[] = [];
  const author = commit.author_name?.trim() ?? "";
  if (author) parts.push(author);
  const subject = commit.subject.trim();
  if (subject) parts.push(subject);
  if (!commit.blob_oid) parts.push("removed the file");
  return parts.join(" · ");
}

export function buildFileHistoryRows(
  history: FileVersionHistory,
): FileHistoryRows {
  const matched = new Map<string, SourceFileCommit[]>();
  const arrivalGroups = new Map<string, FileHistoryCommit[]>();
  const gitEra: FileHistoryCommit[] = [];
  const commits = history.commits;
  for (let index = 0; index < commits.length; index += 1) {
    const commit = commits[index]!;
    const previous = commits
      .slice(index + 1)
      .find((older) => (older.blob_oid ?? "") !== "");
    const entry: FileHistoryCommit = {
      commit,
      previousBlobOid: previous?.blob_oid ?? null,
    };
    const versionID = commit.matches_version_id?.trim() ?? "";
    if (versionID) {
      const held = matched.get(versionID);
      if (held) held.push(commit);
      else matched.set(versionID, [commit]);
      continue;
    }
    const arrivalID = commit.arrival_git_change_id?.trim() ?? "";
    if (arrivalID) {
      const held = arrivalGroups.get(arrivalID);
      if (held) held.push(entry);
      else arrivalGroups.set(arrivalID, [entry]);
      continue;
    }
    gitEra.push(entry);
  }
  const arrivalsById = new Map(history.arrivals.map((change) => [change.id, change]));

  const rows: FileHistoryRow[] = [];
  const placedArrivals = new Set<string>();
  let currentCommits: readonly SourceFileCommit[] = [];
  let baselineFolded = false;
  for (const version of history.versions) {
    // Current includes an unchanged tracking baseline.
    const isBaseline = !version.op && !version.origin;
    const restatesCurrent = history.current?.state === "content" &&
      version.state === "content" &&
      !!version.content_sha256 &&
      version.content_sha256 === history.current.sha256;
    if (isBaseline && restatesCurrent && !baselineFolded) {
      baselineFolded = true;
      currentCommits = matched.get(version.id) ?? [];
      continue;
    }
    rows.push({
      kind: "version",
      version,
      commits: matched.get(version.id) ?? [],
    });
    const movementID = version.git_change?.id ?? "";
    const group = movementID ? arrivalGroups.get(movementID) : undefined;
    if (group && version.git_change && !placedArrivals.has(movementID)) {
      placedArrivals.add(movementID);
      rows.push({ kind: "arrival", change: version.git_change, commits: group });
    }
  }
  for (const [movementID, group] of arrivalGroups) {
    if (placedArrivals.has(movementID)) continue;
    const change = arrivalsById.get(movementID);
    if (!change) {
      // Commits remain visible without an arrival summary.
      gitEra.push(...group);
      continue;
    }
    rows.push({ kind: "arrival", change, commits: group });
  }
  // Commit rows on later pages also require the history boundary.
  const trackedSince = history.trackedSince?.trim() ?? "";
  if (trackedSince && (gitEra.length > 0 || history.nextGitCursor !== null)) {
    rows.push({ kind: "boundary", trackedSince });
  }
  for (const entry of gitEra) {
    rows.push({ kind: "commit", entry });
  }
  return { rows, currentCommits };
}
