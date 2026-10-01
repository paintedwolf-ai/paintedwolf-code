import type { ApprovalGrantCategory } from "../../api/types.ts";

/** Shared labels for lease rows and filters. */
export const GRANT_KIND_LABELS: Record<ApprovalGrantCategory, string> = {
  tool: "Tool",
  mcp: "MCP",
  path: "File",
  host: "Network host",
  write_root: "Write access",
  action_set: "Commands",
  socket_path: "Local service",
  host_resource: "Host access",
  secret: "Secret release",
  secret_redact: "Always redacted",
  execution_capability: "Process and host access",
  direct_ip: "Direct network",
  local_listen: "Local server",
  loopback_connect: "Local connection",
  egress_command: "Command network",
  agent_policy: "Instructions and settings",
  package_coordinate: "Package execution",
} as const;

export const SAVED_APPROVALS_COPY = {
  explainTitle: "What are these?",
  explain:
    "Everything you've approved, in one place. Chat approvals last until you delete the chat; project and device leases expire on the dates shown. Revoke any row to be asked again immediately.",
  projectExplain:
    "Saved approvals available to this chat, including access shared with the project or device.",
  projectExplainNoChat:
    "Approvals for this project and applicable device access. Shared approvals may also affect other chats.",
  elevatedHint: "These approvals allow actions beyond the usual sandbox. Revoking one affects future actions; running processes may retain access.",
  otherHeading: "Other saved approvals",
  revokeElevatedAll: "Revoke all elevated access",
  revokeElevatedTitle: "Revoke elevated access",
  revokeElevatedConfirm: "Revoke every elevated-access approval currently available to this chat? Shared project and device approvals will also be revoked for other chats using them. Running processes may retain access.",
  revokeElevatedSuccess: "Elevated-access approvals revoked. Running processes may retain access.",
  revokeElevatedPartial: "Some elevated-access approvals could not be revoked. The remaining approvals are still listed here.",
  revokedNotice: "Saved approval revoked.",
  summaryError: "Could not check all approvals for this chat. Some worker approvals may be missing from this view.",
  scopeLabel: (scope: "chat" | "project" | "device", projectOnly: boolean) =>
    scope === "chat" ? "This chat" : scope === "project" ? "This project" : projectOnly ? "Device · This project only" : "Device · Other chats may use this",
  chickletTip:
    "Every row here also shows Revoke on its original decision card. Revoking in either place takes effect in both.",
  searchPlaceholder: "Search by name, path, or host",
  searchAriaLabel: "Search saved approvals",
  categoryFilterLabel: "Kind",
  categoryAll: "All",

  chatGroupHeading: "Chats",
  chatGroupLifetime: "last until the chat is deleted",
  chatGroupUntitled: "Untitled chat",
  chatGroupKind: "chat",
  projectGroupHeading: "This project",
  projectGroupLifetime: "leases up to 7 days",
  deviceGroupHeading: "Longer approvals",
  deviceGroupLifetime: "leases up to 30 days · scope shown per approval",
  quietedGroupHeading: "Quieted",
  quietedGroupLifetime: "suppressing asks — not authority",
  quietedSuppressed: (n: number) =>
    n === 1 ? "1 ask suppressed" : `${n} asks suppressed`,
  quietedUntil: (remaining: string) => `Quieted for ${remaining}`,
  quietedChat: "Quieted for this chat",
  quietedUntitled: "Untitled chat",

  revokeButton: "Revoke",
  clearButton: "Clear",
  revokeGroupChat: "Revoke all for this chat",
  revokeGroupProject: "Revoke all in this project",
  revokeGroupDevice: "Revoke all longer approvals",
  revokeGroupConfirm:
    "Revoke every approval in this group? This cannot be undone from here.",
  revokeMissing: (n: number) => `Revoke missing (${n})`,
  revokeMissingConfirm:
    "Revoke every saved approval whose folder or socket is missing? This cannot be undone from here.",
  clearExpired: (n: number) => `Clear expired (${n})`,
  clearExpiredConfirm: "Clear every expired approval from the list?",
  unavailableFolderBadge: "Folder missing",
  unavailableSocketBadge: "Socket missing",
  repointedBadge: "Target changed",
  expiredBadge: "Expired",
  empty: "No active approval leases",
  noMatches: "No matches",
  loading: "Loading saved approvals…",
  loadError: "Could not load saved approvals",
  retry: "Retry",
  revokeError: "Could not revoke approval",
  revokeSomeFailed: (failed: number, total: number) =>
    `${failed} of ${total} approvals could not be revoked`,
  countAll: (n: number) =>
    n === 1 ? "1 saved approval" : `${n} saved approvals`,
  countFiltered: (n: number, total: number) => `${n} of ${total}`,

  actionSetSummary: (n: number) =>
    n === 1 ? "1 approved command" : `${n} approved commands`,
  resourceIdsHint: "Named host resources",
  socketDetailPrefix: "Runs outside the sandbox",
  directIPDetail: "Destinations not observed — any command in this chat",
  deviceProjectOnly: "This project only",
  deviceAllProjects: "Every project",
  socketResolvesTo: "resolves to",
  socketMissingHint:
    "Nothing is listening at this path right now — the lease stays until revoked or expired",

  addLocalService: "Add local service",
  socketCreateTitle: "Allow one local service",
  /** The resolved grant supplies its authority warning separately. */
  socketSectionExplain: "Exact AF_UNIX sockets only.",
  socketPathLabel: "Socket path",
  socketPathPlaceholder: "/absolute/path/to/service.sock",
  socketPathAriaLabel: "Absolute AF_UNIX socket path",
  socketResolve: "Check path",
  socketResolveError: "Could not resolve that socket path",
  socketApprovedLabel: "Approved path",
  socketResolvedLabel: "Resolved target",
  socketScopeLabel: "Where it applies",
  socketScopeProject: "This project",
  socketScopeDevice: "This device (all projects)",
  socketProjectLabel: "Project",
  socketProjectMissing:
    "Project scope needs a project — open one first, or choose This device.",
  socketConfirm: "Allow this socket",
  socketCancel: "Cancel",
  socketCreateError: "Could not create local-service grant",
  recentTitle: "Recent asks",
  recentExplain:
    "Which reasons asked over the last week and how you answered. Counts never change what asks; a reason you allowed every time is one worth quieting.",
  recentEmpty: "No approvals asked in the last week.",
  recentLoading: "Loading recent asks…",
  recentLoadError: "Could not load recent asks",
  recentCounts: (asks: number, allowed: number) =>
    `${asks === 1 ? "1 ask" : `${asks} asks`} · ${allowed} allowed`,
  recentAllAllowed: "allowed every time",
  recentWiden: (pattern: string) => `Allow ${pattern} on this device for 30 days`,
  recentWidenDone: (pattern: string) => `${pattern} allowed on this device`,
  recentWidenError: "Could not save that approval",
  recentSubjects: (subjects: readonly string[]) => subjects.join(" · "),
  socketRevokeTitle: "Revoke local service access?",
  socketRevokeConfirm:
    "New processes won't be able to connect. Anything already running keeps its connection until it restarts.",
} as const;
