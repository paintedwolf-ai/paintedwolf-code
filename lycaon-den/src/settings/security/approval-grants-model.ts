import type {
  ApprovalGrant,
  ApprovalGrantCategory,
  AskQuiet,
} from "../../api/types.ts";

export type GrantCategoryFilter = "all" | ApprovalGrantCategory;

/** Project configuration shows the current chat, this project, and applicable device leases. */
export function projectApprovalGrants(
  grants: readonly ApprovalGrant[],
  projectId: string,
  sessionId: string | undefined,
  applicableIds: ReadonlySet<string>,
): ApprovalGrant[] {
  return grants.filter((grant) => {
    if (grant.scope === "project") return grant.project_id === projectId;
    if (grant.scope === "device") return !grant.project_id || grant.project_id === projectId;
    return (sessionId != null && grant.chat_session_id === sessionId) || applicableIds.has(grant.id);
  });
}

export function projectAskQuiets(
  quiets: readonly AskQuiet[],
  sessionId: string | undefined,
  applicableIds: ReadonlySet<string>,
): AskQuiet[] {
  return quiets.filter((quiet) => (sessionId != null && quiet.chat_session_id === sessionId) || applicableIds.has(quiet.id));
}

export function hasElevatedEffects(row: ApprovalGrant | AskQuiet): boolean {
  return (row.elevated_effects?.length ?? 0) > 0;
}

/** Category order for filters present in the loaded list. */
export const GRANT_CATEGORY_ORDER: readonly ApprovalGrantCategory[] = [
  "agent_policy",
  "action_set",
  "secret",
  "secret_redact",
  "socket_path",
  "direct_ip",
  "execution_capability",
  "loopback_connect",
  "egress_command",
  "write_root",
  "local_listen",
  "host",
  "tool",
  "mcp",
  "path",
  "host_resource",
  "package_coordinate",
] as const;

export type GrantCategoryFilterOption = {
  id: GrantCategoryFilter;
  count: number;
};

/** Filters include the total and each category present in the list. */
export function grantCategoryFilterOptions(
  grants: readonly ApprovalGrant[],
): GrantCategoryFilterOption[] {
  const counts = new Map<ApprovalGrantCategory, number>();
  for (const grant of grants) {
    counts.set(grant.category, (counts.get(grant.category) ?? 0) + 1);
  }
  const options: GrantCategoryFilterOption[] = [
    { id: "all", count: grants.length },
  ];
  for (const category of GRANT_CATEGORY_ORDER) {
    const count = counts.get(category);
    if (count) options.push({ id: category, count });
  }
  return options;
}

/** Case-insensitive match across the row's human-searchable fields. */
function grantMatchesQuery(
  grant: ApprovalGrant,
  query: string,
): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  const fields = [
    grant.title,
    grant.pattern,
    grant.category,
    grant.approved_path,
    grant.resolved_path,
    grant.session_title,
    grant.project_dir,
    ...(grant.resource_ids ?? []),
    ...(grant.secret_names ?? []),
    ...(grant.secret_recipients ?? []).flatMap((recipient) => [recipient.label, recipient.surface]),
  ];
  return fields.some((f) => f?.toLowerCase().includes(q) ?? false);
}

function grantMatchesCategory(
  grant: ApprovalGrant,
  filter: GrantCategoryFilter,
): boolean {
  if (filter === "all") return true;
  return grant.category === filter;
}

export function filterApprovalGrants(
  grants: readonly ApprovalGrant[],
  query: string,
  category: GrantCategoryFilter,
): ApprovalGrant[] {
  return grants.filter(
    (g) => grantMatchesQuery(g, query) && grantMatchesCategory(g, category),
  );
}

function grantedAtMs(grant: ApprovalGrant): number {
  const ms = Date.parse(grant.granted_at);
  return Number.isFinite(ms) ? ms : 0;
}

function newestFirst(grants: ApprovalGrant[]): ApprovalGrant[] {
  return grants.sort((a, b) => grantedAtMs(b) - grantedAtMs(a));
}

export type GrantScopeGroup =
  | {
      kind: "chat";
      sessionId: string;
      sessionTitle?: string;
      grants: ApprovalGrant[];
    }
  | {
      kind: "project";
      projectId: string;
      /** Display label only; projectId determines group membership. */
      projectDir: string;
      grants: ApprovalGrant[];
    }
  | { kind: "device"; grants: ApprovalGrant[] };

