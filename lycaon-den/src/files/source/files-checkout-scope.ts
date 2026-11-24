import { createSignal } from "solid-js";
import { produce } from "solid-js/store";
import type { GitWorktreeEntry, SourceWorkspace, SourceWorkspaceRoot } from "../../api/types.ts";
import type { AppStore } from "../../store/app-state-model.ts";
import { ensureProject, setFilesStore, type FileBuffer } from "../documents/files-buffer-state.ts";

type WorkspaceRootCheckoutMeta = {
  rootId: string;
  path: string;
  branchId: string;
  repoId?: string;
  worktreeId?: string;
};

type KnownCheckoutEntrySummary = {
  repoId: string;
  worktreeId: string;
  path: string;
  branch?: string;
  headShort?: string;
  detached?: boolean;
  isProjectCheckout?: boolean;
  available: boolean;
};

const rootCheckoutsByProject = new Map<string, Record<string, WorkspaceRootCheckoutMeta>>();
const trunkWorktreeByProjectRoot = new Map<string, string>();
const checkoutRootPaths = new Map<string, string>();
const knownCheckoutsByProject = new Map<string, Map<string, KnownCheckoutEntrySummary>>();
const selectedWorktreesByProject = new Map<string, string[]>();
const [checkoutScopeEpoch, setCheckoutScopeEpoch] = createSignal(0);

function bumpCheckoutScope(): void {
  setCheckoutScopeEpoch((n) => n + 1);
}

function projectRootKey(projectId: string, rootId: string): string {
  return `${projectId.trim()}\0${rootId.trim()}`;
}

function checkoutRootKey(projectId: string, rootId: string, worktreeId: string): string {
  return `${projectId.trim()}\0${rootId.trim()}\0${worktreeId.trim()}`;
}

/** Selected checkout UUIDs for a project from shared in-memory inspection state. */
export function selectedProjectWorktreeIds(appStore: AppStore | undefined, projectId: string): string[] {
  if (!appStore) return [];
  // Track epoch bumps for reactive observers.
  void appStore.state.gitWorktreeEpoch;
  const pid = projectId.trim();
  const prefix = `${pid}\u0000`;
  const targets = appStore.state.gitWorktreeTargets;
  if (!targets) {
    if (selectedWorktreesByProject.delete(pid)) bumpCheckoutScope();
    return [];
  }
  const seen = new Set<string>();
  const out: string[] = [];
  for (const [key, value] of Object.entries(targets)) {
    if (!key.startsWith(prefix)) continue;
    const id = typeof value === "string" ? value.trim() : "";
    if (!id || seen.has(id)) continue;
    seen.add(id);
    out.push(id);
  }
  out.sort();
  const prev = selectedWorktreesByProject.get(pid);
  if ((prev?.join(",") ?? "") !== out.join(",")) {
    if (out.length > 0) selectedWorktreesByProject.set(pid, out);
    else selectedWorktreesByProject.delete(pid);
    bumpCheckoutScope();
  }
  return out;
}

/** Reactive checkout UUIDs currently selected for a project across surfaces. */
export function activeProjectWorktreeIds(projectId: string): readonly string[] | undefined {
  void checkoutScopeEpoch();
  const selected = selectedWorktreesByProject.get(projectId.trim());
  return selected && selected.length > 0 ? selected : undefined;
}

/** Records the checkout identity that resolved each root in the current workspace. */
function recordWorkspaceRootCheckouts(
  projectId: string,
  roots: readonly SourceWorkspaceRoot[],
): Record<string, WorkspaceRootCheckoutMeta> {
  const pid = projectId.trim();
  const map: Record<string, WorkspaceRootCheckoutMeta> = {};
  for (const root of roots) {
    const rootId = root.id.trim();
    const branchId = root.branch_id?.trim() ?? "";
    const repoId = root.repo_id?.trim() || undefined;
    const worktreeId = root.worktree_id?.trim() || undefined;
    map[rootId] = { rootId, path: root.path, branchId, repoId, worktreeId };
    if (worktreeId && root.path) {
      checkoutRootPaths.set(checkoutRootKey(pid, rootId, worktreeId), root.path);
    }
    if (branchId === "" && worktreeId) {
      trunkWorktreeByProjectRoot.set(projectRootKey(pid, rootId), worktreeId);
    }
  }
  rootCheckoutsByProject.set(pid, map);
  return map;
}

export function recordKnownCheckoutEntries(
  projectId: string,
  repoId: string,
  entries: readonly GitWorktreeEntry[],
): void {
  const pid = projectId.trim();
  const rid = repoId.trim();
  if (!pid || !rid) return;
  let byId = knownCheckoutsByProject.get(pid);
  if (!byId) {
    byId = new Map();
    knownCheckoutsByProject.set(pid, byId);
  }
  for (const entry of entries) {
    const wid = entry.worktree_id.trim();
    if (!wid) continue;
    byId.set(wid, {
      repoId: rid,
      worktreeId: wid,
      path: entry.path,
      branch: entry.branch,
      headShort: entry.head_short,
      detached: entry.detached,
      isProjectCheckout: entry.is_project_checkout,
      available: entry.available,
    });
  }
}

export function knownCheckoutSummary(projectId: string, worktreeId: string | undefined): KnownCheckoutEntrySummary | undefined {
  const pid = projectId.trim();
  const wid = worktreeId?.trim() ?? "";
  if (!pid || !wid) return undefined;
  return knownCheckoutsByProject.get(pid)?.get(wid);
}

