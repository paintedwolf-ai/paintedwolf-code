// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import { mockAttachmentCapabilities } from "../../api/mocks/fixtures.ts";
import { setPreflightReport } from "../../platform/persistence/preflight-report.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { ingestDropClassification, addToChat } from "./add-to-chat.ts";
import { pendingAttachmentsForSession, resetComposerAttachmentsForTests, addPendingAttachments } from "./composer-attachment-store.ts";
import { resetComposerDocumentStoreForTests } from "./composer-document-store.ts";
import { promptPartsFromPendingAttachments } from "./prompt-parts.ts";
import { retainedComposerAttachment } from "./composer-attachment-persistence.ts";
import { attachmentChipDetail, attachmentPreviewSnippet, countsAsImage, revokeComposerAttachment } from "./composer-attachments.ts";
import { pendingAttachmentsFromRestore } from "../transcript/content/restore-attachments.ts";
import { projectUserMessageParts, transcriptChipTarget } from "../transcript/content/user-message-parts.ts";

vi.mock("../../platform/files/read-path-bytes.ts", () => ({ readPathBytes: vi.fn(), importPathKind: vi.fn() }));
import { readPathBytes } from "../../platform/files/read-path-bytes.ts";

const upload = vi.fn();
const destination = { projectId: "project", sessionId: "chat" };
const file = { id: "file", kind: "external-file" as const, name: "notes.txt", projectId: "project", path: "/outside/notes.txt", startLine: 3, endLine: 5 };

beforeEach(() => {
  resetAppStateSnapshotForTests();
  resetComposerAttachmentsForTests();
  resetComposerDocumentStoreForTests();
  upload.mockReset();
  setLycaonClientForTest(stubClient({ uploadAttachment: upload }));
  setPreflightReport({ overall: "ok", probes: [], attachment_capabilities: mockAttachmentCapabilities });
});

