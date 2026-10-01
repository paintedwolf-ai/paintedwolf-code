/** Pending composer state shared by workspace views. */

import { createSignal, untrack } from "solid-js";
import { invoke } from "@tauri-apps/api/core";
import type { ComposerPendingAttachment } from "./composer-attachments.ts";
import { isTauriRuntime } from "../../platform/runtime.ts";
import { listenHostEvent } from "../../platform/windows/window-channel.ts";
import { windowViewId } from "../../platform/windows/window-subject.ts";
import { getAppStateSnapshot } from "../../store/app-state-snapshot.ts";
import { persistAppStateInBackground } from "../../store/app-state-background-write.ts";
import { parseComposerAttachmentDrafts } from "./composer-attachment-persistence.ts";

export type ChatDestination = {
  projectId: string;
  sessionId: string;
};

export type SharedComposerDocument = ChatDestination & {
  draft: string;
  attachments: ComposerPendingAttachment[];
  revision: number;
  leaseClientId?: string;
};

type ComposerDocumentSeed = ChatDestination & {
  draft: string;
  attachments: ComposerPendingAttachment[];
};

const [documents, setDocuments] = createSignal<
  Record<string, SharedComposerDocument>
>({});
const listeners = new Set<(document: SharedComposerDocument) => void>();
const localPreviewUrls = new Map<string, string>();
let listening = false;
let stopHostListening: (() => void) | undefined;
let hostListenGeneration = 0;

function key(destination: ChatDestination): string {
  return `${destination.projectId}\0${destination.sessionId}`;
}

function previewUrlOf(
  attachment: ComposerPendingAttachment,
): string | undefined {
  return "previewUrl" in attachment ? attachment.previewUrl : undefined;
}

function previewKey(destination: ChatDestination, attachmentId: string): string {
  return `${key(destination)}\0${attachmentId}`;
}

function serializableAttachment(
  attachment: ComposerPendingAttachment,
): ComposerPendingAttachment {
  const serializable = { ...attachment };
  if ("previewUrl" in serializable) delete serializable.previewUrl;
  return serializable;
}

function restoreSeed(seed: ComposerDocumentSeed): ComposerDocumentSeed {
  const retained = parseComposerAttachmentDrafts(getAppStateSnapshot().composerAttachments)[seed.sessionId];
  return seed.attachments.length === 0 && retained?.projectId === seed.projectId
    ? { ...seed, attachments: retained.attachments }
    : seed;
}

function serializableSeed(seed: ComposerDocumentSeed): ComposerDocumentSeed {
  seed = restoreSeed(seed);
  return {
    ...seed,
    attachments: seed.attachments.map(serializableAttachment),
  };
}

function note(document: SharedComposerDocument): SharedComposerDocument {
  const previous = untrack(documents)[key(document)];
  if (previous && previous.revision > document.revision) return previous;
  const previews = new Map(
    (previous?.attachments ?? []).flatMap((attachment) => {
      const previewUrl = previewUrlOf(attachment);
      return previewUrl ? [[attachment.id, previewUrl]] : [];
    }),
  );
  const localDocument = {
    ...document,
    attachments: document.attachments.map((attachment) => {
      const previewUrl = previewUrlOf(attachment);
      const preserved =
        previewUrl ??
        previews.get(attachment.id) ??
        localPreviewUrls.get(previewKey(document, attachment.id));
      return {
        ...attachment,
        ...(preserved ? { previewUrl: preserved } : {}),
      };
    }),
  };
  const present = new Set(
    localDocument.attachments.map((attachment) => attachment.id),
  );
  const prefix = `${key(document)}\0`;
  for (const localKey of localPreviewUrls.keys()) {
    const attachmentId = localKey.slice(prefix.length);
    if (localKey.startsWith(prefix) && !present.has(attachmentId)) {
      localPreviewUrls.delete(localKey);
    }
  }
  setDocuments((current) => ({
    ...current,
    [key(document)]: localDocument,
  }));
  const retained = parseComposerAttachmentDrafts(getAppStateSnapshot().composerAttachments);
  const before = JSON.stringify(retained);
  const updated = parseComposerAttachmentDrafts({ [document.sessionId]: document });
  delete retained[document.sessionId];
  const next = parseComposerAttachmentDrafts({ ...retained, ...updated });
  if (JSON.stringify(next) !== before) {
    void persistAppStateInBackground({ composerAttachments: Object.keys(next).length ? next : undefined });
  }
  for (const listener of listeners) listener(localDocument);
  return localDocument;
}

function localDocument(seed: ComposerDocumentSeed): SharedComposerDocument {
  return (
    untrack(documents)[key(seed)] ?? {
      ...restoreSeed(seed),
      revision: 1,
    }
  );
}

export function sharedComposerDocument(
  destination: ChatDestination,
): SharedComposerDocument | undefined {
  return documents()[key(destination)];
}

export async function resolveSharedComposerDocument(
  seed: ComposerDocumentSeed,
): Promise<SharedComposerDocument> {
  if (!isTauriRuntime()) return note(localDocument(seed));
  return note(
    await invoke<SharedComposerDocument>("resolve_shared_composer_document", {
      seed: serializableSeed(seed),
    }),
  );
}

export async function acquireSharedComposerDocumentLease(
  seed: ComposerDocumentSeed,
): Promise<SharedComposerDocument> {
  if (!isTauriRuntime()) {
    return note({
      ...localDocument(seed),
      leaseClientId: windowViewId(),
    });
  }
  return note(
    await invoke<SharedComposerDocument>(
      "acquire_shared_composer_document_lease",
      { seed: serializableSeed(seed), viewId: windowViewId() },
    ),
  );
}

