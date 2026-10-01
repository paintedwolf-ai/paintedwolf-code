/** Projects staged composer attachments into wire prompt parts. */

import type { PromptAttachmentPart, PromptReferencePart, PromptSecretReferencePart } from "../../api/types.ts";
import { type ComposerPendingAttachment, isPayloadAttachment } from "./composer-attachments.ts";

export type PromptParts = {
  attachments: PromptAttachmentPart[];
  references: PromptReferencePart[];
  secrets: PromptSecretReferencePart[];
};

export function promptPartsFromPendingAttachments(
  pending: readonly ComposerPendingAttachment[],
): PromptParts {
  const attachments = pending.flatMap((a): PromptAttachmentPart[] => {
    if (isPayloadAttachment(a) && a.blobId) {
      return [{ blob_id: a.blobId }];
    }
    return [];
  });
  const references = pending.flatMap((a): PromptReferencePart[] => {
    if (a.kind === "path-file") {
      return [
        {
          kind: "path-file",
          project_id: a.projectId,
          root_id: a.rootId,
          path: a.path,
          ...(a.startLine != null ? { start_line: a.startLine } : {}),
          ...(a.endLine != null ? { end_line: a.endLine } : {}),
        },
      ];
    }
    if (a.kind === "path-folder") {
      return [
        {
          kind: "path-folder",
          project_id: a.projectId,
          root_id: a.rootId,
          path: a.path,
        },
      ];
    }
    if (a.kind === "artifact") {
      return [
        {
          kind: "artifact",
          project_id: a.projectId,
          artifact_id: a.artifactId,
        },
      ];
    }
    if (a.kind === "search-hit") {
      return [
        {
          kind: "search-hit",
          project_id: a.projectId,
          session_id: a.sessionId,
          source_ref: a.sourceRef,
          hit_kind: a.hitKind,
        },
      ];
    }
    return [];
  });
  const secrets = pending.flatMap((a): PromptSecretReferencePart[] =>
    a.kind === "secret" ? [{ reference: a.reference }] : [],
  );
  return { attachments, references, secrets };
}
