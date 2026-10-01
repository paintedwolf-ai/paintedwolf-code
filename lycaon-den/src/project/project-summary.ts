import type { Project, ProjectRoot } from "../api/types.ts";
import { formatRelativeTime } from "../time/time-copy.ts";

function toTilde(path: string): string {
  const unixHome = path.match(/^\/home\/[^/]+(\/.*)?$/);
  if (unixHome) {
    return `~${unixHome[1] ?? ""}`;
  }
  const macHome = path.match(/^\/Users\/[^/]+(\/.*)?$/);
  if (macHome) {
    return `~${macHome[1] ?? ""}`;
  }
  return path;
}

export type ProjectSummary = {
  id: string;
  displayName: string;
  folders: string[];
  primaryFolder: string | null;
  folderLabel: string;
  sessionCount: number;
  chatCountLabel: string;
  lastActivityLabel: string;
  /** Epoch ms for sorting; null when never active. */
  lastActivityAtMs: number | null;
  starred: boolean;
  isDraft: boolean;
  coverArtifactId: string | null;
  coverRootSessionId: string | null;
};

/** Display name for a project the sidecar has not named, such as a fresh draft. */
export const UNTITLED_PROJECT_NAME = "Untitled";

export function folderLabel(roots: ProjectRoot[]): string {
  if (roots.length === 0) return "No folder";
  if (roots.length === 1) return toTilde(roots[0]!.path);
  const primary = roots.find((r) => r.is_primary);
  if (!primary) return `${roots.length} folders`;
  return `${toTilde(primary.path)} +${roots.length - 1}`;
}

export function toProjectSummary(project: Project): ProjectSummary {
  const attachedRoots = project.roots.filter((root) => root.kind === "attached");
  const lastActivityAt = project.last_activity_at
    ? Date.parse(project.last_activity_at)
    : NaN;
  return {
    id: project.id,
    displayName: project.name?.trim() || UNTITLED_PROJECT_NAME,
    folders: attachedRoots.map((r) => r.path),
    primaryFolder: attachedRoots.find((r) => r.is_primary)?.path ?? null,
    folderLabel: folderLabel(attachedRoots),
    sessionCount: project.session_count,
    chatCountLabel: `${project.session_count} chat${project.session_count === 1 ? "" : "s"}`,
    lastActivityLabel:
      Number.isFinite(lastActivityAt) && lastActivityAt > 0
        ? formatRelativeTime(lastActivityAt)
        : "new",
    lastActivityAtMs:
      Number.isFinite(lastActivityAt) && lastActivityAt > 0 ? lastActivityAt : null,
    starred: project.starred,
    isDraft: project.is_draft,
    coverArtifactId: project.cover_artifact_id?.trim() || null,
    coverRootSessionId: project.cover_root_session_id?.trim() || null,
  };
}