export async function publishSharedComposerDraft(
  destination: ChatDestination,
  draft: string,
): Promise<SharedComposerDocument> {
  const current = sharedComposerDocument(destination);
  if (!current) {
    throw new Error("Shared composer document was not resolved.");
  }
  if (!isTauriRuntime()) {
    return note({ ...current, draft, revision: current.revision + 1 });
  }
  try {
    return note(
      await invoke<SharedComposerDocument>(
        "update_shared_composer_document_draft",
        {
          ...destination,
          viewId: windowViewId(),
          revision: current.revision,
          draft,
        },
      ),
    );
  } catch (error) {
    await resolveSharedComposerDocument({
      ...destination,
      draft: current.draft,
      attachments: current.attachments,
    });
    throw error;
  }
}

export async function applySharedComposerMutation(
  seed: ComposerDocumentSeed,
  attachments: ComposerPendingAttachment[],
  draftPrefill?: string,
): Promise<SharedComposerDocument> {
  if (!isTauriRuntime()) {
    const current = localDocument(seed);
    const known = new Set(current.attachments.map((attachment) => attachment.id));
    const additions = attachments.filter((attachment) => !known.has(attachment.id));
    return note({
      ...current,
      attachments: [...current.attachments, ...additions],
      draft:
        current.draft.trim() || !draftPrefill ? current.draft : draftPrefill,
      revision: current.revision + 1,
    });
  }
  for (const attachment of attachments) {
    const previewUrl = previewUrlOf(attachment);
    if (previewUrl) {
      localPreviewUrls.set(previewKey(seed, attachment.id), previewUrl);
    }
  }
  const serializableAttachments = attachments.map(serializableAttachment);
  try {
    return note(
      await invoke<SharedComposerDocument>(
        "apply_shared_composer_document_mutation",
        {
          seed: serializableSeed(seed),
          attachments: serializableAttachments,
          draftPrefill,
        },
      ),
    );
  } catch (error) {
    for (const attachment of attachments) {
      localPreviewUrls.delete(previewKey(seed, attachment.id));
    }
    throw error;
  }
}

export async function protectSharedComposerSelection(
  destination: ChatDestination,
  draft: string,
  attachment: ComposerPendingAttachment,
): Promise<SharedComposerDocument> {
  const current = sharedComposerDocument(destination);
  if (!current) throw new Error("Shared composer document was not resolved.");
  if (attachment.kind !== "secret") {
    throw new Error("Protected composer updates require a secret attachment.");
  }
  if (!isTauriRuntime()) {
    return note({
      ...current,
      draft,
      attachments: current.attachments.some((item) => item.id === attachment.id)
        ? current.attachments
        : [...current.attachments, attachment],
      revision: current.revision + 1,
    });
  }
  return note(
    await invoke<SharedComposerDocument>("protect_shared_composer_selection", {
      ...destination,
      viewId: windowViewId(),
      revision: current.revision,
      draft,
      attachment: serializableAttachment(attachment),
    }),
  );
}

export async function removeSharedComposerAttachment(
  destination: ChatDestination,
  attachmentId: string,
): Promise<SharedComposerDocument> {
  localPreviewUrls.delete(previewKey(destination, attachmentId));
  const current = sharedComposerDocument(destination);
  if (!current) {
    throw new Error("Shared composer document was not resolved.");
  }
  if (!isTauriRuntime()) {
    return note({
      ...current,
      attachments: current.attachments.filter(
        (attachment) => attachment.id !== attachmentId,
      ),
      revision: current.revision + 1,
    });
  }
  return note(
    await invoke<SharedComposerDocument>(
      "remove_shared_composer_document_attachment",
      { ...destination, attachmentId },
    ),
  );
}

export async function clearSharedComposerDocument(
  destination: ChatDestination,
  clearDraft: boolean,
): Promise<SharedComposerDocument> {
  const prefix = `${key(destination)}\0`;
  for (const localKey of localPreviewUrls.keys()) {
    if (localKey.startsWith(prefix)) localPreviewUrls.delete(localKey);
  }
  const current = sharedComposerDocument(destination);
  if (!current) {
    throw new Error("Shared composer document was not resolved.");
  }
  if (!isTauriRuntime()) {
    return note({
      ...current,
      attachments: [],
      draft: clearDraft ? "" : current.draft,
      revision: current.revision + 1,
    });
  }
  return note(
    await invoke<SharedComposerDocument>("clear_shared_composer_document", {
      ...destination,
      clearDraft,
    }),
  );
}

export function subscribeSharedComposerDocuments(
  listener: (document: SharedComposerDocument) => void,
): () => void {
  listeners.add(listener);
  if (!listening && isTauriRuntime()) {
    listening = true;
    const generation = ++hostListenGeneration;
    void listenHostEvent<SharedComposerDocument>(
      "shared-composer-document-changed",
      ({ payload }) => note(payload),
    )
      .then((stop) => {
        if (generation !== hostListenGeneration || listeners.size === 0) {
          stop();
          listening = false;
          return;
        }
        stopHostListening = stop;
      })
      .catch((error) => {
        if (generation === hostListenGeneration) listening = false;
        console.debug("[shared-composer-document] subscription failed", error);
      });
  }
  return () => {
    listeners.delete(listener);
    if (listeners.size === 0) {
      hostListenGeneration += 1;
      stopHostListening?.();
      stopHostListening = undefined;
      listening = false;
    }
  };
}

export function resetSharedComposerDocumentsForTests(): void {
  setDocuments({});
  listeners.clear();
  localPreviewUrls.clear();
  hostListenGeneration += 1;
  stopHostListening?.();
  stopHostListening = undefined;
  listening = false;
}
