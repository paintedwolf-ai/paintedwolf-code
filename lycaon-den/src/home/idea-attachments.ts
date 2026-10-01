/** Home buffers attachments until its draft project exists. */

import { basenameOfPath } from "../api/project-path.ts";
import type { LycaonClient } from "../api/client.ts";
import {
  attachmentKindGlyph,
  attachmentReceiptToPending,
  attachmentRejectCode,
  byteAttachmentIntakeReject,
  fileToComposerAttachment,
  expectedPayloadKind,
  formatAttachmentBytes,
  isAllowedImageMime,
  mediaMimeFromPath,
  isSendablePendingAttachment,
  rejectMessageForCode,
  revokeComposerAttachment,
  type ComposerPendingAttachment,
} from "../chat/composer/composer-attachments.ts";
import { promptPartsFromPendingAttachments, type PromptParts } from "../chat/composer/prompt-parts.ts";
import {
  DROP_REJECT_MISSING,
  type DroppedItem,
} from "../platform/files/file-drop.ts";
import { importExternalAttachment } from "../platform/files/external-attachment-import.ts";
import { importPathKind } from "../platform/files/read-path-bytes.ts";

export type HomeIdeaAttachment =
  | {
      id: string;
      kind: "web-file";
      name: string;
      file: File;
      mime: string;
      byteLength: number;
      previewUrl?: string;
    }
  | {
      id: string;
      kind: "path";
      name: string;
      absolutePath: string;
      /** Extension-guessed; the host decides the real kind at import. */
      expected: "image" | "video" | "document";
    }
  | { id: string; kind: "reject"; name: string; reason: string };

/** What Home submits: the idea text plus its locally buffered attachments. */
export type HomeIdeaDraft = {
  text: string;
  attachments: readonly HomeIdeaAttachment[];
};

export const HOME_IDEA_FOLDER_REJECT =
  "Drop a single file to attach it. To start from a folder, use Open a folder below.";

export const HOME_IDEA_DROP_AFFORDANCE = "Drop files, images, or recordings to attach";

/** The buffer as the byte-plane chips the shared caps arithmetic expects. */
function capsProjection(
  buffer: readonly HomeIdeaAttachment[],
): ComposerPendingAttachment[] {
  return buffer.map((item) => {
    if (item.kind === "web-file") {
      return {
        id: item.id,
        kind: expectedPayloadKind(item.mime),
        name: item.name,
        mime: item.mime,
        byteLength: item.byteLength,
      };
    }
    if (item.kind === "path") {
      return {
        id: item.id,
        kind: item.expected,
        name: item.name,
        mime: "application/octet-stream",
        // Size is unknown until import; the upload route decides admission.
        byteLength: 0,
      };
    }
    return {
      id: item.id,
      kind: "reject" as const,
      name: item.name,
      mime: "application/octet-stream",
      byteLength: 0,
    };
  });
}

function reject(name: string, reason: string): HomeIdeaAttachment {
  return { id: crypto.randomUUID(), kind: "reject", name, reason };
}

function intakeWebFile(
  file: File,
  taken: readonly HomeIdeaAttachment[],
): HomeIdeaAttachment {
  const name = file.name || "attachment";
  const mime = file.type || "application/octet-stream";
  if (file.size === 0) {
    return reject(name, rejectMessageForCode("unsupported_attachment"));
  }
  const refusal = byteAttachmentIntakeReject(capsProjection(taken), file.size, mime);
  if (refusal) return reject(name, refusal);
  const previewUrl =
    isAllowedImageMime(mime) && typeof URL.createObjectURL === "function"
      ? URL.createObjectURL(file)
      : undefined;
  return {
    id: crypto.randomUUID(),
    kind: "web-file",
    name,
    file,
    mime,
    byteLength: file.size,
    ...(previewUrl ? { previewUrl } : {}),
  };
}

async function intakePath(
  absolutePath: string,
  taken: readonly HomeIdeaAttachment[],
): Promise<HomeIdeaAttachment> {
  const name = basenameOfPath(absolutePath);
  let pathKind: "file" | "folder" | "missing" = "missing";
  try {
    pathKind = await importPathKind(absolutePath);
  } catch {
    pathKind = "missing";
  }
  if (pathKind === "folder") return reject(name, HOME_IDEA_FOLDER_REJECT);
  if (pathKind === "missing") return reject(name, DROP_REJECT_MISSING);
  const mime = mediaMimeFromPath(absolutePath);
  const refusal = byteAttachmentIntakeReject(capsProjection(taken), undefined, mime);
  if (refusal) return reject(name, refusal);
  return {
    id: crypto.randomUUID(),
    kind: "path",
    name,
    absolutePath,
    expected: expectedPayloadKind(mime),
  };
}

