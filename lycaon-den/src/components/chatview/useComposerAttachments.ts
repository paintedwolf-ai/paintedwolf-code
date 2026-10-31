import {
  attachmentChipDetail,
  attachmentChipLabel,
  attachmentPreviewSnippet,
  attachmentTurnBytes,
  composerAttachmentCapabilities,
  countsAsImage,
  fileToComposerAttachment,
  formatAttachmentBytes,
  isAllowedImageMime,
  isAllowedVideoMime,
  isByteAttachmentKind,
  isPayloadAttachment,
  isSendablePendingAttachment,
  rejectMessageForCode,
  type ComposerPendingAttachment,
} from "../../chat/composer/composer-attachments.ts";
import { pendingAttachmentsForSession } from "../../chat/composer/composer-attachment-store.ts";
import {
  clearComposerDocumentAttachments,
  removeComposerDocumentAttachment,
  stageComposerMutation,
} from "../../chat/composer/composer-document-store.ts";
import type { ChatDestination } from "../../chat/composer/shared-composer-document.ts";
import type { CoordinatorVisionSupport } from "../../chat/composer/composer-vision.ts";
import { exitRecall } from "../../chat/composer/prompt-recall.ts";
import { shouldAttachTextPaste } from "../../chat/composer/large-text-paste.ts";
import { createPastedTextAttachmentController } from "../../chat/composer/pasted-text-attachment-controller.ts";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";

/** Unreachable hosts leave a rejected file chip. */
export function rejectChipFor(name: string): ComposerPendingAttachment {
  return {
    id: crypto.randomUUID(),
    kind: "reject",
    name,
    mime: "application/octet-stream",
    byteLength: 0,
    rejectMessage: rejectMessageForCode("unsupported_attachment"),
  };
}

export function chipTitle(att: ComposerPendingAttachment): string {
  if (att.kind === "reject") return att.rejectMessage || att.name;
  if (
    att.kind === "path-file" ||
    att.kind === "path-folder" ||
    att.kind === "external-file" ||
    att.kind === "external-folder"
  ) {
    return `${attachmentChipLabel(att)} · ${att.path}`;
  }
  if (att.kind === "artifact") return att.name;
  if (att.kind === "search-hit") return `${att.name} · ${att.sourceRef}`;
  if (att.kind === "secret") return `${att.name} · ${attachmentChipDetail(att)}`;
  return `${att.name} · ${formatAttachmentBytes(att.byteLength)} · ${attachmentPreviewSnippet(att)}`;
}

export type UseComposerAttachmentsOptions = {
  sessionId: () => string;
  destination: () => ChatDestination;
  visionSupport: () => CoordinatorVisionSupport | undefined;
  currentText: () => string;
  setComposerError: (target: ChatDestination, message: string | null) => void;
  scheduleFocus: () => void;
};

