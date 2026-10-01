import { documentOutbox } from "./document-outbox.ts";
import { itemWindowViews, itemWindowInventoryReady } from "../../platform/windows/item-windows.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { createEffect, onCleanup, untrack } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { clientIdentity, prepareClientIdentity } from "../../platform/connection/client-identity.ts";
import { markFilesDocumentUnavailable } from "./project-files-buffers.ts";
import { applyFilesBufferDocumentStatus, projectFilesState } from "./files-buffer-state.ts";

const readers = new Map<string, () => Promise<void>>();
const publishers = new Map<string, () => void>();

/** Event gaps reconcile tab metadata in bounded batches without admitting bodies. */
export function refreshFileDocumentMetadata(projectId: string): Promise<void> {
  return readers.get(projectId)?.() ?? Promise.resolve();
}

/** Republishes this window's tab set even when unchanged, so a mark the host took outside it is dropped. */
export function refreshFileDocumentRetention(projectId: string): void {
  publishers.get(projectId)?.();
}

export function createFileDocumentMetadata(projectId: string, client: () => LycaonClient | null, reachable: () => boolean = () => true): void {
  let disposed = false;
  let reading: Promise<void> | undefined;
  const refresh = (): Promise<void> => reading ??= (async () => {
    const connected = client();
    if (!connected) return;
    const observed = new Map(Object.values(projectFilesState(projectId).byKey).flatMap(buffer => buffer.documentId
      ? [[buffer.documentId, { key: buffer.key, editRevision: buffer.editRevision, payloadGeneration: buffer.payloadGeneration }] as const] : []));
    const unchanged = (id: string): boolean => {
      const before = observed.get(id);
      if (!before) return false;
      const current = projectFilesState(projectId).byKey[before.key];
      return !!current && current.documentId === id && current.editRevision === before.editRevision && current.payloadGeneration === before.payloadGeneration;
    };
    const inventory = new Map((await documentOutbox().list(projectId)).map(entry => [entry.documentId, entry]));
    const ids = [...observed.keys()];
    for (let index = 0; index < ids.length && !disposed; index += 64) {
      const statuses = await connected.readEditorDocumentStatuses(projectId, ids.slice(index, index + 64));
      if (disposed) return;
      for (const id of statuses.missing) if (unchanged(id)) markFilesDocumentUnavailable(projectId, id);
      for (const status of statuses.documents) if (unchanged(status.id)) applyFilesBufferDocumentStatus(projectId, status, !inventory.has(status.id) || inventory.get(status.id)?.synchronized === true);
    }
  })().finally(() => { reading = undefined; });
  readers.set(projectId, refresh);

  let desired: string[] = [];
  let retainedClients: string[] | undefined;
  const inventory = () => JSON.stringify({ desired, retainedClients });
  let acknowledged = "";
  let publishing: Promise<void> | undefined;
  const publish = (): Promise<void> => {
    if (publishing) return publishing;
    if (disposed) return Promise.resolve();
    const connected = client();
    const signature = inventory();
    if (!connected) return Promise.reject(new Error("Waiting for the connection to preserve open tabs."));
    if (signature === acknowledged) return Promise.resolve();
    const captured = { ids: desired, clients: retainedClients };
    publishing = Promise.resolve().then(() => connected.replaceEditorDocumentRetention(projectId, clientIdentity(), captured.ids, captured.clients))
      .then(() => { acknowledged = signature; })
      .finally(() => { publishing = undefined; });
    return publishing;
  };
  const schedule = (): void => {
    void (async () => {
      if (!isTauriRuntime() && typeof navigator !== "undefined" && navigator.locks?.query) {
        await prepareClientIdentity();
        const locks = await navigator.locks.query();
        retainedClients = [...new Set([clientIdentity(), ...(locks.held ?? []).flatMap(lock => lock.name?.startsWith("editor-client:browser:") ? [lock.name.slice("editor-client:".length)] : [])])].sort();
      }
      if (disposed) return;
      const signature = inventory();
      try { await publish(); }
      finally { if (!disposed && signature !== inventory()) schedule(); }
    })().catch(() => undefined);
  };
  const republish = (): void => { acknowledged = ""; schedule(); };
  publishers.set(projectId, republish);
  let wasReachable = true;
  createEffect(() => {
    client();
    // After an outage the host may have swept this window's marks; the same set publishes again.
    const up = reachable();
    if (up && !wasReachable) acknowledged = "";
    wasReachable = up;
    retainedClients = isTauriRuntime() && itemWindowInventoryReady()
      ? [...new Set(["window:main", clientIdentity(), ...itemWindowViews().map(view => `window:${view.label}`)])].sort()
      : undefined;
    desired = [...new Set(Object.values(projectFilesState(projectId).byKey).flatMap(buffer => buffer.documentId ? [buffer.documentId] : []))].sort();
    untrack(schedule);
  });
  const timer = setInterval(schedule, 15_000);
  onCleanup(() => {
    disposed = true;
    clearInterval(timer);
    if (readers.get(projectId) === refresh) readers.delete(projectId);
    if (publishers.get(projectId) === republish) publishers.delete(projectId);
  });
}
