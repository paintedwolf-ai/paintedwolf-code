import { createSignal } from "solid-js";
import type { GitWorktreeEntry, GitWorktreesView } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { gitWorktreeTargetKey, resolveGitWorktreeTarget } from "../../store/git-cache-actions.ts";

export type CheckoutPinSummary = {
  worktreeId: string;
  pinned: boolean;
  branch?: string;
  headShort?: string;
  detached?: boolean;
  path?: string;
  isProjectCheckout?: boolean;
};

export type WorktreeRowView = {
  entry: GitWorktreeEntry;
  branchLabel: string;
  shortDir: string;
  fullPath: string;
  hasDuplicateBasename: boolean;
  inspected: boolean;
  boundToChat: boolean;
  changeSummary?: string;
};

export type GroupedWorktreeRows = {
  project: WorktreeRowView[];
  chat: WorktreeRowView[];
  other: WorktreeRowView[];
};

export type ReconciledCheckoutSelection = {
  inspectedWorktreeId?: string;
  pinned: boolean;
  unavailable: boolean;
  summary?: CheckoutPinSummary;
};

const pinRecordsByStore = new WeakMap<AppStore, Map<string, CheckoutPinSummary>>();
const [inspectionRevision, setInspectionRevision] = createSignal(0);

function storeMap(appStore: AppStore): Map<string, CheckoutPinSummary> {
  let map = pinRecordsByStore.get(appStore);
  if (!map) {
    map = new Map();
    pinRecordsByStore.set(appStore, map);
  }
  return map;
}

function pathSegments(rawPath: string): string[] {
  return rawPath.replace(/\\/g, "/").split("/").filter(Boolean);
}

export function formatCheckoutBranchLabel(entry: {
  branch?: string;
  detached?: boolean;
  head_short?: string;
}): string {
  const head = entry.head_short?.trim();
  if (entry.detached || !entry.branch?.trim()) {
    return head ? `Detached HEAD (${head})` : "Detached HEAD";
  }
  return entry.branch.trim();
}

function computeDisambiguatedDirs(entries: readonly GitWorktreeEntry[]): Map<string, { shortDir: string; duplicateBasename: boolean }> {
  const splitById = new Map<string, string[]>();
  const basenameCounts = new Map<string, number>();
  for (const entry of entries) {
    const segs = pathSegments(entry.path);
    splitById.set(entry.worktree_id, segs);
    const base = segs[segs.length - 1] ?? entry.path;
    basenameCounts.set(base, (basenameCounts.get(base) ?? 0) + 1);
  }
  const out = new Map<string, { shortDir: string; duplicateBasename: boolean }>();
  for (const entry of entries) {
    const segs = splitById.get(entry.worktree_id) ?? [];
    const base = segs[segs.length - 1] ?? entry.path;
    const duplicateBasename = (basenameCounts.get(base) ?? 0) > 1;
    let shortDir = base;
    if (duplicateBasename && segs.length > 0) {
      for (let depth = 2; depth <= segs.length; depth++) {
        const candidate = segs.slice(-depth).join("/");
        const collides = entries.some((peer) => {
          if (peer.worktree_id === entry.worktree_id) return false;
          const peerSegs = splitById.get(peer.worktree_id) ?? [];
          return peerSegs.slice(-depth).join("/") === candidate;
        });
        shortDir = candidate;
        if (!collides) break;
      }
    }
    out.set(entry.worktree_id, { shortDir, duplicateBasename });
  }
  return out;
}

export function groupWorktreeEntries(
  entries: readonly GitWorktreeEntry[],
  inspectedWorktreeId?: string,
): GroupedWorktreeRows {
  const dirs = computeDisambiguatedDirs(entries);
  const project: WorktreeRowView[] = [];
  const chat: WorktreeRowView[] = [];
  const other: WorktreeRowView[] = [];
  const seen = new Set<string>();

  const toRow = (entry: GitWorktreeEntry): WorktreeRowView => {
    const dirInfo = dirs.get(entry.worktree_id) ?? { shortDir: entry.path, duplicateBasename: false };
    const changed = entry.changed_count ?? ((entry.staged_count ?? 0) + (entry.unstaged_count ?? 0));
    const changeSummary = entry.status_cached
      ? entry.dirty
        ? `${changed} changed`
        : "Clean"
      : undefined;
    return {
      entry,
      branchLabel: formatCheckoutBranchLabel(entry),
      shortDir: dirInfo.shortDir,
      fullPath: entry.path,
      hasDuplicateBasename: dirInfo.duplicateBasename,
      inspected: Boolean(inspectedWorktreeId && entry.worktree_id === inspectedWorktreeId),
      boundToChat: entry.bound_to_session === true,
      changeSummary,
    };
  };

  for (const entry of entries) {
    if (!entry.is_project_checkout || seen.has(entry.worktree_id)) continue;
    seen.add(entry.worktree_id);
    project.push(toRow(entry));
  }
  for (const entry of entries) {
    if (seen.has(entry.worktree_id)) continue;
    if (entry.bound_to_session || entry.used_by_chat || entry.created_by_chat) {
      seen.add(entry.worktree_id);
      chat.push(toRow(entry));
    }
  }
  for (const entry of entries) {
    if (seen.has(entry.worktree_id)) continue;
    seen.add(entry.worktree_id);
    other.push(toRow(entry));
  }
  return { project, chat, other };
}

