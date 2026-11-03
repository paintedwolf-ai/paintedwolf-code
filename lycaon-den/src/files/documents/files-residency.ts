import { createEffect, onCleanup, untrack } from "solid-js";
import { DEFAULT_FRAME_LIMITS } from "../../ui/paged-view/controller.ts";
import { useResidentPresence } from "../../ui/resident-presence-context.tsx";
import { documentResidency, documentResourceKey, type DocumentReservation } from "./document-residency.ts";
import { editorReplica, suspendFileDocument } from "./editor-document.ts";
import { projectFilesState, type FileBuffer } from "./files-buffer-state.ts";

export { suspendFileDocument };

export function retainFileDocument(projectId: string, buffer: FileBuffer, reservation: DocumentReservation): void {
  const replica = editorReplica(buffer.documentId);
  // Reader frames, editor text and preparation can coexist during a page change.
  const bytes = replica?.estimatedBytes ?? (buffer.overLimit ? DEFAULT_FRAME_LIMITS.bytes * 4 : Math.max(65536, buffer.sizeBytes * (buffer.kind === "image" ? 8 : 6)));
  reservation.identify(documentResourceKey(projectId, buffer));
  reservation.retain(bytes, () => suspendFileDocument(projectId, buffer.rootId, buffer.path, buffer.documentId ?? undefined, buffer.jobId));
}

/** Both the displayed payload and its preparing replacement stay resident. */
export function createFilesResidency(projectId: string, displayed: () => string | null = () => null): void {
  const presence = useResidentPresence();
  const token = Symbol("file presentation");
  let protectedKeys = new Set<string>();
  createEffect(() => {
    const state = projectFilesState(projectId);
    const next = new Set<string>();
    if (presence() !== "idle") {
      for (const key of [state.activeKey, state.pendingKey, displayed()]) {
        const buffer = key ? state.byKey[key] : undefined;
        if (buffer) next.add(documentResourceKey(projectId, buffer));
      }
    }
    untrack(() => {
      for (const key of protectedKeys) if (!next.has(key)) documentResidency.protect(key, false, token);
      for (const key of next) documentResidency.protect(key, true, token);
      protectedKeys = next;
    });
  });
  onCleanup(() => { for (const key of protectedKeys) documentResidency.protect(key, false, token); });
}
