/** Composer attachment intake projected by the host readiness response. */

import type { AttachmentCapabilities, AttachmentUploadResponse, AttachmentVideoFacts, ManagedSecret } from "../../api/types.ts";
import type { LycaonClient } from "../../api/client.ts";
import { preflightReport } from "../../platform/persistence/preflight-report.ts";
import { pathFileChipLabel } from "./path-file-ref.ts";

export function composerAttachmentCapabilities(): AttachmentCapabilities | undefined {
  return preflightReport()?.attachment_capabilities;
}

export const ATTACHMENT_PROVIDER_SEND_HINT =
  "Attached files are sent to the active model provider with your message.";

export type AttachmentRejectCode =
  | "unsupported_attachment"
  | "attachment_undecodable"
  | "attachment_too_large"
  | "attachment_not_found"
  | "attachment_unavailable";

function imageMIMEs(): Set<string> {
  return new Set(composerAttachmentCapabilities()?.image_mime_types ?? []);
}

function videoMIMEs(): Set<string> {
  return new Set(composerAttachmentCapabilities()?.video_mime_types ?? []);
}

/** Host-detected families whose bytes ride the prompt. */
export type PayloadKind = "image" | "text" | "document" | "video";

export type ComposerPendingKind =
  | PayloadKind
  | "reject"
  | "path-file"
  | "path-folder"
  | "artifact"
  | "search-hit"
  | "secret";

export type ComposerPendingByteAttachment = {
  id: string;
  /** Host-detected routing family. */
  kind: PayloadKind | "reject";
  name: string;
  mime: string;
  /** Materialized body ID. */
  blobId?: string;
  /** Immutable external-file snapshot. */
  importedCopy?: boolean;
  previewUrl?: string;
  /** Display-only chip snippet. */
  preview?: string;
  byteLength: number;
  /** What the host's decoder read from a video at upload. */
  video?: AttachmentVideoFacts;
  rejectCode?: AttachmentRejectCode;
  rejectMessage?: string;
};

export type ComposerPendingPathFile = {
  id: string;
  kind: "path-file";
  name: string;
  projectId: string;
  rootId: string;
  path: string;
  /** Inclusive 1-based start line when the chip is a selection range. */
  startLine?: number;
  /** Inclusive 1-based end line. */
  endLine?: number;
};

export type ComposerPendingPathFolder = {
  id: string;
  kind: "path-folder";
  name: string;
  projectId: string;
  rootId: string;
  path: string;
};

export type ComposerPendingArtifact = {
  id: string;
  kind: "artifact";
  name: string;
  projectId: string;
  artifactId: string;
  previewUrl?: string;
};

export type ComposerPendingSearchHit = {
  id: string;
  kind: "search-hit";
  name: string;
  projectId: string;
  sessionId: string;
  sourceRef: string;
  hitKind: string;
};

export type ComposerPendingSecret = {
  id: string;
  kind: "secret";
  name: string;
  projectId: string;
  reference: string;
  scope: ManagedSecret["scope"];
  shape?: string;
  runeLength: number;
};

export type ComposerPendingAttachment =
  | ComposerPendingByteAttachment
  | ComposerPendingPathFile
  | ComposerPendingPathFolder
  | ComposerPendingArtifact
  | ComposerPendingSearchHit
  | ComposerPendingSecret;

export function isAllowedImageMime(mime: string): boolean {
  const m = mime.trim().toLowerCase();
  return imageMIMEs().has(m);
}

export function isAllowedVideoMime(mime: string): boolean {
  return videoMIMEs().has(mime.trim().toLowerCase());
}

const IMAGE_EXT_TO_MIME: Readonly<Record<string, string>> = {
  ".png": "image/png",
  ".jpg": "image/jpeg",
  ".jpeg": "image/jpeg",
  ".gif": "image/gif",
  ".webp": "image/webp",
};

/** Video containers the host decodes, by extension. */
const VIDEO_EXT_TO_MIME: Readonly<Record<string, string>> = {
  ".mp4": "video/mp4",
  ".m4v": "video/mp4",
  ".mov": "video/quicktime",
  ".webm": "video/webm",
};

