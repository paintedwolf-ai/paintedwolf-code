import { createEffect, createSignal } from "solid-js";
import type { ArtifactEvent } from "../../api/types.ts";
import { LycaonApiError } from "../../api/http.ts";
import { invalidateVisualArtifactSrc } from "./visual-artifact-src.ts";

import { invalidateFrameArchive } from "./frame-archive-cache.ts";

type Listener = (projectId: string) => void;

const listeners = new Set<Listener>();
const [deletedIds, setDeletedIds] = createSignal<ReadonlySet<string>>(new Set());

export function artifactWasDeleted(artifactId: string): boolean {
  return deletedIds().has(artifactId.trim());
}

export function clearArtifactDeletionMemory(): void {
  setDeletedIds(new Set<string>());
}

function rememberDeletion(artifactId: string): void {
  const id = artifactId.trim();
  if (!id || deletedIds().has(id)) return;
  invalidateVisualArtifactSrc(id);
  invalidateFrameArchive(id);
  setDeletedIds((current) => new Set([...current, id]));
}

/** A tombstone fetched after reconnect updates every reference to that id. */
export function useArtifactDeletion(artifactId: () => string, error: () => unknown): void {
  createEffect(() => {
    const failure = error();
    if (failure instanceof LycaonApiError && failure.code === "artifact_deleted") {
      rememberDeletion(artifactId());
    }
  });
}

/** Announce that a project's durable artifacts changed. */
export function notifyArtifactChanged(event: ArtifactEvent): void {
  const id = event.project_id.trim();
  if (!id) return;
  const artifactId = event.artifact_id.trim();
  if (event.op === "deleted") rememberDeletion(artifactId);
  for (const listener of listeners) listener(id);
}

/** Subscribe to durable artifact changes. Returns an unsubscribe. */
export function subscribeArtifactChanges(listener: Listener): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
