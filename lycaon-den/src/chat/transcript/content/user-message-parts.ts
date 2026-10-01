/** Project typed message parts into prose and attachment chips. */

import type { MessageContentPart } from "../../../api/types.ts";
import type { ComposerPendingKind } from "../../composer/composer-attachments.ts";
import { formatAttachmentBytes } from "../../composer/composer-attachments.ts";
import { normalizeLineRange } from "../../composer/path-file-ref.ts";

export type TranscriptAttachmentChip = {
  id: string;
  kind: Extract<
    ComposerPendingKind,
    "text" | "document" | "video" | "path-file" | "path-folder" | "search-hit"
  >;
  /** Chip label (basename or ranged basename). */
  label: string;
  /** Optional muted detail beside the label. */
  detail: string;
  /** Project-relative path for path-* chips (no line range). */
  path?: string;
  /** Attached root the path resolves under; the host stamps it at ingest. */
  rootId?: string;
  startLine?: number;
  endLine?: number;
  mime?: string;
  blobId?: string;
  byteLength?: number;
  /** Search-hit identity. */
  sourceRef?: string;
  hitKind?: string;
  sourceSessionId?: string;
};

/** Where opening a chip lands. */
type TranscriptChipTarget = {
  path: string;
  rootId?: string;
  entryKind: "file" | "folder";
  startLine?: number;
  endLine?: number;
};

/** Only path chips have file destinations; payloads and search hits have no read route. */
export function transcriptChipTarget(
  chip: TranscriptAttachmentChip,
): TranscriptChipTarget | null {
  const path = chip.path?.trim();
  if (!path) return null;
  if (chip.kind === "path-file") {
    return {
      path,
      rootId: chip.rootId,
      entryKind: "file",
      startLine: chip.startLine,
      endLine: chip.endLine,
    };
  }
  if (chip.kind === "path-folder") {
    return { path, rootId: chip.rootId, entryKind: "folder" };
  }
  return null;
}

export type UserMessageProjection = {
  prose: string;
  chips: TranscriptAttachmentChip[];
  /** Clipboard / Copy text matching what the bubble shows. */
  copyText: string;
};

function basename(path: string): string {
  const parts = path.replace(/\\/g, "/").split("/").filter(Boolean);
  return parts[parts.length - 1] || path || "file";
}

function labelForPath(path: string, startLine?: number, endLine?: number): string {
  const base = basename(path);
  const range = normalizeLineRange(startLine, endLine);
  if (!range) return base;
  if (range.startLine === range.endLine) return `${base}:${range.startLine}`;
  return `${base}:${range.startLine}–${range.endLine}`;
}

function isDocumentMime(mime: string): boolean {
  const m = mime.trim().toLowerCase();
  return (
    m === "application/pdf" ||
    m.includes("officedocument") ||
    m.includes("opendocument") ||
    m === "application/rtf" ||
    m === "text/rtf"
  );
}

function retrievalKind(
  part: MessageContentPart,
): "path-file" | "path-folder" | "search-hit" | null {
  switch (part.reference_kind) {
    case "path_file":
      return "path-file";
    case "path_folder":
      return "path-folder";
    case "search_hit":
      return "search-hit";
    default:
      return null;
  }
}

function chipFromPart(part: MessageContentPart, index: number): TranscriptAttachmentChip | null {
  const origin = part.origin;
  if (origin === "attachment") {
    const source = part.source?.trim();
    const mime = part.media_type?.trim();
    const blobId = part.blob_id?.trim();
    const byteLength = part.size_bytes;
    if (
      !source ||
      !mime ||
      !blobId ||
      byteLength == null ||
      !Number.isFinite(byteLength) ||
      byteLength <= 0
    ) {
      return null;
    }
    const kind = mime.toLowerCase().startsWith("video/") ? "video" : isDocumentMime(mime) ? "document" : "text";
    return {
      id: `part-${index}`,
      kind,
      label: source,
      detail: formatAttachmentBytes(byteLength),
      mime,
      blobId,
      byteLength,
    };
  }
  if (origin === "retrieval") {
    const kind = retrievalKind(part);
    if (!kind) return null;
    const mime = part.media_type?.trim();
    if (kind === "path-folder") {
      const source = part.path?.trim();
      if (!source) return null;
      return {
        id: `part-${index}`,
        kind: "path-folder",
        label: basename(source),
        detail: source.includes("/") ? source : "",
        path: source,
        rootId: part.root_id?.trim() || undefined,
        mime,
      };
    }
    if (kind === "search-hit") {
      const sourceRef = part.source_ref?.trim();
      const hitKind = part.hit_kind?.trim();
      if (!sourceRef || !hitKind) return null;
      return {
        id: `part-${index}`,
        kind: "search-hit",
        label: basename(sourceRef),
        detail: hitKind,
        sourceRef,
        hitKind,
        sourceSessionId: part.source_session_id?.trim() || undefined,
        mime,
      };
    }
    const source = part.path?.trim();
    if (!source) return null;
    const range = normalizeLineRange(part.start_line, part.end_line);
    return {
      id: `part-${index}`,
      kind: "path-file",
      label: labelForPath(source, range?.startLine, range?.endLine),
      detail: source.includes("/") ? source : "",
      path: source,
      rootId: part.root_id?.trim() || undefined,
      startLine: range?.startLine,
      endLine: range?.endLine,
      mime,
    };
  }
  return null;
}

/** Project mixed-origin parts for a user bubble. */
export function projectUserMessageParts(
  content: string,
  parts: readonly MessageContentPart[] | null | undefined,
): UserMessageProjection {
  if (!parts || parts.length === 0) {
    const prose = content;
    return { prose, chips: [], copyText: prose };
  }
  const proseParts: string[] = [];
  const chips: TranscriptAttachmentChip[] = [];
  parts.forEach((part, index) => {
    if (part.origin === "user" && part.authority === "user") {
      const text = part.content;
      if (text.trim()) proseParts.push(text);
      return;
    }
    const chip = chipFromPart(part, index);
    if (chip) chips.push(chip);
  });
  const prose = proseParts.join("\n\n");
  const attachLines = chips.map((c) => `Attached: ${c.label}`);
  const copyText = [prose, ...attachLines].filter((s) => s.trim()).join("\n");
  return { prose, chips, copyText };
}
