/** Bounded metadata for staged attachments; bodies stay in host storage. */
import { COMPOSER_DRAFTS_SESSION_CAP } from "../../../shared/app-state-types.ts";
import type { AttachmentVideoFacts } from "../../api/types.ts";
import type { ComposerPendingAttachment } from "./composer-attachments.ts";
import { isRecord } from "../../utils/type-guards.ts";

export type ComposerAttachmentDraft = {
  projectId: string;
  attachments: ComposerPendingAttachment[];
};
export type ComposerAttachmentDrafts = Record<string, ComposerAttachmentDraft>;

function text(value: unknown): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= 4096;
}
function absolutePath(value: string): boolean {
  return value.startsWith("/") || /^[a-zA-Z]:[\\/]/.test(value) || value.startsWith("\\\\");
}
function count(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function videoFacts(value: unknown): value is AttachmentVideoFacts {
  return isRecord(value) && typeof value.duration_ms === "number" && Number.isFinite(value.duration_ms) &&
    count(value.width) && count(value.height);
}

/** Project only declared metadata, even when a runtime object has extra fields. */
export function retainedComposerAttachment(value: unknown, projectId: string): ComposerPendingAttachment | undefined {
  if (!isRecord(value) || !text(value.id) || !text(value.name)) return;
  const common = { id: value.id, name: value.name };
  switch (value.kind) {
    case "image": case "text": case "document": case "video":
      if (!text(value.blobId) || !text(value.mime) || !count(value.byteLength)) return;
      return { ...common, kind: value.kind, blobId: value.blobId, mime: value.mime,
        byteLength: value.byteLength, ...(value.importedCopy === true ? { importedCopy: true } : {}),
        ...(value.kind === "video" && videoFacts(value.video) ? { video: value.video } : {}) };
    case "path-file": case "path-folder": case "external-file": case "external-folder": {
      if (value.projectId !== projectId || !text(value.path)) return;
      const external = value.kind === "external-file" || value.kind === "external-folder";
      if (external ? !absolutePath(value.path) : !text(value.rootId)) return;
      const path = { ...common, projectId, path: value.path };
      if (value.kind === "external-folder") return { ...path, kind: "external-folder" };
      if (value.kind === "path-folder") return { ...path, kind: "path-folder", rootId: value.rootId as string };
      if (value.startLine !== undefined && (!count(value.startLine) || value.startLine < 1)) return;
      if (value.endLine !== undefined && (!count(value.endLine) || value.endLine < 1 ||
        (typeof value.startLine === "number" && value.endLine < value.startLine))) return;
      return { ...path, ...(value.kind === "external-file" ? { kind: "external-file" as const } : { kind: "path-file" as const, rootId: value.rootId as string }),
        ...(text(value.mediaType) ? { mediaType: value.mediaType } : {}),
        ...(typeof value.startLine === "number" ? { startLine: value.startLine } : {}),
        ...(typeof value.endLine === "number" ? { endLine: value.endLine } : {}) };
    }
    case "artifact":
      if (value.projectId !== projectId || !text(value.artifactId)) return;
      return { ...common, kind: "artifact", projectId, artifactId: value.artifactId };
    case "search-hit":
      if (value.projectId !== projectId || !text(value.sessionId) || !text(value.sourceRef) || !text(value.hitKind)) return;
      return { ...common, kind: "search-hit", projectId, sessionId: value.sessionId,
        sourceRef: value.sourceRef, hitKind: value.hitKind };
    case "secret":
      if (value.projectId !== projectId || !text(value.reference) ||
        !/^\{\{paintedwolf-secret:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\}\}$/.test(value.reference) ||
        (value.scope !== "chat" && value.scope !== "project") || !count(value.runeLength) ||
        value.runeLength < 1 || value.runeLength > 4194304) return;
      return { ...common, kind: "secret", projectId, reference: value.reference,
        scope: value.scope, runeLength: value.runeLength,
        ...(typeof value.shape === "string" && value.shape.length <= 256 ? { shape: value.shape } : {}) };
    default: return;
  }
}

export function parseComposerAttachmentDrafts(raw: unknown): ComposerAttachmentDrafts {
  if (!isRecord(raw)) return {};
  const out: ComposerAttachmentDrafts = {};
  for (const [sessionId, value] of Object.entries(raw).slice(-COMPOSER_DRAFTS_SESSION_CAP)) {
    if (!text(sessionId) || !isRecord(value) || !text(value.projectId) || !Array.isArray(value.attachments)) continue;
    const attachments = value.attachments.slice(0, 128).flatMap((item) => {
      const attachment = retainedComposerAttachment(item, value.projectId as string);
      return attachment ? [attachment] : [];
    });
    if (attachments.length > 0) Object.defineProperty(out, sessionId, {
      value: { projectId: value.projectId, attachments }, enumerable: true, configurable: true, writable: true,
    });
  }
  return out;
}
