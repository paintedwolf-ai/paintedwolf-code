/** Rebuild attachments from restored metadata. */

import type { MessageContentPart, Project } from "../../../api/types.ts";
import type { ComposerPendingAttachment } from "../../composer/composer-attachments.ts";
import {
  projectUserMessageParts,
  type TranscriptAttachmentChip,
} from "./user-message-parts.ts";

function newId(): string {
  return crypto.randomUUID();
}

/** The host-stamped root of a restored chip, when the project still has it. */
function chipRootId(
  chip: TranscriptAttachmentChip,
  roots: readonly { id: string }[],
): string | null {
  const stamped = chip.rootId?.trim();
  return stamped && roots.some((root) => root.id === stamped) ? stamped : null;
}

function chipToPending(
  chip: TranscriptAttachmentChip,
  args: {
    projectId: string;
    roots: readonly { id: string; path: string; is_primary?: boolean }[];
  },
): ComposerPendingAttachment | null {
  const { projectId, roots } = args;
  switch (chip.kind) {
    case "path-file": {
      const path = chip.path?.trim();
      if (!path) return null;
      const rootId = chipRootId(chip, roots);
      if (!rootId) return null;
      return {
        id: newId(),
        kind: "path-file",
        name: chip.label,
        projectId,
        rootId,
        path,
        mediaType: chip.mime,
        startLine: chip.startLine,
        endLine: chip.endLine,
      };
    }
    case "path-folder": {
      const path = chip.path?.trim();
      if (!path) return null;
      const rootId = chipRootId(chip, roots);
      if (!rootId) return null;
      return {
        id: newId(),
        kind: "path-folder",
        name: chip.label,
        projectId,
        rootId,
        path,
      };
    }
    case "external-file":
    case "external-folder": {
      const path = chip.path?.trim();
      if (!path) return null;
      return chip.kind === "external-folder" ? { id: newId(), kind: "external-folder", name: chip.label, projectId, path }
        : { id: newId(), kind: "external-file", name: chip.label, projectId, path, mediaType: chip.mime, startLine: chip.startLine, endLine: chip.endLine };
    }
    case "search-hit": {
      const sourceRef = chip.sourceRef?.trim();
      const sourceSessionId = chip.sourceSessionId?.trim();
      const hitKind = chip.hitKind?.trim();
      if (!sourceRef || !sourceSessionId || !hitKind) return null;
      return {
        id: newId(),
        kind: "search-hit",
        name: chip.label,
        projectId,
        sessionId: sourceSessionId,
        sourceRef,
        hitKind,
      };
    }
    case "image":
    case "text":
    case "document":
    case "video": {
      const blobId = chip.blobId?.trim();
      const mime = chip.mime?.trim();
      const byteLength = chip.byteLength;
      if (
        !blobId ||
        !mime ||
        byteLength == null ||
        !Number.isFinite(byteLength) ||
        byteLength <= 0
      ) {
        return null;
      }
      return {
        id: newId(),
        kind: chip.kind,
        name: chip.label,
        blobId,
        mime,
        byteLength,
      };
    }
    default:
      return null;
  }
}

/** Convert restored metadata into pending attachments. */
export function pendingAttachmentsFromRestore(args: {
  projectId: string;
  sessionId: string;
  project: Project | undefined;
  contentParts?: readonly MessageContentPart[] | null;
  artifactIds?: readonly string[] | null;
}): ComposerPendingAttachment[] {
  const projectId = args.projectId.trim();
  const sessionId = args.sessionId.trim();
  if (!projectId || !sessionId) return [];
  const roots = args.project?.roots ?? [];
  const out: ComposerPendingAttachment[] = [];

  const { chips } = projectUserMessageParts("", args.contentParts);
  const sourcedArtifacts = new Set<string>();
  const sources: ComposerPendingAttachment[] = [];
  for (const chip of chips) {
    const pending = chipToPending(chip, { projectId, roots });
    if (pending) {
      sources.push(pending);
      if (chip.artifactId) sourcedArtifacts.add(chip.artifactId);
    }
  }
  for (const id of args.artifactIds ?? []) {
    const artifactId = id.trim();
    if (!artifactId || sourcedArtifacts.has(artifactId)) continue;
    out.push({ id: newId(), kind: "artifact", name: "Image", projectId, artifactId });
  }
  out.push(...sources);
  return out;
}
