// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  attachmentChipDetail,
  attachmentChipLabel,
  attachmentPreviewSnippet,
  attachmentRejectCode,
  attachmentReceiptToPending,
  attachmentTurnBytes,
  byteAttachmentIntakeReject,
  expectedPayloadKind,
  fileToComposerAttachment,
  formatAttachmentBytes,
  isAllowedImageMime,
  isSendablePendingAttachment,
  mediaMimeFromPath,
  rejectMessageForCode,
  textToComposerAttachment,
  wouldExceedTurnBytes,
  type ComposerPendingAttachment,
} from "./composer-attachments.ts";
import { setPreflightReport } from "../../platform/persistence/preflight-report.ts";
import type { AttachmentUploadResponse } from "../../api/types.ts";

const TEST_CAPS = {
  auto_attach_paste_bytes: 16 * 1024,
  max_inline_text_bytes: 64 * 1024,
  max_attachments: 8,
  max_references: 64,
  max_images: 4,
  max_upload_bytes: 16 * 1024 * 1024,
  max_image_bytes: 4 * 1024 * 1024,
  max_body_bytes: 16 * 1024 * 1024,
  max_turn_bytes: 32 * 1024 * 1024,
  max_body_preview_bytes: 16 * 1024,
  max_large_text_preview_bytes: 8 * 1024,
  max_turn_preview_bytes: 32 * 1024,
  max_document_bytes: 16 * 1024 * 1024,
  image_mime_types: ["image/png", "image/jpeg", "image/gif", "image/webp"],
  max_video_bytes: 128 * 1024 * 1024,
  video_mime_types: ["video/mp4", "video/quicktime", "video/webm"],
  text_mime_types: ["text/plain", "text/csv", "application/json"],
  text_extensions: [".go", ".txt", ".csv", ".svg"],
  text_basenames: ["dockerfile", "makefile"],
};

/** A host that accepts uploads and answers with the receipt it chose. */
function hostAccepting(receipt: Partial<AttachmentUploadResponse>) {
  return {
    uploadAttachment: vi.fn(async (_p: string, filename: string): Promise<AttachmentUploadResponse> => ({
      blob_id: "a".repeat(64),
      filename,
      mime: "text/plain",
      kind: "text",
      bytes: 12,
      ...receipt,
    })),
  };
}

/** A host that refuses, the way the upload route refuses a sniffed reject. */
function hostRejecting(code: string) {
  return {
    uploadAttachment: vi.fn(async (): Promise<AttachmentUploadResponse> => {
      throw Object.assign(new Error(code), { code });
    }),
  };
}

beforeEach(() => {
  setPreflightReport({ overall: "ok", probes: [], attachment_capabilities: TEST_CAPS });
});

