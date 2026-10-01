import { createSignal, onCleanup, type Accessor } from "solid-js";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import {
  attachmentTurnBytes,
  composerAttachmentCapabilities,
  fileToComposerAttachment,
  formatAttachmentBytes,
  isByteAttachmentKind,
  rejectMessageForCode,
} from "./composer-attachments.ts";
import { pendingAttachmentsForSession } from "./composer-attachment-store.ts";
import {
  removeComposerDocumentAttachment,
  stageComposerMutation,
} from "./composer-document-store.ts";
import type { ChatDestination } from "./shared-composer-document.ts";

const PASTED_TEXT_FILENAME = "Pasted text.txt";

type PastedTextUpload = ChatDestination & {
  id: string;
  file: File;
  status: "uploading" | "failed";
  error?: string;
};

type PastedTextAttachmentControllerOptions = {
  destination: Accessor<ChatDestination>;
  clearError: (destination: ChatDestination) => void;
};

function destinationKey(destination: ChatDestination): string {
  return `${destination.projectId}\0${destination.sessionId}`;
}

export function createPastedTextAttachmentController(
  options: PastedTextAttachmentControllerOptions,
) {
  const [uploads, setUploads] = createSignal<PastedTextUpload[]>([]);
  const [announcement, setAnnouncement] = createSignal("");
  const canceledUploadIDs = new Set<string>();

  const visibleUploads = () => {
    const currentKey = destinationKey(options.destination());
    return uploads().filter((upload) => destinationKey(upload) === currentKey);
  };

  const fail = (id: string, error: string) => {
    setUploads((current) =>
      current.map((upload) =>
        upload.id === id ? { ...upload, status: "failed", error } : upload,
      ),
    );
    setAnnouncement(`Pasted text could not be attached. ${error}`);
  };

  const upload = async (item: PastedTextUpload) => {
    canceledUploadIDs.delete(item.id);
    setUploads((current) =>
      current.map((candidate) =>
        candidate.id === item.id
          ? { ...candidate, status: "uploading", error: undefined }
          : candidate,
      ),
    );
    const client = getLycaonClient();
    if (!client) {
      fail(item.id, "Reconnect to retry this attachment.");
      return;
    }
    const attachment = await fileToComposerAttachment(item.file, item.projectId, client);
    if (canceledUploadIDs.delete(item.id)) return;
    if (attachment.kind === "reject") {
      fail(
        item.id,
        attachment.rejectMessage ?? rejectMessageForCode("unsupported_attachment"),
      );
      return;
    }
    const result = await stageComposerMutation(item, [attachment]);
    if (canceledUploadIDs.delete(item.id)) {
      if (result.ok) await removeComposerDocumentAttachment(item, attachment.id);
      return;
    }
    if (!result.ok) {
      fail(item.id, result.reason);
      return;
    }
    setUploads((current) => current.filter((candidate) => candidate.id !== item.id));
    setAnnouncement(`Pasted text attached, ${formatAttachmentBytes(item.file.size)}.`);
  };

  const stage = (pasted: string) => {
    const item: PastedTextUpload = {
      ...options.destination(),
      id: crypto.randomUUID(),
      file: new File([pasted], PASTED_TEXT_FILENAME, { type: "text/plain" }),
      status: "uploading",
    };
    options.clearError(item);
    const caps = composerAttachmentCapabilities();
    const current = pendingAttachmentsForSession(item.sessionId);
    const activeUploads = uploads().filter(
      (candidate) =>
        candidate.status === "uploading" &&
        destinationKey(candidate) === destinationKey(item),
    );
    const currentCount = current.filter((attachment) =>
      isByteAttachmentKind(attachment.kind),
    ).length;
    const currentBytes =
      attachmentTurnBytes(current) +
      activeUploads.reduce((sum, upload) => sum + upload.file.size, 0);
    if (
      !caps ||
      currentCount + activeUploads.length >= caps.max_attachments ||
      item.file.size > caps.max_upload_bytes ||
      currentBytes + item.file.size > caps.max_turn_bytes
    ) {
      const error = caps
        ? rejectMessageForCode("attachment_too_large")
        : "Attachments are unavailable until host readiness loads.";
      setUploads((currentUploads) => [
        ...currentUploads,
        { ...item, status: "failed", error },
      ]);
      setAnnouncement(`Pasted text could not be attached. ${error}`);
      return;
    }
    setUploads((current) => [...current, item]);
    setAnnouncement(
      `Large paste is being attached as text, ${formatAttachmentBytes(item.file.size)}.`,
    );
    void upload(item);
  };

  const remove = (id: string) => {
    const item = uploads().find((candidate) => candidate.id === id);
    if (item?.status === "uploading") canceledUploadIDs.add(id);
    setUploads((current) => current.filter((candidate) => candidate.id !== id));
  };

  const cancelVisible = () => {
    const visibleIDs = new Set(visibleUploads().map((item) => item.id));
    for (const item of uploads()) {
      if (visibleIDs.has(item.id) && item.status === "uploading") {
        canceledUploadIDs.add(item.id);
      }
    }
    setUploads((current) => current.filter((item) => !visibleIDs.has(item.id)));
  };

  onCleanup(() => {
    for (const item of uploads()) {
      if (item.status === "uploading") canceledUploadIDs.add(item.id);
    }
  });

  return {
    announcement,
    visibleUploads,
    stage,
    retry: upload,
    remove,
    cancelVisible,
  };
}