describe("live file references", () => {
  it("keeps a bounded thumbnail and source coordinates for an in-project image", async () => {
    const createObjectURL = vi.fn(() => "blob:thumbnail");
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL: vi.fn() });
    vi.mocked(readPathBytes).mockResolvedValue(new Uint8Array([1, 2, 3]));
    try {
      await ingestDropClassification({ kind: "path-file", projectId: "project", rootId: "root", path: "assets/shot.png", absolutePath: "/project/assets/shot.png" }, destination, ["/project"]);
      expect(readPathBytes).toHaveBeenCalledWith("/project/assets/shot.png", ["/project"], mockAttachmentCapabilities.max_image_bytes);
      expect(pendingAttachmentsForSession("chat")).toMatchObject([{ kind: "path-file", path: "assets/shot.png", rootId: "root", previewUrl: "blob:thumbnail", mediaType: "image/png" }]);
      expect(promptPartsFromPendingAttachments(pendingAttachmentsForSession("chat")).attachments).toEqual([]);
    } finally { vi.unstubAllGlobals(); }
  });

  it("keeps an image reference even when its optional local preview fails", async () => {
    vi.mocked(readPathBytes).mockRejectedValue(new Error("preview unavailable"));
    await ingestDropClassification({ kind: "path-file", projectId: "project", rootId: "root", path: "shot.png", absolutePath: "/project/shot.png" }, destination, ["/project"]);
    expect(pendingAttachmentsForSession("chat")).toMatchObject([{ kind: "path-file", path: "shot.png", mediaType: "image/png" }]);
  });

  it("keeps disk files and folders through staging, persistence, and wire projection", async () => {
    await ingestDropClassification({ kind: "external-file", absolutePath: file.path }, destination, []);
    await ingestDropClassification({ kind: "external-folder", absolutePath: "/outside/folder" }, destination, []);
    const pending = pendingAttachmentsForSession("chat");
    expect(pending.map(attachmentChipDetail)).toEqual([file.path, "/outside/folder"]);
    expect(upload).not.toHaveBeenCalled();
    const retained = pending.map(a => retainedComposerAttachment({ ...a, previewUrl: "blob:temporary" }, "project")!);
    expect(JSON.stringify(retained)).not.toContain("blob:temporary");
    expect(promptPartsFromPendingAttachments(retained)).toEqual({ attachments: [], secrets: [], references: [
      { kind: "external-file", project_id: "project", path: file.path },
      { kind: "external-folder", project_id: "project", path: "/outside/folder" },
    ] });
  });

  it("deduplicates external selections by exact project, path, and range", async () => {
    await addToChat(file, { destination });
    await addToChat({ ...file, name: "same notes" }, { destination });
    await addToChat({ ...file, startLine: 7, endLine: 9 }, { destination });
    expect(pendingAttachmentsForSession("chat")).toHaveLength(2);
    expect(promptPartsFromPendingAttachments(pendingAttachmentsForSession("chat")).references[0]).toEqual({
      kind: "external-file", project_id: "project", path: file.path, start_line: 3, end_line: 5,
    });
    expect(attachmentPreviewSnippet(file)).toBe("/outside/notes.txt:3–5");
    await expect(addToChat(file, { destination: { projectId: "other", sessionId: "other" } })).resolves.toMatchObject({ ok: false });
  });

  it("validates external persisted identity and retains image presentation hints", () => {
    const image = { ...file, path: "/outside/shot.png", mediaType: "image/png" };
    expect(countsAsImage(image)).toBe(true);
    expect(retainedComposerAttachment(image, "project")).toMatchObject({ kind: "external-file", mediaType: "image/png" });
    for (const invalid of [{ ...file, path: "relative.txt" }, { ...file, projectId: "other" }, { ...file, startLine: 0 }, { ...file, endLine: 1 }]) {
      expect(retainedComposerAttachment(invalid, "project")).toBeUndefined();
    }
    expect(retainedComposerAttachment({ ...file, path: "C:\\Users\\person\\notes.txt" }, "project")).toBeDefined();
  });

  it("enforces the reference count and shared image allowance", () => {
    setPreflightReport({ overall: "ok", probes: [], attachment_capabilities: { ...mockAttachmentCapabilities, max_references: 1, max_images: 1 } });
    expect(addPendingAttachments("chat", [{ ...file, mediaType: "image/png" }]).ok).toBe(true);
    expect(addPendingAttachments("chat", [{ ...file, id: "second" }]).ok).toBe(false);
    expect(addPendingAttachments("chat", [{ id: "upload", kind: "image", name: "shot", mime: "image/png", blobId: "blob", byteLength: 1 }]).ok).toBe(false);
  });

  it("restores the original image source without relinking its derived artifact twice", () => {
    const parts = [{ content: "source", origin: "retrieval" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      reference_kind: "external_file" as const, path: "/outside/shot.png", media_type: "image/png", artifact_id: "derived" }];
    const projected = projectUserMessageParts("", parts);
    expect(projected.chips[0]).toMatchObject({ kind: "external-file", detail: "/outside/shot.png" });
    expect(transcriptChipTarget(projected.chips[0]!)).toBeNull();
    const pending = pendingAttachmentsFromRestore({ ...destination, project: undefined, contentParts: parts, artifactIds: ["derived", "independent"] });
    expect(pending).toMatchObject([{ kind: "artifact", artifactId: "independent" }, { kind: "external-file", path: "/outside/shot.png", mediaType: "image/png" }]);
    expect(promptPartsFromPendingAttachments(pending).references).toEqual([
      { kind: "artifact", project_id: "project", artifact_id: "independent" },
      { kind: "external-file", project_id: "project", path: "/outside/shot.png" },
    ]);
  });

  it("restores uploaded image blobs once, preserving an unrelated artifact", () => {
    const parts = [{ content: "upload", origin: "attachment" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      source: "shot.png", media_type: "image/png", blob_id: "a".repeat(64), size_bytes: 10, artifact_id: "upload-derived" }];
    const pending = pendingAttachmentsFromRestore({ ...destination, project: undefined, contentParts: parts, artifactIds: ["upload-derived", "independent"] });
    expect(pending).toMatchObject([{ kind: "artifact", artifactId: "independent" }, { kind: "image", blobId: "a".repeat(64) }]);
  });

  it("restores an external folder without requiring a project root", () => {
    const parts = [{ content: "folder", origin: "retrieval" as const, authority: "none" as const, trust_tier: "untrusted" as const,
      reference_kind: "external_folder" as const, path: "/outside/folder" }];
    expect(pendingAttachmentsFromRestore({ ...destination, project: undefined, contentParts: parts })).toMatchObject([{ kind: "external-folder", path: "/outside/folder" }]);
  });

  it("releases locally measured path image previews", () => {
    const revoke = vi.fn();
    vi.stubGlobal("URL", { revokeObjectURL: revoke });
    try { revokeComposerAttachment({ ...file, previewUrl: "blob:preview" }); expect(revoke).toHaveBeenCalledWith("blob:preview"); }
    finally { vi.unstubAllGlobals(); }
  });
});