function summaryFromEntry(entry: GitWorktreeEntry, pinned: boolean): CheckoutPinSummary {
  return {
    worktreeId: entry.worktree_id,
    pinned,
    branch: entry.branch,
    headShort: entry.head_short,
    detached: entry.detached,
    path: entry.path,
    isProjectCheckout: entry.is_project_checkout,
  };
}

export function readCheckoutInspectionState(
  appStore: AppStore | undefined,
  projectId: string,
  repoId: string,
): ReconciledCheckoutSelection {
  if (!appStore) return { pinned: false, unavailable: false };
  void inspectionRevision();
  void appStore.state.gitWorktreeEpoch;
  const key = gitWorktreeTargetKey(projectId, repoId);
  const record = storeMap(appStore).get(key);
  const targetId = resolveGitWorktreeTarget(appStore.state, projectId, repoId) ?? record?.worktreeId;
  return {
    inspectedWorktreeId: targetId,
    pinned: record?.pinned === true,
    unavailable: false,
    summary: record,
  };
}

export function pinRepoWorktreeSelection(
  appStore: AppStore,
  projectId: string,
  repoId: string,
  entry: GitWorktreeEntry,
): void {
  const key = gitWorktreeTargetKey(projectId, repoId);
  storeMap(appStore).set(key, summaryFromEntry(entry, true));
  appStore.actions.setGitWorktreeTarget(projectId, repoId, entry.worktree_id);
  setInspectionRevision((n) => n + 1);
}

export function clearRepoWorktreePin(
  appStore: AppStore,
  projectId: string,
  repoId: string,
  view?: GitWorktreesView,
): ReconciledCheckoutSelection {
  const key = gitWorktreeTargetKey(projectId, repoId);
  storeMap(appStore).delete(key);
  appStore.actions.setGitWorktreeTarget(projectId, repoId, null);
  setInspectionRevision((n) => n + 1);
  return view
    ? reconcileRepoWorktreeSelection(appStore, projectId, repoId, view)
    : { pinned: false, unavailable: false };
}

export function computeCheckoutSelection(
  view: GitWorktreesView,
  existing: CheckoutPinSummary | undefined,
  currentWorktreeId?: string,
): ReconciledCheckoutSelection {
  const byId = new Map(view.worktrees.map((w) => [w.worktree_id, w]));
  if (existing?.pinned) {
    const match = byId.get(existing.worktreeId);
    if (match && match.available) {
      return {
        inspectedWorktreeId: match.worktree_id,
        pinned: true,
        unavailable: false,
        summary: summaryFromEntry(match, true),
      };
    }
    return { inspectedWorktreeId: existing.worktreeId, pinned: true, unavailable: true, summary: existing };
  }

  const boundId = view.bound_worktree_id?.trim();
  const boundEntry = boundId ? byId.get(boundId) : undefined;
  if (boundEntry && boundEntry.available) {
    return {
      inspectedWorktreeId: boundEntry.worktree_id,
      pinned: false,
      unavailable: false,
      summary: summaryFromEntry(boundEntry, false),
    };
  }

  const chatCandidates = view.worktrees.filter(
    (w) => !w.is_project_checkout && w.available && (w.used_by_chat || w.created_by_chat),
  );
  if (chatCandidates.length === 1) {
    const chosen = chatCandidates[0]!;
    return {
      inspectedWorktreeId: chosen.worktree_id,
      pinned: false,
      unavailable: false,
      summary: summaryFromEntry(chosen, false),
    };
  }

  if (chatCandidates.length > 1) {
    const currentMatch = currentWorktreeId ? byId.get(currentWorktreeId) : undefined;
    if (currentMatch && currentMatch.available) {
      return {
        inspectedWorktreeId: currentMatch.worktree_id,
        pinned: false,
        unavailable: false,
        summary: summaryFromEntry(currentMatch, false),
      };
    }
  }

  const projectEntry = (view.project_checkout_id ? byId.get(view.project_checkout_id) : undefined)
    ?? view.worktrees.find((w) => w.is_project_checkout && w.available);
  if (projectEntry) {
    return {
      inspectedWorktreeId: projectEntry.worktree_id,
      pinned: false,
      unavailable: false,
      summary: summaryFromEntry(projectEntry, false),
    };
  }
  return { pinned: false, unavailable: false };
}

export function reconcileRepoWorktreeSelection(
  appStore: AppStore,
  projectId: string,
  repoId: string,
  view: GitWorktreesView,
): ReconciledCheckoutSelection {
  const key = gitWorktreeTargetKey(projectId, repoId);
  const map = storeMap(appStore);
  const existing = map.get(key);
  const currentId = resolveGitWorktreeTarget(appStore.state, projectId, repoId) ?? existing?.worktreeId;
  const computed = computeCheckoutSelection(view, existing, currentId);
  if (computed.summary) {
    map.set(key, computed.summary);
  }
  if (computed.pinned || computed.unavailable) {
    appStore.actions.setGitWorktreeTarget(projectId, repoId, computed.inspectedWorktreeId ?? null);
  } else if (computed.summary?.isProjectCheckout) {
    const boundId = view.bound_worktree_id?.trim();
    appStore.actions.setGitWorktreeTarget(projectId, repoId, boundId ? computed.inspectedWorktreeId ?? null : null);
  } else {
    appStore.actions.setGitWorktreeTarget(projectId, repoId, computed.inspectedWorktreeId ?? null);
  }
  return computed;
}