export function useComposerAttachments(options: UseComposerAttachmentsOptions) {
  const attachments = () => pendingAttachmentsForSession(options.sessionId());
  const pastedTextAttachments = createPastedTextAttachmentController({
    destination: options.destination,
    clearError: (target) => options.setComposerError(target, null),
  });
  const visiblePastedTextUploads = pastedTextAttachments.visibleUploads;
  const pasteAnnouncement = pastedTextAttachments.announcement;

  const sendableAttachments = () => attachments().filter(isSendablePendingAttachment);
  const hasRejectChips = () =>
    attachments().some((a) => a.kind === "reject") ||
    visiblePastedTextUploads().some((upload) => upload.status === "failed");
  const hasPastedTextUploads = () => visiblePastedTextUploads().length > 0;
  const imageCount = () => attachments().filter((a) => countsAsImage(a)).length;
  const byteAttachmentCount = () =>
    attachments().filter((a) => isByteAttachmentKind(a.kind)).length;
  const hasByteAttachments = () => attachments().some(isPayloadAttachment);

  // Unknown vision support remains distinct from unsupported.
  const visionHint = () => {
    if (imageCount() === 0 || options.visionSupport() !== "unsupported") return null;
    const media = attachments().some((a) => a.kind === "video")
      ? "images or video frames"
      : "images";
    return `The active model cannot see ${media}. They will still appear in the transcript.`;
  };

  const clearAttachments = () => {
    const target = options.destination();
    pastedTextAttachments.cancelVisible();
    options.setComposerError(target, null);
    void clearComposerDocumentAttachments(target);
  };

  const interceptTextPaste = (pasted: string, start: number, end: number): boolean => {
    if (!pasted) return false;
    const shouldAttach = shouldAttachTextPaste({
      current: options.currentText(),
      selectionStart: start,
      selectionEnd: end,
      pasted,
      capabilities: composerAttachmentCapabilities(),
    });
    if (!shouldAttach) return false;
    exitRecall(options.sessionId());
    pastedTextAttachments.stage(pasted);
    return true;
  };

  const addFiles = async (files: File[]) => {
    if (files.length === 0) return;
    const target = options.destination();
    options.setComposerError(target, null);
    const caps = composerAttachmentCapabilities();
    if (!caps) {
      options.setComposerError(
        target,
        "Attachments are unavailable until host readiness loads.",
      );
      return;
    }
    const activePastedUploads = visiblePastedTextUploads().filter(
      (upload) => upload.status === "uploading",
    );
    let availableCount = Math.max(
      0,
      caps.max_attachments - byteAttachmentCount() - activePastedUploads.length,
    );
    let availableBytes = Math.max(
      0,
      caps.max_turn_bytes -
        attachmentTurnBytes(attachments()) -
        activePastedUploads.reduce((sum, upload) => sum + upload.file.size, 0),
    );
    let availableImages = Math.max(0, caps.max_images - imageCount());
    const acceptedFiles: File[] = [];
    for (const file of files) {
      const looksLikeVideo = isAllowedVideoMime(file.type);
      // A video reaches the model as one frame sheet, so it spends an image slot too.
      const spendsImage = looksLikeVideo || isAllowedImageMime(file.type);
      if (
        availableCount <= 0 ||
        file.size > availableBytes ||
        (looksLikeVideo && file.size > caps.max_video_bytes) ||
        (spendsImage && availableImages <= 0)
      ) {
        continue;
      }
      acceptedFiles.push(file);
      availableCount -= 1;
      availableBytes -= file.size;
      if (spendsImage) availableImages -= 1;
    }
    if (acceptedFiles.length === 0) {
      options.setComposerError(target, rejectMessageForCode("attachment_too_large"));
      return;
    }
    if (acceptedFiles.length < files.length) {
      options.setComposerError(target, rejectMessageForCode("attachment_too_large"));
    }
    const pending: ComposerPendingAttachment[] = [];
    for (const file of acceptedFiles) {
      const client = getLycaonClient();
      const attachment = client
        ? await fileToComposerAttachment(file, target.projectId, client)
        : rejectChipFor(file.name);
      pending.push(attachment);
      if (attachment.kind === "reject" && attachment.rejectMessage) {
        options.setComposerError(target, attachment.rejectMessage);
      }
    }
    const result = await stageComposerMutation(target, pending);
    if (!result.ok) options.setComposerError(target, result.reason);
    // File-picker blur drops the composer focus claim.
    options.scheduleFocus();
  };

  const removeAttachment = (id: string) => {
    const target = options.destination();
    options.setComposerError(target, null);
    void removeComposerDocumentAttachment(target, id);
  };

  const isAttachLimitReached = () =>
    !composerAttachmentCapabilities() ||
    byteAttachmentCount() +
      visiblePastedTextUploads().filter((upload) => upload.status === "uploading").length >=
      (composerAttachmentCapabilities()?.max_attachments ?? 0);

  return {
    attachments,
    pastedTextAttachments,
    visiblePastedTextUploads,
    pasteAnnouncement,
    sendableAttachments,
    hasRejectChips,
    hasPastedTextUploads,
    hasByteAttachments,
    visionHint,
    clearAttachments,
    interceptTextPaste,
    addFiles,
    removeAttachment,
    isAttachLimitReached,
  };
}
