import { flushAppState } from "../../store/app-state-snapshot.ts";
/** Project/session draft and attachment synchronization boundary. */

import { untrack } from "solid-js";
import type { ComposerPendingAttachment } from "./composer-attachments.ts";
import {
  addPendingAttachments,
  clearPendingAttachments,
  pendingAttachmentsForSession,
  removePendingAttachment,
  replacePendingAttachmentsForSession,
} from "./composer-attachment-store.ts";
import {
  clearComposerDraft,
  composerDraftForSession,
  flushComposerDraftsToDisk,
  replaceComposerDraftFromShared,
  setComposerDraft,
} from "./composer-drafts.ts";
import {
  applySharedComposerMutation,
  acquireSharedComposerDocumentLease,
  clearSharedComposerDocument,
  publishSharedComposerDraft,
  protectSharedComposerSelection,
  removeSharedComposerAttachment,
  resolveSharedComposerDocument,
  resetSharedComposerDocumentsForTests,
  sharedComposerDocument,
  subscribeSharedComposerDocuments,
  type ChatDestination,
  type SharedComposerDocument,
} from "./shared-composer-document.ts";
import { windowViewId } from "../../platform/windows/window-subject.ts";
import { composerAttachmentCapabilities } from "./composer-attachments.ts";
import { INLINE_TEXT_TOO_LARGE_MESSAGE, inlineTextWithinLimit } from "./large-text-paste.ts";

const DRAFT_PUBLISH_DEBOUNCE_MS = 120;
const draftPublishTimers = new Map<string, ReturnType<typeof setTimeout>>();
const pendingDrafts = new Map<string, string>();
const editorClaims = new Map<string, Promise<SharedComposerDocument>>();
let stopMirror: (() => void) | undefined;

function key(destination: ChatDestination): string {
  return `${destination.projectId}\0${destination.sessionId}`;
}

function mirror(
  document: SharedComposerDocument,
  preservePendingDraft = false,
): void {
  replacePendingAttachmentsForSession(
    document.sessionId,
    document.attachments,
  );
  const documentKey = key(document);
  const pendingDraft = pendingDrafts.get(documentKey);
  if (pendingDraft !== undefined) {
    if (
      (preservePendingDraft || document.leaseClientId === windowViewId()) &&
      document.draft !== pendingDraft
    ) {
      return;
    }
    pendingDrafts.delete(documentKey);
  }
  replaceComposerDraftFromShared(document.sessionId, document.draft);
}

export function startComposerDocumentMirror(): () => void {
  stopMirror?.();
  stopMirror = subscribeSharedComposerDocuments(mirror);
  return () => {
    stopMirror?.();
    stopMirror = undefined;
  };
}

function seed(destination: ChatDestination) {
  return untrack(() => ({
    ...destination,
    draft: composerDraftForSession(destination.sessionId),
    attachments: pendingAttachmentsForSession(destination.sessionId),
  }));
}

export async function ensureComposerDocument(
  destination: ChatDestination,
): Promise<SharedComposerDocument> {
  const document = await resolveSharedComposerDocument(seed(destination));
  mirror(document, true);
  return document;
}

export async function acquireComposerDocumentLease(
  destination: ChatDestination,
): Promise<SharedComposerDocument> {
  const documentKey = key(destination);
  const claim = acquireSharedComposerDocumentLease(seed(destination));
  editorClaims.set(documentKey, claim);
  try {
    const document = await claim;
    mirror(document);
    return document;
  } finally {
    if (editorClaims.get(documentKey) === claim) {
      editorClaims.delete(documentKey);
    }
  }
}

function referenceProject(
  attachment: ComposerPendingAttachment,
): string | undefined {
  switch (attachment.kind) {
    case "path-file":
    case "path-folder":
    case "artifact":
    case "search-hit":
      return attachment.projectId.trim();
    default:
      return undefined;
  }
}

