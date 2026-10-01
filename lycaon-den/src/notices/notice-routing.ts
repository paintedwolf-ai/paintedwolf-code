import type { NoticeScope, NoticeScopeKind } from "./notice-scope.ts";
import { APP_SCOPE, projectScope, widenNoticeScope } from "./notice-scope.ts";
import { CLIENT_NOTICES, type ClientNoticeKind } from "./client-notices.generated.ts";

/** Den-authored notice scopes from the generated catalog. */
const CLIENT_NOTICE_SCOPE: Record<ClientNoticeKind, NoticeScopeKind> =
  Object.fromEntries(
    Object.entries(CLIENT_NOTICES).map(([kind, copy]) => [kind, copy.scope]),
  ) as Record<ClientNoticeKind, NoticeScopeKind>;

/** Widen a Den notice to its catalog-declared scope. */
export function clientNoticeScope(
  kind: ClientNoticeKind | undefined,
  callSite: NoticeScope,
): NoticeScope {
  if (!kind) return callSite;
  const want = CLIENT_NOTICE_SCOPE[kind];
  if (!want) return callSite;
  return widenNoticeScope(scopeOfKind(want, callSite), callSite);
}

/** Build a declared scope from available call-site ids. */
export function scopeOfKind(kind: NoticeScopeKind, callSite: NoticeScope): NoticeScope {
  if (kind === "app") return APP_SCOPE;
  if (kind === "project") {
    return callSite.kind === "app" ? APP_SCOPE : projectScope(callSite.projectId);
  }
  return callSite;
}
