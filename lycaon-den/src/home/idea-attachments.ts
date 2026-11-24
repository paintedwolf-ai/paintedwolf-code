/** Home buffers attachments until they travel with a launch. */
import type { LaunchChip, PathFingerprint } from "./launch/home-launch-model.ts";
import { fingerprintPath, stageLaunchBytes } from "./launch/home-launches.ts";
import { basenameOfPath } from "../api/project-path.ts";
import {
  attachmentKindGlyph,
  byteAttachmentIntakeReject,
  referenceAttachmentIntakeReject,
  expectedPayloadKind,
  formatAttachmentBytes,
  isAllowedImageMime,
  mediaMimeFromPath,
  nativeProjectionMimeFromPath,
  rejectMessageForCode,
  type ComposerPendingAttachment,
} from "../chat/composer/composer-attachments.ts";
import {
  DROP_REJECT_MISSING,
  type DroppedItem,
} from "../platform/files/file-drop.ts";
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
      /** Recorded at intake so a changed file is noticed before it is sent. */
      fingerprint?: PathFingerprint;
    }
  | { id: string; kind: "reject"; name: string; reason: string };

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
    if (item.kind === "path" && !nativeProjectionMimeFromPath(item.absolutePath)) return { id: item.id, kind: "external-file", projectId: "", path: item.absolutePath, name: item.name, mediaType: mediaMimeFromPath(item.absolutePath) };
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
  const refusal = nativeProjectionMimeFromPath(absolutePath) ? byteAttachmentIntakeReject(capsProjection(taken), undefined, mime) : referenceAttachmentIntakeReject(capsProjection(taken), mime);
  if (refusal) return reject(name, refusal);
  const fingerprint = await fingerprintPath(absolutePath);
  return {
    id: crypto.randomUUID(),
    kind: "path",
    name,
    absolutePath,
    expected: expectedPayloadKind(mime),
    ...(fingerprint ? { fingerprint } : {}),
  };
}

/**
 * Refused drops remain visible as rejected attachment chips. Byte-backed
 * chips begin copying into the outbox at intake.
 */
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
  for (const item of added) {
    if (item.kind === "web-file") void stageLaunchBytes(item.id, item.file).catch(() => undefined);
  }
  return added;
}

/** The chips a launch carries for this buffer. */
export function launchChipsFromIdea(items: readonly HomeIdeaAttachment[]): LaunchChip[] {
  return items.map((item): LaunchChip => {
    if (item.kind === "web-file") {
      return { kind: "bytes", itemId: item.id, name: item.name, mime: item.mime, byteLength: item.byteLength, snapshot: "pending" };
    }
    if (item.kind === "path") {
      return {
        kind: "path", itemId: item.id, name: item.name, absolutePath: item.absolutePath, expected: item.expected,
        ...(item.fingerprint ? { fingerprint: item.fingerprint } : {}), snapshot: "ready",
      };
    }
    return { kind: "reject", itemId: item.id, name: item.name, reason: item.reason };
  });
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

export function homeIdeaChipGlyph(item: HomeIdeaAttachment): string {
  if (item.kind === "reject") return attachmentKindGlyph("reject");
  return attachmentKindGlyph(item.kind === "web-file" ? expectedPayloadKind(item.mime) : item.expected);
}

export function homeIdeaChipDetail(item: HomeIdeaAttachment): string {
  if (item.kind === "reject") return item.reason;
  if (item.kind === "web-file") return formatAttachmentBytes(item.byteLength);
  return item.absolutePath;
}
