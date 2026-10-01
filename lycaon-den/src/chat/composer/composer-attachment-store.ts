/** Local reactive attachment mirror for the host-managed composer document. */

import { createSignal } from "solid-js";
import {
  composerAttachmentCapabilities,
  countsAsImage,
  isByteAttachmentKind,
  isPayloadAttachment,
  isReferenceKind,
  rejectMessageForCode,
  revokeComposerAttachment,
  wouldExceedTurnBytes,
  type ComposerPendingAttachment,
} from "./composer-attachments.ts";

const [bySession, setBySession] = createSignal<
  Record<string, ComposerPendingAttachment[]>
>({});

/** Reactive pending attachments for a session (empty when none). */
export function pendingAttachmentsForSession(
  sessionId: string | null | undefined,
): ComposerPendingAttachment[] {
  const id = sessionId?.trim();
  if (!id) return [];
  return bySession()[id] ?? [];
}

function addResult(
  current: readonly ComposerPendingAttachment[],
  attachment: ComposerPendingAttachment,
): { ok: true } | { ok: false; reason: string } {
  const plane = planeCount(current, attachment);
  if (plane.cap <= 0) {
    return { ok: false, reason: "Attachments are unavailable until host readiness loads." };
  }
  if (plane.used >= plane.cap) {
    return { ok: false, reason: rejectMessageForCode("attachment_too_large") };
  }
  if (
    attachment.kind === "image" &&
    current.filter((candidate) => countsAsImage(candidate.kind)).length >=
      (composerAttachmentCapabilities()?.max_images ?? 0)
  ) {
    return { ok: false, reason: rejectMessageForCode("attachment_too_large") };
  }
  if (
    isPayloadAttachment(attachment) &&
    wouldExceedTurnBytes(current, attachment.byteLength)
  ) {
    return { ok: false, reason: rejectMessageForCode("attachment_too_large") };
  }
  return { ok: true };
}

/** Chips of the same budget plane already staged for this session. */
function planeCount(
  current: readonly ComposerPendingAttachment[],
  attachment: ComposerPendingAttachment,
): { used: number; cap: number } {
  if (isReferenceKind(attachment.kind)) {
    return {
      used: current.filter((a) => isReferenceKind(a.kind)).length,
      cap: composerAttachmentCapabilities()?.max_references ?? 0,
    };
  }
  return {
    used: current.filter((a) => isByteAttachmentKind(a.kind)).length,
    cap: composerAttachmentCapabilities()?.max_attachments ?? 0,
  };
}

/** Atomically append a mutation after validating every item against one snapshot. */
export function addPendingAttachments(
  sessionId: string | null | undefined,
  attachments: ComposerPendingAttachment[],
): { ok: true } | { ok: false; reason: string } {
  const id = sessionId?.trim();
  if (!id) {
    for (const attachment of attachments) revokeComposerAttachment(attachment);
    return { ok: false, reason: "Choose a chat before attaching." };
  }
  const current = bySession()[id] ?? [];
  const additions: ComposerPendingAttachment[] = [];
  for (const attachment of attachments) {
    const result = addResult([...current, ...additions], attachment);
    if (!result.ok) {
      for (const candidate of attachments) revokeComposerAttachment(candidate);
      return result;
    }
    additions.push(attachment);
  }
  setBySession((previous) => ({
    ...previous,
    [id]: [...(previous[id] ?? []), ...additions],
  }));
  return { ok: true };
}

/** Replace one session from the host-managed canonical document. */
export function replacePendingAttachmentsForSession(
  sessionId: string,
  attachments: ComposerPendingAttachment[],
): void {
  const id = sessionId.trim();
  if (!id) return;
  setBySession((previous) => {
    const retained = new Set(attachments.map((attachment) => attachment.id));
    for (const attachment of previous[id] ?? []) {
      if (!retained.has(attachment.id)) revokeComposerAttachment(attachment);
    }
    const next = { ...previous };
    if (attachments.length === 0) delete next[id];
    else next[id] = attachments;
    return next;
  });
}

export function removePendingAttachment(
  sessionId: string | null | undefined,
  attachmentId: string,
): void {
  const id = sessionId?.trim();
  if (!id) return;
  setBySession((prev) => {
    const list = prev[id];
    if (!list) return prev;
    const victim = list.find((a) => a.id === attachmentId);
    if (victim) revokeComposerAttachment(victim);
    const nextList = list.filter((a) => a.id !== attachmentId);
    const next = { ...prev };
    if (nextList.length === 0) delete next[id];
    else next[id] = nextList;
    return next;
  });
}

export function clearPendingAttachments(
  sessionId: string | null | undefined,
): void {
  const id = sessionId?.trim();
  if (!id) return;
  setBySession((prev) => {
    const list = prev[id];
    if (!list) return prev;
    for (const att of list) revokeComposerAttachment(att);
    const next = { ...prev };
    delete next[id];
    return next;
  });
}

/** Reset module state between test cases. */
export function resetComposerAttachmentsForTests(): void {
  const snap = bySession();
  for (const list of Object.values(snap)) {
    for (const att of list) revokeComposerAttachment(att);
  }
  setBySession({});
}