describe("composer-attachments", () => {
  it("allows raster mimes and rejects svg as image mime", () => {
    expect(isAllowedImageMime("image/png")).toBe(true);
    expect(isAllowedImageMime("image/svg+xml")).toBe(false);
  });

  // The chip uses the host receipt.
  it("takes kind, name and mime from the host receipt", async () => {
    const client = hostAccepting({
      filename: "doc.pdf",
      mime: "application/pdf",
      kind: "document",
      bytes: 2048,
    });
    const att = await fileToComposerAttachment(
      new File([new TextEncoder().encode("%PDF-1.4 fake")], "doc.pdf", { type: "application/pdf" }),
      "project-1",
      client,
    );
    expect(client.uploadAttachment).toHaveBeenCalledOnce();
    expect(att.kind).toBe("document");
    if (att.kind !== "document") return;
    expect(att.blobId).toHaveLength(64);
    expect(att.byteLength).toBe(2048);
  });

  it("carries a stable blob ID for a text upload", async () => {
    const att = await fileToComposerAttachment(
      new File([new TextEncoder().encode("a,b\n1,2\n")], "rows.csv", { type: "text/csv" }),
      "project-1",
      hostAccepting({ filename: "rows.csv", mime: "text/csv", kind: "text", bytes: 8 }),
    );
    expect(att.kind).toBe("text");
    if (att.kind !== "text") return;
    expect(att.blobId).toHaveLength(64);
  });

  it("marks an external upload as an imported copy rather than a path reference", () => {
    const att = attachmentReceiptToPending(
      {
        blob_id: "b".repeat(64),
        filename: "outside.txt",
        mime: "text/plain",
        kind: "text",
        bytes: 8,
      },
      { importedCopy: true },
    );
    expect(att.kind).toBe("text");
    if (att.kind !== "text") return;
    expect(att.importedCopy).toBe(true);
    expect(attachmentChipDetail(att)).toContain("Imported copy");
  });

  it("maps a host rejection onto its structured code", async () => {
    const att = await fileToComposerAttachment(
      new File([new Uint8Array([0, 1, 255])], "evil.txt", { type: "text/plain" }),
      "project-1",
      hostRejecting("unsupported_attachment"),
    );
    expect(att.kind).toBe("reject");
    if (att.kind !== "reject") return;
    expect(att.rejectCode).toBe("unsupported_attachment");
  });

  it("refuses a file past the upload bound without calling the host", async () => {
    const client = hostAccepting({});
    const big = new File([new Uint8Array(1)], "big.bin", { type: "application/octet-stream" });
    Object.defineProperty(big, "size", { value: TEST_CAPS.max_upload_bytes + 1 });
    const att = await fileToComposerAttachment(big, "project-1", client);
    expect(client.uploadAttachment).not.toHaveBeenCalled();
    expect(att.kind).toBe("reject");
    if (att.kind !== "reject") return;
    expect(att.rejectCode).toBe("attachment_too_large");
  });

  // A composed selection stages like a dropped file, keeping its text for the chip.
  it("stages composer text through the same upload path", async () => {
    const client = hostAccepting({ filename: "main.go.txt", kind: "text", bytes: 13 });
    const att = await textToComposerAttachment("package main\n", "main.go.txt", "project-1", client);
    expect(client.uploadAttachment).toHaveBeenCalledOnce();
    expect(att.kind).toBe("text");
    if (att.kind !== "text") return;
    expect(att.blobId).toHaveLength(64);
    expect(att.preview).toBe("package main\n");
  });

  it("preflights per-turn decoded byte sum", () => {
    const a: ComposerPendingAttachment = {
      id: "1",
      kind: "text",
      name: "a.txt",
      mime: "text/plain",
      blobId: "a".repeat(64),
      byteLength: 20 * 1024 * 1024,
    };
    const b: ComposerPendingAttachment = {
      id: "2",
      kind: "text",
      name: "b.txt",
      mime: "text/plain",
      blobId: "b".repeat(64),
      byteLength: 10 * 1024 * 1024,
    };
    expect(attachmentTurnBytes([a, b])).toBe(30 * 1024 * 1024);
    expect(wouldExceedTurnBytes([a, b], 3 * 1024 * 1024)).toBe(true);
    expect(wouldExceedTurnBytes([a], 10 * 1024 * 1024)).toBe(false);
  });

  it("folds a selection range into the one-line chip label", () => {
    const ranged: ComposerPendingAttachment = {
      id: "1",
      kind: "path-file",
      name: "main.go",
      projectId: "p",
      rootId: "r",
      path: "cmd/server/main.go",
      startLine: 12,
      endLine: 48,
    };
    expect(attachmentChipLabel(ranged)).toBe("main.go:12–48");
    expect(attachmentChipLabel({ ...ranged, endLine: 12 })).toBe("main.go:12");
    expect(
      attachmentChipLabel({ ...ranged, startLine: undefined, endLine: undefined }),
    ).toBe("main.go");
    // Path chips carry no detail — the glyph plus the title say the rest.
    expect(attachmentChipDetail(ranged)).toBe("");
  });

  it("keeps chip detail short and plain for byte chips", () => {
    const detail = attachmentChipDetail({
      id: "1",
      kind: "text",
      name: "x.html",
      mime: "text/html",
      preview: `<script>alert(1)</script> hello there this runs long`,
      byteLength: 64,
    });
    expect(detail).not.toContain("<");
    expect(detail.length).toBeLessThanOrEqual(24);
    expect(
      attachmentChipDetail({
        id: "2",
        kind: "image",
        name: "shot.png",
        mime: "image/png",
        byteLength: 2048,
      }),
    ).toMatch(/KB/);
  });

  it("formats sizes and builds plain text previews (never HTML)", () => {
    expect(formatAttachmentBytes(512)).toBe("512 B");
    expect(formatAttachmentBytes(2048)).toMatch(/KB/);
    const preview = attachmentPreviewSnippet({
      id: "1",
      kind: "text",
      name: "x.html",
      mime: "text/html",
      preview: `<script>alert(1)</script> hello`,
      byteLength: 32,
    });
    expect(preview).not.toContain("<");
    expect(preview).not.toContain(">");
    expect(preview.toLowerCase()).toContain("script");
    expect(rejectMessageForCode("attachment_too_large")).toMatch(/too large/i);
  });

  it("treats unstructured native failures as unavailable, not unsupported content", () => {
    expect(attachmentRejectCode(new Error("native bridge offline"))).toBe(
      "attachment_unavailable",
    );
  });
  it("stages a video with the facts the host decoded and states them on the chip", async () => {
    const host = hostAccepting({ kind: "video", mime: "video/mp4", bytes: 3 * 1024 * 1024, video: { duration_ms: 12_400, width: 1920, height: 1080 } });
    const staged = await fileToComposerAttachment(new File(["x"], "bug.mp4", { type: "video/mp4" }), "p1", host);
    expect(staged).toMatchObject({ kind: "video", video: { duration_ms: 12_400 } });
    expect(attachmentChipDetail(staged)).toBe("0:12.4 · 3.0 MB");
    expect(attachmentPreviewSnippet(staged)).toBe("Video · 0:12.4 · 3.0 MB");
    expect(attachmentTurnBytes([staged])).toBe(3 * 1024 * 1024);
    expect(isSendablePendingAttachment(staged)).toBe(true);
  });

  it("names an undecodable video's remedy", async () => {
    const staged = await fileToComposerAttachment(new File(["x"], "bug.mov", { type: "video/quicktime" }), "p1", hostRejecting("attachment_undecodable"));
    expect(staged).toMatchObject({ kind: "reject", rejectCode: "attachment_undecodable" });
    expect(attachmentChipDetail(staged)).toMatch(/MP4 \(H\.264\) or WebM/);
  });

  it("counts a video against the image slots its frame sheet takes, and bounds its bytes", () => {
    const images = Array.from({ length: 4 }, (_, i): ComposerPendingAttachment => ({
      id: String(i), kind: i === 0 ? "video" : "image", name: "x", mime: "image/png", blobId: "b", byteLength: 1,
    }));
    expect(byteAttachmentIntakeReject(images, 1, "video/mp4")).toMatch(/too large/i);
    expect(byteAttachmentIntakeReject([], TEST_CAPS.max_video_bytes + 1, "video/webm")).toMatch(/too large/i);
    expect(byteAttachmentIntakeReject([], 1024, "video/webm")).toBeNull();
  });

  it("uploads media under a root rather than referencing it", () => {
    expect(mediaMimeFromPath("/p/rec/Bug.MOV")).toBe("video/quicktime");
    expect(mediaMimeFromPath("/p/shot.png")).toBe("image/png");
    expect(mediaMimeFromPath("/p/notes.md")).toBeUndefined();
    expect(expectedPayloadKind("video/webm")).toBe("video");
  });
});