function extensionOf(path: string): string {
  const base = path.trim().toLowerCase().replace(/\\/g, "/");
  const name = base.slice(base.lastIndexOf("/") + 1);
  const dot = name.lastIndexOf(".");
  return dot < 0 ? "" : name.slice(dot);
}

function imageMimeFromPath(path: string): string | undefined {
  const mime = IMAGE_EXT_TO_MIME[extensionOf(path)];
  if (!mime || !isAllowedImageMime(mime)) return undefined;
  return mime;
}

function videoMimeFromPath(path: string): string | undefined {
  const mime = VIDEO_EXT_TO_MIME[extensionOf(path)];
  if (!mime || !isAllowedVideoMime(mime)) return undefined;
  return mime;
}

/** The payload family a file is expected to land in before the host detects it: an extension or picker guess. */
export function expectedPayloadKind(mime: string | undefined): "image" | "video" | "document" {
  if (mime && isAllowedImageMime(mime)) return "image";
  if (mime && isAllowedVideoMime(mime)) return "video";
  return "document";
}

/** Media the model sees as pixels, which the host uploads rather than references: images and videos. */
export function mediaMimeFromPath(path: string): string | undefined {
  return imageMimeFromPath(path) ?? videoMimeFromPath(path);
}

const REJECT_MESSAGES: Record<AttachmentRejectCode, string> = {
  attachment_too_large: "Attachment is too large for one message",
  unsupported_attachment: "This file type is not supported as an attachment",
  attachment_undecodable: "This video can't be decoded — attach an MP4 (H.264) or WebM recording",
  attachment_not_found: "This attachment is no longer staged — attach it again",
  attachment_unavailable: "Attachment storage is unavailable — try again",
};

export function rejectMessageForCode(code: AttachmentRejectCode): string {
  return REJECT_MESSAGES[code];
}

/** True for a host-detected family whose bytes ride the prompt. */
function isPayloadKind(kind: ComposerPendingKind): kind is PayloadKind {
  return kind === "image" || kind === "text" || kind === "document" || kind === "video";
}

/** True for a staged payload chip, narrowed to its byte-attachment shape. */
export function isPayloadAttachment(
  att: ComposerPendingAttachment,
): att is ComposerPendingByteAttachment & { kind: PayloadKind } {
  return isPayloadKind(att.kind);
}

/** True when the chip carries payload bytes, or was refused as a payload. */
export function isByteAttachmentKind(kind: ComposerPendingKind): boolean {
  return isPayloadKind(kind) || kind === "reject";
}

/** True when the chip reaches the model as one image: an image, or a video's frame sheet. */
export function countsAsImage(kind: ComposerPendingKind): boolean {
  return kind === "image" || kind === "video";
}

/** True when the chip is a coordinates-only reference. */
export function isReferenceKind(kind: ComposerPendingKind): boolean {
  return (
    kind === "path-file" ||
    kind === "path-folder" ||
    kind === "artifact" ||
    kind === "search-hit" ||
    kind === "secret"
  );
}

/** Decoded-byte sum for sendable payload chips. */
export function attachmentTurnBytes(atts: readonly ComposerPendingAttachment[]): number {
  let sum = 0;
  for (const a of atts) {
    if (isPayloadAttachment(a)) sum += a.byteLength;
  }
  return sum;
}

/** True when adding nextByteLength would exceed the per-turn decoded-byte cap. */
export function wouldExceedTurnBytes(
  current: readonly ComposerPendingAttachment[],
  nextByteLength: number,
): boolean {
  const max = composerAttachmentCapabilities()?.max_turn_bytes ?? 0;
  return max <= 0 || attachmentTurnBytes(current) + nextByteLength > max;
}

