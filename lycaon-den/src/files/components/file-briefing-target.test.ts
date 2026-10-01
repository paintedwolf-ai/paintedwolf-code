import { deletedSourceFixture } from "../../test/source-reader-fixture.ts";
import { describe, expect, it } from "vitest";
import { fileVersionFromSnapshot, type FileVersionView } from "../history/file-version.ts";
import type { FileBuffer } from "../documents/files-buffer-state.ts";
import {
  briefingSelectionForBuffer,
  briefingTargetIdentity,
} from "./file-briefing-target.ts";

const buffer = (values: Partial<FileBuffer>): FileBuffer =>
  ({
    rootId: "root-1",
    path: "src/main.ts",
    kind: "text",
    loading: false,
    loadError: null,
    dirty: false,
    ...values,
  }) as FileBuffer;

const version = (values: Partial<FileVersionView> = {}): FileVersionView => ({ versionId: "version-1",
fileId: "file-1",
rootId: "root-1",
path: "src/main.ts",
op: "write",
ts: "2026-08-24T10:00:00Z",
beforeAvailability: "available",
sha256: "sha-1",
sizeBytes: 33,
availability: "available",
...values, source: { kind: "text", before: "", after: "export const current = true;" } });

describe("briefingSelectionForBuffer", () => {
  it.each(["available", "not_captured"] as const)("summarizes the retained document only when its contents are %s", (availability) => {
    const selection = briefingSelectionForBuffer(buffer({ deleted: deletedSourceFixture({
      deleted_at: "2026-09-12T10:00:00Z",
      previous: { version_id: "before-delete", state: "content", content: "retained", size_bytes: 8, availability },
    }) }), null);
    expect(selection.contextLabel).toBe("Before deletion");
    if (availability === "available") {
      expect(selection.target).toEqual({
        root_id: "root-1", path: "src/main.ts", presentation: "version", version_id: "before-delete",
      });
    } else {
      expect(selection.availability).toBe("unavailable");
      expect(selection.target).toBeNull();
    }
  });

  it("pins a dirty editor to its exact host revision", () => {
    expect(
      briefingSelectionForBuffer(
        buffer({ dirty: true, documentId: "doc-1", documentRevision: 7 }),
        null,
      ),
    ).toEqual({
      availability: "ready",
      target: {
        root_id: "root-1",
        path: "src/main.ts",
        presentation: "document",
        document_id: "doc-1",
        document_revision: 7,
      },
      displayPath: "src/main.ts",
      unavailableReason: null,
      contextLabel: null,
    });
  });

  it("does not brief an immutable comparison preview", () => {
    expect(
      briefingSelectionForBuffer(
        buffer({
          kind: "diff",
          diffPreview: {
            version: fileVersionFromSnapshot({
              rootId: "root-1", path: "src/main.ts", before: "old", after: "new",
            }),
          },
        }),
        null,
      ),
    ).toEqual({
      availability: "unavailable",
      target: null,
      displayPath: "src/main.ts",
      unavailableReason: "File summaries are unavailable for this preview.",
      contextLabel: null,
    });
  });

  it("uses the current revision for a clean source tab", () => {
    expect(briefingSelectionForBuffer(buffer({}), null)).toMatchObject({
      availability: "ready",
      target: { root_id: "root-1", path: "src/main.ts", presentation: "current" },
    });
  });

  it("keeps worker overlays on their worker checkout", () => {
    expect(
      briefingSelectionForBuffer(buffer({ jobId: "job-1" }), null),
    ).toMatchObject({
      availability: "ready",
      target: {
        root_id: "root-1",
        path: "src/main.ts",
        presentation: "current",
        worker_id: "job-1",
      },
    });
  });

  it("pins the selected historical version and labels its context", () => {
    expect(
      briefingSelectionForBuffer(
        buffer({ dirty: true, documentId: "doc-1", documentRevision: 9 }),
        version(),
        Date.parse("2026-08-24T12:00:00Z"),
      ),
    ).toEqual({
      availability: "ready",
      target: {
        root_id: "root-1",
        path: "src/main.ts",
        presentation: "version",
        version_id: "version-1",
      },
      displayPath: "src/main.ts",
      unavailableReason: null,
      contextLabel: "Version · Edited · 2h ago",
    });
  });

  it("refuses to brief a git-history state the ledger does not hold", () => {
    expect(
      briefingSelectionForBuffer(
        buffer({}),
        version({ versionId: "" }),
        0,
      ),
    ).toMatchObject({
      availability: "unavailable",
      target: null,
      unavailableReason: "Summaries cover retained versions; this state comes from git history.",
    });
  });

  it("never falls back to Current for absent or unavailable versions", () => {
    expect(
      briefingSelectionForBuffer(
        buffer({}),
        version({ availability: "absent", sha256: null }),
        0,
      ),
    ).toMatchObject({
      availability: "unavailable",
      target: null,
      unavailableReason: "This file does not exist in this version.",
    });
    expect(
      briefingSelectionForBuffer(
        buffer({}),
        version({
          availability: "unavailable",
        }),
        0,
      ),
    ).toMatchObject({
      availability: "unavailable",
      target: null,
      unavailableReason: "Content for this version is unavailable.",
    });
  });

  it("distinguishes an opening file from an empty selection", () => {
    expect(briefingSelectionForBuffer(null, null)).toMatchObject({
      availability: "empty",
      target: null,
    });
    expect(
      briefingSelectionForBuffer(buffer({ loading: true }), null),
    ).toMatchObject({
      availability: "loading",
      target: null,
      displayPath: "src/main.ts",
    });
  });

  it("uses one identity for semantically equal targets", () => {
    expect(briefingTargetIdentity({
      root_id: " root-1 ",
      path: " src/main.ts ",
      presentation: "version",
      version_id: " version-1 ",
    })).toBe(briefingTargetIdentity({
      path: "src/main.ts",
      version_id: "version-1",
      presentation: "version",
      root_id: "root-1",
    }));
  });
});