/** Refused drops remain visible as rejected attachment chips. */
export async function intakeHomeIdeaDrop(
  items: readonly DroppedItem[],
  current: readonly HomeIdeaAttachment[],
): Promise<HomeIdeaAttachment[]> {
  const added: HomeIdeaAttachment[] = [];
  for (const item of items) {
    const taken = [...current, ...added];
    added.push(
      item.source === "file"
        ? intakeWebFile(item.file, taken)
        : await intakePath(item.absolutePath, taken),
    );
  }
  return added;
}

export function revokeHomeIdeaAttachment(item: HomeIdeaAttachment): void {
  if (
    item.kind === "web-file" &&
    item.previewUrl &&
    typeof URL.revokeObjectURL === "function"
  ) {
    URL.revokeObjectURL(item.previewUrl);
  }
}

/** Rejected chips block idea submission. */
export function homeIdeaRejectsPresent(
  buffer: readonly HomeIdeaAttachment[],
): boolean {
  return buffer.some((item) => item.kind === "reject");
}

export function homeIdeaChipGlyph(item: HomeIdeaAttachment): string {
  if (item.kind === "reject") return attachmentKindGlyph("reject");
  return attachmentKindGlyph(item.kind === "web-file" ? expectedPayloadKind(item.mime) : item.expected);
}

export function homeIdeaChipDetail(item: HomeIdeaAttachment): string {
  if (item.kind === "reject") return item.reason;
  if (item.kind === "web-file") return formatAttachmentBytes(item.byteLength);
  return "";
}

/** Imports buffered attachments into the project, retaining failures as chips. */
export async function stageHomeIdeaAttachments(
  client: Pick<LycaonClient, "uploadAttachment">,
  projectId: string,
  items: readonly HomeIdeaAttachment[],
): Promise<ComposerPendingAttachment[]> {
  const staged: ComposerPendingAttachment[] = [];
  for (const item of items) {
    if (item.kind === "web-file") {
      staged.push(await fileToComposerAttachment(item.file, projectId, client));
      continue;
    }
    if (item.kind === "path") {
      try {
        const receipt = await importExternalAttachment(projectId, item.absolutePath);
        staged.push(attachmentReceiptToPending(receipt, { importedCopy: true }));
      } catch (error) {
        staged.push({
          id: crypto.randomUUID(),
          kind: "reject",
          name: item.name,
          mime: "application/octet-stream",
          byteLength: 0,
          rejectCode: attachmentRejectCode(error),
          rejectMessage: rejectMessageForCode(attachmentRejectCode(error)),
        });
      }
      continue;
    }
    // Rejected chips retain their reason through staging.
    staged.push({
      id: item.id,
      kind: "reject",
      name: item.name,
      mime: "application/octet-stream",
      byteLength: 0,
      rejectMessage: item.reason,
    });
  }
  return staged;
}

export type IdeaDelivery =
  | { mode: "sent" }
  /** The payload landed in the new chat's composer instead of sending. */
  | { mode: "staged" }
  | { mode: "failed"; reason: string };

/** Failed staging or sending retains the whole payload in the chat composer. */
export async function deliverStagedIdea(opts: {
  text: string;
  staged: readonly ComposerPendingAttachment[];
  send: (
    text: string,
    parts: PromptParts,
    attachmentLabels: readonly string[],
  ) => Promise<void>;
  stageFallback: (
    staged: ComposerPendingAttachment[],
    draft: string,
  ) => Promise<{ ok: true } | { ok: false; reason: string }>;
}): Promise<IdeaDelivery> {
  const staged = [...opts.staged];
  if (staged.every(isSendablePendingAttachment)) {
    try {
      await opts.send(
        opts.text,
        promptPartsFromPendingAttachments(staged),
        staged.map((attachment) => attachment.name),
      );
      for (const attachment of staged) revokeComposerAttachment(attachment);
      return { mode: "sent" };
    } catch {
      // The chat composer holds the payload for a retry instead.
    }
  }
  const parked = await opts.stageFallback(staged, opts.text);
  return parked.ok ? { mode: "staged" } : { mode: "failed", reason: parked.reason };
}