/** Groups chats, projects, then device grants; project groups use stable IDs. */
export function groupApprovalGrants(
  grants: readonly ApprovalGrant[],
): GrantScopeGroup[] {
  const chats = new Map<string, ApprovalGrant[]>();
  const projects = new Map<string, ApprovalGrant[]>();
  const device: ApprovalGrant[] = [];
  for (const grant of grants) {
    if (grant.scope === "chat") {
      const key = grant.chat_session_id ?? "";
      const list = chats.get(key);
      if (list) list.push(grant);
      else chats.set(key, [grant]);
    } else if (grant.scope === "project") {
      const key = grant.project_id ?? "";
      const list = projects.get(key);
      if (list) list.push(grant);
      else projects.set(key, [grant]);
    } else {
      device.push(grant);
    }
  }
  const groups: GrantScopeGroup[] = [];
  const chatGroups = [...chats.entries()].map(([sessionId, list]) => ({
    kind: "chat" as const,
    sessionId,
    sessionTitle: list.find((g) => g.session_title)?.session_title,
    grants: newestFirst(list),
  }));
  chatGroups.sort(
    (a, b) => grantedAtMs(b.grants[0]!) - grantedAtMs(a.grants[0]!),
  );
  groups.push(...chatGroups);
  const projectGroups = [...projects.entries()].map(([projectId, list]) => ({
    kind: "project" as const,
    projectId,
    // Folderless projects use the group heading as their label.
    projectDir: list.find((g) => g.project_dir)?.project_dir ?? "",
    grants: newestFirst(list),
  }));
  // Stable IDs break ties between identical folder labels.
  projectGroups.sort(
    (a, b) =>
      a.projectDir.localeCompare(b.projectDir) ||
      a.projectId.localeCompare(b.projectId),
  );
  groups.push(...projectGroups);
  if (device.length > 0) {
    groups.push({ kind: "device", grants: newestFirst(device) });
  }
  return groups;
}

export function unavailableGrants(
  grants: readonly ApprovalGrant[],
): ApprovalGrant[] {
  return grants.filter((g) => g.unavailable === true);
}

/** Rows past their expiry, kept only for review until cleared. */
export function expiredGrants(
  grants: readonly ApprovalGrant[],
): ApprovalGrant[] {
  return grants.filter((g) => g.expired === true);
}

export function formatGrantedAt(iso: string): string {
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return "—";
  return new Date(ms).toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

/** Near-term expiry uses a countdown; distant and past expiry use an absolute date. */
export function formatGrantExpiry(iso: string, nowMs = Date.now()): string {
  const ms = Date.parse(iso);
  if (!Number.isFinite(ms)) return "—";
  const minutes = Math.round((ms - nowMs) / 60_000);
  if (minutes <= 0) return formatGrantedAt(iso);
  if (minutes < 60) return `in ${minutes} min`;
  if (minutes < 48 * 60) {
    const hours = Math.floor(minutes / 60);
    const rest = minutes % 60;
    return rest ? `in ${hours} h ${rest} min` : `in ${hours} h`;
  }
  return formatGrantedAt(iso);
}

export type QuietSessionGroup = {
  sessionId: string;
  sessionTitle?: string;
  quiets: AskQuiet[];
};

function quietCreatedMs(quiet: AskQuiet): number {
  const ms = Date.parse(quiet.created_at);
  return Number.isFinite(ms) ? ms : 0;
}

/** Group quiets by chat session, newest activity first inside each group. */
export function groupAskQuiets(quiets: readonly AskQuiet[]): QuietSessionGroup[] {
  const bySession = new Map<string, AskQuiet[]>();
  for (const quiet of quiets) {
    const key = quiet.chat_session_id || "";
    const list = bySession.get(key);
    if (list) list.push(quiet);
    else bySession.set(key, [quiet]);
  }
  const groups = [...bySession.entries()].map(([sessionId, list]) => ({
    sessionId,
    sessionTitle: list.find((q) => q.session_title)?.session_title,
    quiets: list.sort((a, b) => quietCreatedMs(b) - quietCreatedMs(a)),
  }));
  groups.sort(
    (a, b) => quietCreatedMs(b.quiets[0]!) - quietCreatedMs(a.quiets[0]!),
  );
  return groups;
}

function quietMatchesQuery(quiet: AskQuiet, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return [quiet.label, quiet.key, quiet.session_title].some(
    (f) => f?.toLowerCase().includes(q) ?? false,
  );
}

export function filterAskQuiets(
  quiets: readonly AskQuiet[],
  query: string,
): AskQuiet[] {
  return quiets.filter((q) => quietMatchesQuery(q, query));
}

/** Quiets without an expiry last until the chat is deleted or the quiet is revoked. */
export function formatQuietExpiry(
  quiet: AskQuiet,
  nowMs = Date.now(),
): string {
  if (!quiet.expires_at) return "until this chat is deleted or you revoke it";
  return formatGrantExpiry(quiet.expires_at, nowMs);
}
