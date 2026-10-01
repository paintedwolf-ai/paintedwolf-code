/** Per-window scroll hints are independent of saved disclosure intent. */

import type { DenFilesTreeViewState } from "../../../shared/app-state-types.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import {
  getAppStateSnapshot,
  persistAppState,
} from "../../store/app-state-snapshot.ts";
export const FILES_TREE_WINDOW_CAP = 8;
export const FILES_TREE_WINDOW_PROJECT_CAP = 16;
const PERSIST_DEBOUNCE_MS = 1000;

let windowLabel = "main";
const scrollByProject = new Map<string, number>();
const navigationByProject = new Map<string, number>();
const dirtyScrollProjects = new Set<string>();
let persistTimer: ReturnType<typeof setTimeout> | undefined;
let dirty = false;

export async function resolveFilesTreeWindowLabel(): Promise<void> {
  if (!isTauriRuntime()) {
    windowLabel = "main";
    return;
  }
  try {
    const { getCurrentWindow } = await import("@tauri-apps/api/window");
    windowLabel = getCurrentWindow().label.trim() || "main";
  } catch {
    windowLabel = "main";
  }
}

export function getFilesTreeScrollTop(projectId: string): number | undefined {
  return scrollByProject.get(projectId);
}

/** A remount restores its viewport; a new file navigation supersedes it. */
export function claimFilesTreeNavigation(workspaceId: string, revision: number): boolean {
  if (navigationByProject.get(workspaceId) === revision) return false;
  navigationByProject.set(workspaceId, revision);
  return true;
}

export function setFilesTreeScrollTop(
  projectId: string,
  scrollTop: number,
): void {
  const id = projectId.trim();
  if (!id) return;
  const top = Math.max(0, Math.round(scrollTop));
  const prev = scrollByProject.get(id);
  if (prev === top) return;
  scrollByProject.set(id, top);
  dirtyScrollProjects.add(id);
  scheduleFilesTreeViewPersist();
}

function scheduleFilesTreeViewPersist(): void {
  dirty = true;
  clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = undefined;
    void flushFilesTreeViewToDisk().catch(() => undefined);
  }, PERSIST_DEBOUNCE_MS);
}

function pruneTouchedRecord<T extends { touchedAt: number }>(
  record: Record<string, T>,
  cap: number,
): Record<string, T> {
  const entries = Object.entries(record);
  if (entries.length <= cap) return record;
  return Object.fromEntries(
    entries
      .sort((a, b) => b[1].touchedAt - a[1].touchedAt)
      .slice(0, cap),
  );
}

export async function flushFilesTreeViewToDisk(): Promise<void> {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  if (!dirty) return;
  dirty = false;

  const scrollDirty = [...dirtyScrollProjects];
  dirtyScrollProjects.clear();
  if (scrollDirty.length === 0) return;

  const snap = getAppStateSnapshot().filesTreeView;
  const touchedAt = Date.now();
  let byWindow: NonNullable<DenFilesTreeViewState["byWindow"]> = {
    ...(snap?.byWindow ?? {}),
  };
  const label = windowLabel;
  let winProjects = {
    ...(byWindow[label]?.byProject ?? {}),
  };
  for (const projectId of scrollDirty) {
    const top = scrollByProject.get(projectId) ?? 0;
    if (top <= 0) delete winProjects[projectId];
    else winProjects[projectId] = { scrollTop: top, touchedAt };
  }
  winProjects = pruneTouchedRecord(winProjects, FILES_TREE_WINDOW_PROJECT_CAP);
  if (Object.keys(winProjects).length === 0) delete byWindow[label];
  else byWindow[label] = { byProject: winProjects, touchedAt };
  byWindow = pruneTouchedRecord(byWindow, FILES_TREE_WINDOW_CAP);

  if (Object.keys(byWindow).length === 0) {
    await persistAppState({ filesTreeView: undefined });
    return;
  }
  await persistAppState({ filesTreeView: { byWindow } });
}

export function syncFilesTreeViewFromSnapshot(): void {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  dirty = false;
  dirtyScrollProjects.clear();
  scrollByProject.clear();
  navigationByProject.clear();

  const snap = getAppStateSnapshot().filesTreeView;
  if (!snap) return;

  const win = snap.byWindow?.[windowLabel]?.byProject ?? {};
  for (const [projectId, row] of Object.entries(win)) {
    const id = projectId.trim();
    if (!id) continue;
    const top = row.scrollTop;
    if (typeof top === "number" && Number.isFinite(top) && top > 0) {
      scrollByProject.set(id, Math.round(top));
    }
  }
}

export function parseFilesTreeViewState(
  value: unknown,
): DenFilesTreeViewState | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return undefined;
  }
  const row = value as Record<string, unknown>;
  const byWindowRaw = row.byWindow;
  const byWindow: NonNullable<DenFilesTreeViewState["byWindow"]> = {};
  if (
    typeof byWindowRaw === "object" &&
    byWindowRaw !== null &&
    !Array.isArray(byWindowRaw)
  ) {
    for (const [label, winEntry] of Object.entries(
      byWindowRaw as Record<string, unknown>,
    )) {
      const winLabel = label.trim();
      if (!winLabel || typeof winEntry !== "object" || winEntry === null) {
        continue;
      }
      const projectsRaw = (winEntry as { byProject?: unknown }).byProject;
      if (
        typeof projectsRaw !== "object" ||
        projectsRaw === null ||
        Array.isArray(projectsRaw)
      ) {
        continue;
      }
      const winProjects: Record<string, { scrollTop?: number; touchedAt: number }> = {};
      for (const [projectId, scrollEntry] of Object.entries(
        projectsRaw as Record<string, unknown>,
      )) {
        const id = projectId.trim();
        if (!id || typeof scrollEntry !== "object" || scrollEntry === null) {
          continue;
        }
        const top = (scrollEntry as { scrollTop?: unknown }).scrollTop;
        const touchedAt = (scrollEntry as { touchedAt?: unknown }).touchedAt;
        if (typeof top !== "number" || !Number.isFinite(top) || top <= 0) {
          continue;
        }
        winProjects[id] = {
          scrollTop: Math.round(top),
          touchedAt:
            typeof touchedAt === "number" && Number.isFinite(touchedAt)
              ? touchedAt
              : 0,
        };
      }
      if (Object.keys(winProjects).length > 0) {
        const touchedAt = (winEntry as { touchedAt?: unknown }).touchedAt;
        byWindow[winLabel] = {
          byProject: pruneTouchedRecord(
            winProjects,
            FILES_TREE_WINDOW_PROJECT_CAP,
          ),
          touchedAt:
            typeof touchedAt === "number" && Number.isFinite(touchedAt)
              ? touchedAt
              : 0,
        };
      }
    }
  }

  const prunedWindows = pruneTouchedRecord(byWindow, FILES_TREE_WINDOW_CAP);
  return Object.keys(prunedWindows).length ? { byWindow: prunedWindows } : undefined;
}

export function resetFilesTreeViewStateForTests(): void {
  clearTimeout(persistTimer);
  persistTimer = undefined;
  dirty = false;
  dirtyScrollProjects.clear();
  scrollByProject.clear();
  navigationByProject.clear();
  windowLabel = "main";
}