/** Resolves the durable host branch_id for a checkout UUID, preserving "" for the trunk checkout. */
export function branchIdForWorktree(
  projectId: string,
  rootId: string,
  worktreeId: string | undefined,
): string | undefined {
  const wid = worktreeId?.trim() ?? "";
  if (!wid) return undefined;
  if (trunkWorktreeByProjectRoot.get(projectRootKey(projectId, rootId)) === wid) return "";
  if (knownCheckoutSummary(projectId, wid)?.isProjectCheckout) return "";
  return `worktree:${wid}`;
}

/** Resolves a checkout's mapped physical root path when known. */
export function resolveCheckoutRootPath(
  projectId: string,
  rootId: string,
  worktreeId: string | undefined,
  fallbackRootPath?: string,
): string | undefined {
  const wid = worktreeId?.trim() ?? "";
  if (!wid) return fallbackRootPath;
  const direct = checkoutRootPaths.get(checkoutRootKey(projectId, rootId, wid));
  if (direct) return direct;
  const summary = knownCheckoutSummary(projectId, wid);
  if (!summary?.path) return fallbackRootPath;
  if (!fallbackRootPath) return summary.path;
  const projectSummary = [...(knownCheckoutsByProject.get(projectId.trim())?.values() ?? [])].find(
    (item) => item.repoId === summary.repoId && item.isProjectCheckout && item.path,
  );
  if (projectSummary?.path) {
    const normRoot = fallbackRootPath.replace(/\\/g, "/").replace(/\/+$/, "");
    const normTop = projectSummary.path.replace(/\\/g, "/").replace(/\/+$/, "");
    if (normRoot === normTop) return summary.path;
    if (normRoot.startsWith(`${normTop}/`)) {
      return `${summary.path.replace(/\/+$/, "")}/${normRoot.slice(normTop.length + 1)}`;
    }
  }
  return summary.path;
}

export function workspaceRootCheckout(projectId: string, rootId: string): WorkspaceRootCheckoutMeta | undefined {
  return rootCheckoutsByProject.get(projectId.trim())?.[rootId.trim()];
}

export function resetWorkspaceRootCheckoutsForTests(): void {
  rootCheckoutsByProject.clear();
  trunkWorktreeByProjectRoot.clear();
  checkoutRootPaths.clear();
  knownCheckoutsByProject.clear();
  selectedWorktreesByProject.clear();
}

/** Extracts a checkout UUID from a durable branch_id ("worktree:<uuid>") when present. */
export function worktreeIdFromBranchId(branchId: string | undefined | null): string | undefined {
  const trimmed = branchId?.trim() ?? "";
  return trimmed.startsWith("worktree:") ? trimmed.slice("worktree:".length) || undefined : undefined;
}

/** Resolves the explicit checkout UUID addressing a specific buffer, independent of the visible tree. */
export function bufferCheckoutWorktreeId(
  projectId: string,
  buffer: Pick<FileBuffer, "rootId"> & Partial<Pick<FileBuffer, "worktreeId" | "branchId">>,
  fallbackWorktreeIds?: readonly string[],
): string | readonly string[] | undefined {
  const explicit = buffer.worktreeId?.trim() || worktreeIdFromBranchId(buffer.branchId);
  if (explicit) return explicit;
  if (buffer.branchId === "") {
    const trunkId = trunkWorktreeByProjectRoot.get(projectRootKey(projectId, buffer.rootId));
    if (trunkId) return trunkId;
  }
  return fallbackWorktreeIds && fallbackWorktreeIds.length > 0 ? fallbackWorktreeIds : undefined;
}

/** Updates active workspace root branches while keeping existing open tabs bound to their own checkouts. */
export function adoptFilesWorkspaceRoots(projectId: string, workspace: SourceWorkspace): void {
  const pid = projectId.trim();
  ensureProject(pid);
  const meta = recordWorkspaceRootCheckouts(pid, workspace.roots);
  const branches = Object.fromEntries(workspace.roots.map((root) => [root.id, root.branch_id ?? ""]));
  setFilesStore(pid, produce((state) => {
    state.rootBranches = branches;
    for (const key of state.order) {
      const buf = state.byKey[key];
      if (!buf || buf.jobId) continue;
      const rootMeta = meta[buf.rootId];
      if (!rootMeta) continue;
      if (buf.branchId === undefined && !buf.documentId && !buf.fileId) {
        buf.branchId = rootMeta.branchId;
        buf.worktreeId = rootMeta.worktreeId;
        buf.workspaceId = workspace.workspace_id;
      }
    }
  }));
}

/** Short disambiguating label for a buffer's checkout when same-named files are open across checkouts. */
export function bufferCheckoutQualifier(buffer: Partial<Pick<FileBuffer, "branchId" | "worktreeId" | "checkoutLabel" | "checkoutUnavailable">>): string {
  const base = buffer.checkoutLabel?.trim()
    || (buffer.branchId?.startsWith("worktree:")
      ? `Worktree ${buffer.branchId.slice("worktree:".length, "worktree:".length + 8)}`
      : buffer.branchId === ""
        ? "Project folder"
        : "");
  if (buffer.checkoutUnavailable) {
    return base ? `${base} (unavailable)` : "Unavailable checkout";
  }
  return base;
}
