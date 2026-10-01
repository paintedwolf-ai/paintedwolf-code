/** Host-declared display and lifecycle scope for a notice. */
export type NoticeScope =
  | { kind: "app" }
  | { kind: "project"; projectId: string }
  | { kind: "session"; projectId: string; sessionId: string };

export type NoticeScopeKind = NoticeScope["kind"];

/** Shared installation-wide scope. */
export const APP_SCOPE: NoticeScope = Object.freeze({ kind: "app" });

/** Session UUIDs identify their bucket without a project lookup. */
export function sessionNoticeScopeKey(sessionId: string): string {
  return `session:${sessionId}`;
}

/** Stable bucket key for a scope. */
export function noticeScopeKey(scope: NoticeScope): string {
  switch (scope.kind) {
    case "app":
      return "app";
    case "project":
      return `project:${scope.projectId}`;
    case "session":
      return sessionNoticeScopeKey(scope.sessionId);
  }
}

/** Widening order: lower is broader. */
const NOTICE_SCOPE_BREADTH: Record<NoticeScopeKind, number> = {
  app: 0,
  project: 1,
  session: 2,
};

/** True when `a` is at least as broad as `b`. */
function isAtLeastAsBroad(a: NoticeScope, b: NoticeScope): boolean {
  return NOTICE_SCOPE_BREADTH[a.kind] <= NOTICE_SCOPE_BREADTH[b.kind];
}

/** Returns the broader scope without narrowing. */
export function widenNoticeScope(a: NoticeScope, b: NoticeScope): NoticeScope {
  return isAtLeastAsBroad(a, b) ? a : b;
}

/** Scope for one chat. */
export function sessionScope(projectId: string, sessionId: string): NoticeScope {
  return { kind: "session", projectId, sessionId };
}

/** Scope for one project. */
export function projectScope(projectId: string): NoticeScope {
  return { kind: "project", projectId };
}
