import { createEffect, createSignal } from "solid-js";
import type { ArtifactEvent } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { invalidateVisualArtifactSrc } from "./visual-artifact-src.ts";
import { invalidateFrameArchive } from "./frame-archive-cache.ts";

export type ArtifactChangeDetail =
  | { readonly kind: "event"; readonly event: ArtifactEvent }
  | { readonly kind: "resync" };

type Listener = (projectId: string, detail: ArtifactChangeDetail) => void;

export const ARTIFACT_REFRESH_DEBOUNCE_MS = 80;
export const ARTIFACT_REFRESH_MAX_WAIT_MS = 400;

const listeners = new Set<Listener>();
const lastHashByArtifact = new Map<string, string>();
const [deletedIds, setDeletedIds] = createSignal<ReadonlySet<string>>(new Set());

export function artifactWasDeleted(artifactId: string): boolean {
  return deletedIds().has(artifactId.trim());
}

export function clearArtifactDeletionMemory(): void {
  lastHashByArtifact.clear();
  setDeletedIds(new Set<string>());
}

function invalidateArtifactCaches(artifactId: string): void {
  const id = artifactId.trim();
  if (!id) return;
  invalidateVisualArtifactSrc(id);
  invalidateFrameArchive(id);
}

function rememberDeletion(artifactId: string): void {
  const id = artifactId.trim();
  if (!id) return;
  lastHashByArtifact.delete(id);
  invalidateArtifactCaches(id);
  if (deletedIds().has(id)) return;
  setDeletedIds((current) => new Set([...current, id]));
}

/** Tombstone errors from direct fetches update deletion state across views. */
export function useArtifactDeletion(artifactId: () => string, error: () => unknown): void {
  createEffect(() => {
    const failure = error();
    if (failure instanceof LycaonApiError && failure.code === "artifact_deleted") {
      rememberDeletion(artifactId());
    }
  });
}

export function notifyArtifactChanged(event: ArtifactEvent): void {
  const id = event.project_id.trim();
  if (!id) return;
  const artifactId = event.artifact_id.trim();
  if (event.op === "deleted") {
    rememberDeletion(artifactId);
  } else if (artifactId) {
    const nextHash = event.content_hash?.trim() ?? "";
    if (nextHash) {
      const prevHash = lastHashByArtifact.get(artifactId);
      lastHashByArtifact.set(artifactId, nextHash);
      if (prevHash !== undefined && prevHash !== nextHash) {
        invalidateArtifactCaches(artifactId);
      }
    }
  }
  const detail: ArtifactChangeDetail = { kind: "event", event };
  for (const listener of listeners) listener(id, detail);
}

export function requestArtifactResync(projectId: string): void {
  const id = projectId.trim();
  if (!id) return;
  const detail: ArtifactChangeDetail = { kind: "resync" };
  for (const listener of listeners) listener(id, detail);
}

export function subscribeArtifactChanges(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

/** Coalesce artifact notifications per project with a bounded maximum wait. */
export function createArtifactRefreshSubscription(
  projectId: () => string,
  refresh: () => void,
  delayMs = ARTIFACT_REFRESH_DEBOUNCE_MS,
  maxWaitMs = ARTIFACT_REFRESH_MAX_WAIT_MS,
): () => void {
  let debounceTimer: ReturnType<typeof setTimeout> | undefined;
  let maxTimer: ReturnType<typeof setTimeout> | undefined;
  let pendingProjectId = "";

  const clear = () => {
    pendingProjectId = "";
    if (debounceTimer !== undefined) {
      clearTimeout(debounceTimer);
      debounceTimer = undefined;
    }
    if (maxTimer !== undefined) {
      clearTimeout(maxTimer);
      maxTimer = undefined;
    }
  };

  const flush = () => {
    const scheduledProjectId = pendingProjectId;
    clear();
    if (!scheduledProjectId || projectId().trim() !== scheduledProjectId) return;
    refresh();
  };

  const unsubscribe = subscribeArtifactChanges((changedProjectId) => {
    const target = projectId().trim();
    if (!target || changedProjectId !== target) return;
    if (pendingProjectId !== target) {
      clear();
      pendingProjectId = target;
      if (maxWaitMs > delayMs) {
        maxTimer = setTimeout(flush, maxWaitMs);
      }
    } else if (debounceTimer !== undefined) {
      clearTimeout(debounceTimer);
    }
    debounceTimer = setTimeout(flush, delayMs);
  });

  return () => {
    clear();
    unsubscribe();
  };
}