/** Native drops acquire size and MIME type at import; available limits apply before then. */
export function byteAttachmentIntakeReject(
  current: readonly ComposerPendingAttachment[],
  byteLength?: number,
  mime?: string,
): string | null {
  const caps = composerAttachmentCapabilities();
  if (!caps) return "Attachments are unavailable until host readiness loads.";
  if (
    current.filter((attachment) => isByteAttachmentKind(attachment.kind)).length >=
    caps.max_attachments
  ) {
    return rejectMessageForCode("attachment_too_large");
  }
  if (
    byteLength !== undefined &&
    (byteLength > caps.max_upload_bytes ||
      attachmentTurnBytes(current) + byteLength > caps.max_turn_bytes)
  ) {
    return rejectMessageForCode("attachment_too_large");
  }
  if (mime && isAllowedVideoMime(mime) && byteLength !== undefined && byteLength > caps.max_video_bytes) {
    return rejectMessageForCode("attachment_too_large");
  }
  if (
    mime &&
    (isAllowedImageMime(mime) || isAllowedVideoMime(mime)) &&
    current.filter((attachment) => countsAsImage(attachment.kind)).length >= caps.max_images
  ) {
    return rejectMessageForCode("attachment_too_large");
  }
  return null;
}

/** Human-readable size for chip metadata (binary units). */
export function formatAttachmentBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "0 B";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

export function attachmentKindGlyph(kind: ComposerPendingKind): string {
  switch (kind) {
    case "image":
      return "▣";
    case "text":
      return "≡";
    case "document":
      return "▤";
    case "video":
      return "▶";
    case "path-file":
      return "▢";
    case "path-folder":
      return "⊡";
    case "artifact":
      return "◈";
    case "search-hit":
      return "⌕";
    case "secret":
      return "●";
    case "reject":
      return "!";
  }
}

export function attachmentChipLabel(att: ComposerPendingAttachment): string {
  if (att.kind === "path-file") return pathFileChipLabel(att);
  return att.name;
}

export function attachmentChipDetail(att: ComposerPendingAttachment): string {
  switch (att.kind) {
    case "secret":
      return att.shape || `${att.runeLength} characters`;
    case "reject":
      return att.rejectMessage || rejectMessageForCode(att.rejectCode ?? "unsupported_attachment");
    case "text":
      return att.importedCopy
        ? `Imported copy · ${attachmentPreviewSnippet(att, 24)}`
        : attachmentPreviewSnippet(att, 24);
    case "image":
    case "document":
      return att.importedCopy
        ? `Imported copy · ${formatAttachmentBytes(att.byteLength)}`
        : formatAttachmentBytes(att.byteLength);
    case "video": {
      const facts = videoChipFacts(att);
      return att.importedCopy ? `Imported copy · ${facts}` : facts;
    }
    case "search-hit":
      return att.hitKind;
    default:
      return "";
  }
}

export function attachmentPreviewSnippet(att: ComposerPendingAttachment, maxChars = 48): string {
  if (att.kind === "reject") {
    return att.rejectMessage || rejectMessageForCode(att.rejectCode ?? "unsupported_attachment");
  }
  if (att.kind === "path-folder") {
    return `folder · ${att.path}`;
  }
  if (att.kind === "path-file") {
    return pathFileChipLabel({ ...att, name: att.path });
  }
  if (att.kind === "artifact") {
    return "artifact";
  }
  if (att.kind === "search-hit") {
    return att.hitKind;
  }
  if (att.kind === "secret") {
    return att.shape || `${att.runeLength} characters`;
  }
  if (att.kind === "document") {
    const type = (att.mime || "document").split("/").pop() || "document";
    return `${type.toUpperCase()} · ${formatAttachmentBytes(att.byteLength)}`;
  }
  if (att.kind === "image") {
    return formatAttachmentBytes(att.byteLength);
  }
  if (att.kind === "video") {
    return `Video · ${videoChipFacts(att)}`;
  }
  const raw = (att.preview ?? "").replace(/\s+/g, " ").trim();
  if (!raw) return formatAttachmentBytes(att.byteLength);
  // Strip angle brackets so a paste preview cannot look like injected markup.
  const plain = raw.replace(/[<>&]/g, "");
  if (plain.length <= maxChars) return plain;
  return `${plain.slice(0, Math.max(0, maxChars - 1))}…`;
}

/** A video's length and size, as a chip states them. */
function videoChipFacts(att: ComposerPendingByteAttachment): string {
  const size = formatAttachmentBytes(att.byteLength);
  const ms = att.video?.duration_ms;
  if (ms === undefined || !Number.isFinite(ms)) return size;
  const tenths = Math.round(ms / 100);
  const clock = `${Math.floor(tenths / 600)}:${String(Math.floor(tenths / 10) % 60).padStart(2, "0")}.${tenths % 10}`;
  return `${clock} · ${size}`;
}