function compatible(
  destination: ChatDestination,
  attachments: readonly ComposerPendingAttachment[],
): boolean {
  return attachments.every((attachment) => {
    const projectId = referenceProject(attachment);
    return projectId == null || projectId === destination.projectId;
  });
}

export async function stageComposerMutation(
  destination: ChatDestination,
  attachments: ComposerPendingAttachment[],
  draftPrefill?: string,
): Promise<{ ok: true } | { ok: false; reason: string }> {
  const projectId = destination.projectId.trim();
  const sessionId = destination.sessionId.trim();
  if (!projectId || !sessionId) {
    return { ok: false, reason: "Choose a chat before attaching." };
  }
  const normalized = { projectId, sessionId };
  if (draftPrefill && !inlineTextWithinLimit(draftPrefill, composerAttachmentCapabilities())) {
    return { ok: false, reason: INLINE_TEXT_TOO_LARGE_MESSAGE };
  }
  if (!compatible(normalized, attachments)) {
    return {
      ok: false,
      reason: "Choose a chat in the same project as this reference.",
    };
  }

  const previousAttachments = pendingAttachmentsForSession(sessionId);
  const previousDraft = composerDraftForSession(sessionId);
  const result = addPendingAttachments(sessionId, attachments);
  if (!result.ok) return result;
  if (!previousDraft.trim() && draftPrefill) {
    setComposerDraft(sessionId, draftPrefill);
  }

  try {
    const document = await applySharedComposerMutation(
      {
        ...normalized,
        draft: previousDraft,
        attachments: previousAttachments,
      },
      attachments,
      draftPrefill,
    );
    // Preserve local text until its debounced publish.
    mirror(document, true);
    await flushAppState();
    return { ok: true };
  } catch {
    replacePendingAttachmentsForSession(sessionId, previousAttachments);
    if (composerDraftForSession(sessionId) !== previousDraft) {
      setComposerDraft(sessionId, previousDraft);
    }
    return { ok: false, reason: "Could not stage content in that chat." };
  }
}

export async function protectComposerSelection(
  destination: ChatDestination,
  draft: string,
  attachment: ComposerPendingAttachment,
): Promise<void> {
  cancelPendingDraft(destination);
  await acquireComposerDocumentLease(destination);
  const document = await protectSharedComposerSelection(destination, draft, attachment);
  mirror(document);
  await flushComposerDraftsToDisk();
}

/** Updates the local mirror before publishing the draft. */
export function updateComposerDocumentDraft(
  destination: ChatDestination,
  draft: string,
): boolean {
  if (!inlineTextWithinLimit(draft, composerAttachmentCapabilities())) return false;
  setComposerDraft(destination.sessionId, draft);
  const documentKey = key(destination);
  pendingDrafts.set(documentKey, draft);
  clearTimeout(draftPublishTimers.get(documentKey));
  draftPublishTimers.set(
    documentKey,
    setTimeout(() => {
      draftPublishTimers.delete(documentKey);
      void publishPendingDraft(destination, draft);
    }, DRAFT_PUBLISH_DEBOUNCE_MS),
  );
  return true;
}

async function publishPendingDraft(
  destination: ChatDestination,
  draft: string,
): Promise<void> {
  const documentKey = key(destination);
  await editorClaims.get(documentKey)?.catch(() => undefined);
  for (let attempt = 0; attempt < 2; attempt += 1) {
    if (pendingDrafts.get(documentKey) !== draft) return;
    try {
      mirror(await publishSharedComposerDraft(destination, draft));
      return;
    } catch {
      const latest = sharedComposerDocument(destination);
      if (latest?.leaseClientId !== windowViewId()) break;
    }
  }
  if (pendingDrafts.get(documentKey) === draft) {
    const latest = sharedComposerDocument(destination);
    if (latest?.leaseClientId !== windowViewId()) {
      pendingDrafts.delete(documentKey);
      if (latest) mirror(latest);
    }
  }
}

