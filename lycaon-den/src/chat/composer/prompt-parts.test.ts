import { describe, expect, it } from "vitest";
import { promptPartsFromPendingAttachments } from "./prompt-parts.ts";
import type { ComposerPendingAttachment } from "./composer-attachments.ts";

describe("promptPartsFromPendingAttachments", () => {
  it("maps blob-backed bytes to handles and references to coordinates", () => {
    const pending: ComposerPendingAttachment[] = [
      {
        id: "1",
        kind: "image",
        name: "shot.png",
        mime: "image/png",
        blobId: "blob-1",
        byteLength: 10,
      },
      {
        id: "2",
        kind: "path-file",
        name: "main.go",
        projectId: "p",
        rootId: "r",
        path: "cmd/main.go",
        startLine: 3,
        endLine: 9,
      },
      {
        id: "3",
        kind: "path-folder",
        name: "pkg",
        projectId: "p",
        rootId: "r",
        path: "pkg",
      },
      {
        id: "4",
        kind: "artifact",
        name: "mock",
        projectId: "p",
        artifactId: "art-1",
      },
      {
        id: "5",
        kind: "search-hit",
        name: "hit",
        projectId: "p",
        sessionId: "s",
        sourceRef: "ref",
        hitKind: "message",
      },
      {
        id: "6",
        kind: "secret",
        name: "Deploy token",
        projectId: "p",
        reference: "{{paintedwolf-secret:6d261502-f5c6-4a79-b01b-c7409a95cd29}}",
        scope: "chat",
        runeLength: 32,
      },
    ];
    expect(promptPartsFromPendingAttachments(pending)).toEqual({
      attachments: [{ blob_id: "blob-1" }],
      references: [
        {
          kind: "path-file",
          project_id: "p",
          root_id: "r",
          path: "cmd/main.go",
          start_line: 3,
          end_line: 9,
        },
        { kind: "path-folder", project_id: "p", root_id: "r", path: "pkg" },
        { kind: "artifact", project_id: "p", artifact_id: "art-1" },
        {
          kind: "search-hit",
          project_id: "p",
          session_id: "s",
          source_ref: "ref",
          hit_kind: "message",
        },
      ],
      secrets: [{ reference: "{{paintedwolf-secret:6d261502-f5c6-4a79-b01b-c7409a95cd29}}" }],
    });
  });

  it("contributes nothing for rejects and blobless bytes", () => {
    const pending: ComposerPendingAttachment[] = [
      {
        id: "1",
        kind: "reject",
        name: "bad",
        mime: "application/octet-stream",
        byteLength: 0,
        rejectMessage: "refused",
      },
      {
        id: "2",
        kind: "text",
        name: "still-uploading.txt",
        mime: "text/plain",
        byteLength: 4,
      },
    ];
    expect(promptPartsFromPendingAttachments(pending)).toEqual({
      attachments: [],
      references: [],
      secrets: [],
    });
  });
});