function rejectPending(
  file: File,
  mime: string,
  byteLength: number,
  code: AttachmentRejectCode,
): ComposerPendingAttachment {
  return {
    id: crypto.randomUUID(),
    kind: "reject",
    name: file.name || "attachment",
    mime,
    byteLength,
    rejectCode: code,
    rejectMessage: rejectMessageForCode(code),
  };
}

/** Uploads a file and trusts the host-issued receipt. */
export async function fileToComposerAttachment(
  file: File,
  projectId: string,
  client: Pick<LycaonClient, "uploadAttachment">,
): Promise<ComposerPendingAttachment> {
  // Local size checks are affordances; the upload route decides admission.
  const caps = composerAttachmentCapabilities();
  if (caps && file.size > caps.max_upload_bytes) {
    return rejectPending(file, file.type || "application/octet-stream", file.size, "attachment_too_large");
  }
  if (file.size === 0) {
    return rejectPending(file, file.type || "application/octet-stream", 0, "unsupported_attachment");
  }
  try {
    const receipt = await client.uploadAttachment(projectId, file.name || "file", file.type || "", file);
    return attachmentReceiptToPending(receipt, {
      previewUrl: receipt.kind === "image" ? URL.createObjectURL(file) : undefined,
    });
  } catch (err) {
    return rejectPending(
      file,
      file.type || "application/octet-stream",
      file.size,
      attachmentRejectCode(err),
    );
  }
}

/** Project a host-issued upload receipt into the one composer attachment shape. */
export function attachmentReceiptToPending(
  receipt: AttachmentUploadResponse,
  options: { previewUrl?: string; importedCopy?: boolean } = {},
): ComposerPendingAttachment {
  return {
    id: crypto.randomUUID(),
    kind: receipt.kind,
    name: receipt.filename,
    mime: receipt.mime,
    blobId: receipt.blob_id,
    byteLength: receipt.bytes,
    ...(receipt.video ? { video: receipt.video } : {}),
    ...(options.previewUrl ? { previewUrl: options.previewUrl } : {}),
    ...(options.importedCopy ? { importedCopy: true } : {}),
  };
}

/** Upload composed text and return a blob-backed attachment. */
export async function textToComposerAttachment(
  text: string,
  filename: string,
  projectId: string,
  client: Pick<LycaonClient, "uploadAttachment">,
): Promise<ComposerPendingAttachment> {
  const body = new Blob([text], { type: "text/plain" });
  const file = new File([body], filename, { type: "text/plain" });
  const staged = await fileToComposerAttachment(file, projectId, client);
  return staged.kind === "text" ? { ...staged, preview: text } : staged;
}

/** Unrecognized upload failures map to attachment_unavailable. */
export function attachmentRejectCode(err: unknown): AttachmentRejectCode {
  const code = (err as { code?: string } | undefined)?.code;
  if (
    code === "attachment_too_large" ||
    code === "unsupported_attachment" ||
    code === "attachment_undecodable" ||
    code === "attachment_not_found" ||
    code === "attachment_unavailable"
  ) {
    return code;
  }
  return "attachment_unavailable";
}

export function revokeComposerAttachment(att: ComposerPendingAttachment): void {
  if (att.kind === "image" || att.kind === "artifact") {
    if (att.previewUrl) URL.revokeObjectURL(att.previewUrl);
  }
}

/** True when the chip contributes to a sendable prompt part (bytes or reference). */
export function isSendablePendingAttachment(att: ComposerPendingAttachment): boolean {
  if (isPayloadAttachment(att)) {
    return Boolean(att.blobId);
  }
  return (
    att.kind === "path-file" ||
    att.kind === "path-folder" ||
    att.kind === "artifact" ||
    att.kind === "search-hit" ||
    att.kind === "secret"
  );
}

/** Clipboard: any file item (images + text files when the OS provides them). */
export function filesFromClipboard(items: DataTransferItemList | null | undefined): File[] {
  if (!items) return [];
  const out: File[] = [];
  for (const item of Array.from(items)) {
    if (item.kind === "file") {
      const file = item.getAsFile();
      if (file) out.push(file);
    }
  }
  return out;
}
