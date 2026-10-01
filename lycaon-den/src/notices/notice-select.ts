import type { AppNotice } from "./notice-model.ts";
import type { NoticeIndex } from "./notice-store.ts";
import {
  noticeScopeKey,
  sessionNoticeScopeKey,
  APP_SCOPE,
} from "./notice-scope.ts";

/** Installation-wide notices. */
export function selectAppNotices(index: NoticeIndex): readonly AppNotice[] {
  return index.get(noticeScopeKey(APP_SCOPE)) ?? [];
}

export type ProjectNoticeGroup = {
  projectId: string;
  notices: readonly AppNotice[];
};

/** Project groups, with the active project first and then newest first. */
export function selectProjectNoticeGroups(
  index: NoticeIndex,
  activeProjectId?: string,
): readonly ProjectNoticeGroup[] {
  const grouped = new Map<string, AppNotice[]>();
  for (const rows of index.values()) {
    for (const notice of rows) {
      if (notice.scope.kind !== "project") continue;
      const projectRows = grouped.get(notice.scope.projectId) ?? [];
      projectRows.push(notice);
      grouped.set(notice.scope.projectId, projectRows);
    }
  }

  return [...grouped.entries()]
    .map(([projectId, notices]) => ({ projectId, notices }))
    .sort((a, b) => {
      if (a.projectId === activeProjectId) return -1;
      if (b.projectId === activeProjectId) return 1;
      const aLatest = a.notices[a.notices.length - 1]?.createdAt ?? 0;
      const bLatest = b.notices[b.notices.length - 1]?.createdAt ?? 0;
      return bLatest - aLatest || a.projectId.localeCompare(b.projectId);
    });
}

/** Whether the shared stack has an app or project condition to render. */
export function hasSharedNotices(index: NoticeIndex): boolean {
  if (selectAppNotices(index).length > 0) return true;
  for (const rows of index.values()) {
    if (rows.some((notice) => notice.scope.kind === "project")) return true;
  }
  return false;
}

/** One chat's own notices. */
export function selectSessionNotices(
  index: NoticeIndex,
  sessionId: string | undefined,
): readonly AppNotice[] {
  if (!sessionId) return [];
  return index.get(sessionNoticeScopeKey(sessionId)) ?? [];
}