function cancelPendingDraft(destination: ChatDestination): void {
  const documentKey = key(destination);
  clearTimeout(draftPublishTimers.get(documentKey));
  draftPublishTimers.delete(documentKey);
  pendingDrafts.delete(documentKey);
}

/** Publishes pending keystrokes before a window releases its composer leases. */
export async function flushComposerDocumentsToDisk(): Promise<void> {
  // Capture local drafts immediately, including those still inside the debounce.
  await flushComposerDraftsToDisk();
  for (const [documentKey, draft] of [...pendingDrafts]) {
    clearTimeout(draftPublishTimers.get(documentKey));
    draftPublishTimers.delete(documentKey);
    const separator = documentKey.indexOf("\0");
    const projectId = documentKey.slice(0, separator);
    const sessionId = documentKey.slice(separator + 1);
    await publishPendingDraft({ projectId, sessionId }, draft);
    if (pendingDrafts.has(documentKey)) {
      throw new Error("Could not preserve the latest chat draft.");
    }
  }
  await flushComposerDraftsToDisk();
}

/** Replaces draft text after an explicit chat action. */
export async function replaceComposerDocumentDraft(
  destination: ChatDestination,
  draft: string,
): Promise<void> {
  if (!inlineTextWithinLimit(draft, composerAttachmentCapabilities())) {
    throw new Error(INLINE_TEXT_TOO_LARGE_MESSAGE);
  }
  cancelPendingDraft(destination);
  pendingDrafts.set(key(destination), draft);
  setComposerDraft(destination.sessionId, draft);
  for (let attempt = 0; attempt < 2; attempt += 1) {
    await acquireComposerDocumentLease(destination);
    try {
      mirror(await publishSharedComposerDraft(destination, draft));
      return;
    } catch {
      if (attempt === 1) {
        pendingDrafts.delete(key(destination));
        const latest = sharedComposerDocument(destination);
        if (latest) mirror(latest);
        throw new Error("Could not update that chat draft.");
      }
    }
  }
}

export async function removeComposerDocumentAttachment(
  destination: ChatDestination,
  attachmentId: string,
): Promise<void> {
  removePendingAttachment(destination.sessionId, attachmentId);
  const current = sharedComposerDocument(destination);
  if (!current) await ensureComposerDocument(destination);
  try {
    mirror(await removeSharedComposerAttachment(destination, attachmentId));
    await flushAppState();
  } catch {
    await ensureComposerDocument(destination);
  }
}

export async function clearComposerDocumentAttachments(
  destination: ChatDestination,
): Promise<void> {
  clearPendingAttachments(destination.sessionId);
  const current = sharedComposerDocument(destination);
  if (!current) await ensureComposerDocument(destination);
  try {
    mirror(await clearSharedComposerDocument(destination, false));
    await flushAppState();
  } catch {
    await ensureComposerDocument(destination);
  }
}

export async function consumeComposerDocument(
  destination: ChatDestination,
): Promise<void> {
  cancelPendingDraft(destination);
  clearPendingAttachments(destination.sessionId);
  clearComposerDraft(destination.sessionId);
  await flushComposerDraftsToDisk();
  for (let attempt = 0; attempt < 2; attempt += 1) {
    if (!sharedComposerDocument(destination)) {
      await ensureComposerDocument(destination);
    }
    try {
      mirror(await clearSharedComposerDocument(destination, true));
      await flushAppState();
      return;
    } catch {
      if (attempt === 1) {
        throw new Error("Message sent, but its staged composer state could not be cleared.");
      }
      await ensureComposerDocument(destination);
    }
  }
}

export function resetComposerDocumentStoreForTests(): void {
  for (const timer of draftPublishTimers.values()) clearTimeout(timer);
  draftPublishTimers.clear();
  pendingDrafts.clear();
  editorClaims.clear();
  stopMirror?.();
  stopMirror = undefined;
  resetSharedComposerDocumentsForTests();
}
