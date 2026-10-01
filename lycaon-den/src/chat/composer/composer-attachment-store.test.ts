// @vitest-environment jsdom
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";

const importExternalAttachment = vi.fn();

vi.mock("../../platform/files/external-attachment-import.ts", () => ({
  importExternalAttachment: (...args: unknown[]) => importExternalAttachment(...args),
}));
import {
  addSelectedTextToChat,
  addTextAttachmentToChat,
  addToChat,
  composerAttachFocusPulse,
  ingestChatDrop,
  ingestDropClassification,
  registerComposerAttachmentSink,
  resetComposerAttachmentSinkForTests,
  runComposerPrefillEffect,
} from "./add-to-chat.ts";
import {
  addPendingAttachments,
  clearPendingAttachments,
  pendingAttachmentsForSession,
  resetComposerAttachmentsForTests,
} from "./composer-attachment-store.ts";
import { composerDraftForSession, setComposerDraft, resetComposerDraftsForTests } from "./composer-drafts.ts";
import { resetComposerDocumentStoreForTests } from "./composer-document-store.ts";
import {
  pendingChatDestinationRequest,
  resetChatDestinationForTests,
  settleChatDestination,
} from "./chat-destination.ts";
import {
  createNoticeStore,
  registerNoticePublisher,
} from "../../notices/notice-store.ts";
import { selectAppNotices, selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { USER_SELECTED_TEXT_PREFIX } from "./selection-provenance.ts";
import { setPreflightReport } from "../../platform/persistence/preflight-report.ts";
import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import { LycaonApiError } from "../../api/http.ts";
import type { AttachmentUploadResponse } from "../../api/types.ts";

// Accepted uploads let these fixtures exercise attachment staging.
function seedUploadHost(): void {
  setLycaonClientForTest(stubClient({
    uploadAttachment: async (_projectId: string, filename: string): Promise<AttachmentUploadResponse> => ({
      blob_id: "a".repeat(64),
      filename,
      mime: "text/plain",
      kind: "text",
      bytes: 16,
    }),
  }));
}


const BYTE_CAP = 8;
const REFERENCE_CAP = 64;

beforeEach(() => {
  resetAppStateSnapshotForTests();
  seedUploadHost();
  resetComposerAttachmentsForTests();
  resetComposerAttachmentSinkForTests();
  resetComposerDraftsForTests();
  resetComposerDocumentStoreForTests();
  resetChatDestinationForTests();
  setPreflightReport({
    overall: "ok",
    probes: [],
    attachment_capabilities: {
      auto_attach_paste_bytes: 16 * 1024,
      max_inline_text_bytes: 64 * 1024,
      max_attachments: BYTE_CAP,
      max_references: REFERENCE_CAP,
      max_images: 4,
      max_upload_bytes: 16 * 1024 * 1024,
      max_image_bytes: 4 * 1024 * 1024,
      max_body_bytes: 16 * 1024 * 1024,
      max_turn_bytes: 32 * 1024 * 1024,
      max_body_preview_bytes: 16 * 1024,
      max_large_text_preview_bytes: 8 * 1024,
      max_turn_preview_bytes: 32 * 1024,
      max_document_bytes: 16 * 1024 * 1024,
      image_mime_types: ["image/png", "image/jpeg"],
      max_video_bytes: 128 * 1024 * 1024,
      video_mime_types: ["video/mp4", "video/quicktime", "video/webm"],
      text_mime_types: ["text/plain"],
      text_extensions: [".txt"],
      text_basenames: ["makefile"],
    },
  });
  importExternalAttachment.mockReset();
});

describe("composer-attachment-store", () => {
  it("stores pending attachments per session", () => {
    const result = addPendingAttachments("s1", [{
      id: "a1",
      kind: "path-file",
      name: "main.go",
      projectId: "p1",
      rootId: "r1",
      path: "src/main.go",
    }]);
    expect(result).toEqual({ ok: true });
    expect(pendingAttachmentsForSession("s1")).toHaveLength(1);
    expect(pendingAttachmentsForSession("s2")).toHaveLength(0);
    clearPendingAttachments("s1");
    expect(pendingAttachmentsForSession("s1")).toHaveLength(0);
  });

  it("counts references against their own ceiling, not the byte budget", () => {
    // Reference attachments remain available at the byte limit.
    for (let i = 0; i < BYTE_CAP; i++) {
      const r = addPendingAttachments("s1", [{
        id: `b${i}`,
        kind: "text",
        name: `b${i}.txt`,
        mime: "text/plain",
        blobId: "a".repeat(64),
        byteLength: 1,
      }]);
      expect(r.ok).toBe(true);
    }
    const extraByte = addPendingAttachments("s1", [{
      id: "byte-overflow",
      kind: "text",
      name: "overflow.txt",
      mime: "text/plain",
      blobId: "a".repeat(64),
      byteLength: 1,
    }]);
    expect(extraByte.ok).toBe(false);

    for (let i = 0; i < REFERENCE_CAP; i++) {
      const r = addPendingAttachments("s1", [{
        id: `a${i}`,
        kind: "path-file",
        name: `f${i}.go`,
        projectId: "p1",
        rootId: "r1",
        path: `f${i}.go`,
      }]);
      expect(r.ok).toBe(true);
    }
    expect(pendingAttachmentsForSession("s1")).toHaveLength(
      BYTE_CAP + REFERENCE_CAP,
    );

    const capped = addPendingAttachments("s1", [{
      id: "ref-overflow",
      kind: "path-folder",
      name: "extra",
      projectId: "p1",
      rootId: "r1",
      path: "extra",
    }]);
    expect(capped.ok).toBe(false);
    if (capped.ok) return;
    expect(capped.reason).toMatch(/too large/i);
  });
});

describe("addTextAttachmentToChat", () => {
  it("stages a text attachment in the chosen chat and preserves its draft", async () => {
    setComposerDraft("session-other", "Please explain these findings.");
    const result = addTextAttachmentToChat("project-1", "Finding context", "security-findings.txt");
    expect(pendingChatDestinationRequest()).toMatchObject({ projectId: "project-1" });
    settleChatDestination({ projectId: "project-1", sessionId: "session-other" });
    await expect(result).resolves.toMatchObject({ ok: true });
    expect(pendingAttachmentsForSession("session-other")).toMatchObject([
      { kind: "text", name: "security-findings.txt", preview: "Finding context" },
    ]);
    expect(composerDraftForSession("session-other")).toBe("Please explain these findings.");
    expect(pendingChatDestinationRequest()).toBeNull();
  });

  it("leaves the composer untouched when choosing a chat is canceled", async () => {
    const result = addTextAttachmentToChat("project-1", "Finding context", "security-findings.txt");
    settleChatDestination(null);
    await expect(result).resolves.toEqual({ ok: false, reason: "No chat chosen." });
    expect(pendingAttachmentsForSession("session-other")).toHaveLength(0);
  });

  it("reports an upload refusal without adding a rejected attachment", async () => {
    setLycaonClientForTest(stubClient({ uploadAttachment: async () => {
      throw new LycaonApiError("Attachment too large", 413, "attachment_too_large");
    } }));
    const result = addTextAttachmentToChat("project-1", "Finding context", "security-findings.txt");
    settleChatDestination({ projectId: "project-1", sessionId: "session-other" });
    await expect(result).resolves.toMatchObject({ ok: false });
    expect(pendingAttachmentsForSession("session-other")).toHaveLength(0);
  });
});

describe("addToChat", () => {
  it("treats the visible chat as a suggestion, not an implicit global target", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "session-visible",
      blockReason: () => null,
      projectId: () => "project-1",
      projectRoots: () => [],
      reveal: () => {},
    });

    const result = addToChat({
      kind: "artifact",
      projectId: "project-1",
      artifactId: "artifact-1",
      name: "Result",
    });
    expect(pendingChatDestinationRequest()).toMatchObject({
      projectId: "project-1",
      suggested: {
        projectId: "project-1",
        sessionId: "session-visible",
      },
    });

    settleChatDestination({
      projectId: "project-1",
      sessionId: "session-other",
    });
    await expect(result).resolves.toMatchObject({ ok: true });
    expect(pendingAttachmentsForSession("session-visible")).toHaveLength(0);
    expect(pendingAttachmentsForSession("session-other")).toHaveLength(1);
  });

  it("pushes a path-file chip onto the registered session", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => null,
      projectId: () => "proj-1",
      projectRoots: () => [{ id: "root-a", path: "/proj" }],
      reveal: () => {},
    });
    const result = await addToChat(
      {
        kind: "path-file",
        projectId: "proj-1",
        rootId: "root-a",
        path: "readme.md",
        name: "readme.md",
      },
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(true);
    const chips = pendingAttachmentsForSession("sess-a");
    expect(chips).toHaveLength(1);
    expect(chips[0]?.kind).toBe("path-file");
  });

  it("reveals chat and pulses focus after a successful attach", async () => {
    const reveal = vi.fn();
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => null,
      projectId: () => "proj-1",
      projectRoots: () => [],
      reveal,
    });
    const before = composerAttachFocusPulse();
    const result = await addToChat(
      {
        kind: "path-file",
        projectId: "proj-1",
        rootId: "root-a",
        path: "a.ts",
        name: "a.ts",
      },
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(true);
    expect(reveal).toHaveBeenCalledTimes(1);
    expect(composerAttachFocusPulse()).toBe(before + 1);
  });

  it("stages attachments while the chat is still preparing", async () => {
    const reveal = vi.fn();
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => "session_preparing",
      projectId: () => "proj-1",
      projectRoots: () => [],
      reveal,
    });
    const result = await addToChat(
      {
        kind: "artifact",
        projectId: "proj-1",
        artifactId: "art-1",
        name: "shot.png",
      },
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(true);
    expect(pendingAttachmentsForSession("sess-a")).toHaveLength(1);
    expect(reveal).toHaveBeenCalledTimes(1);
  });

  it("stages dropped files while the chat is still preparing", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => "session_preparing",
      projectId: () => "proj-1",
      projectRoots: () => [],
      reveal: () => {},
    });
    const result = await ingestChatDrop({
      items: [
        {
          source: "file",
          file: new File(["hello"], "note.txt", { type: "text/plain" }),
        },
      ],
    });
    expect(result).toEqual({ attached: 1 });
    expect(pendingAttachmentsForSession("sess-a")).toHaveLength(1);
  });

  it("does not reveal when attach is refused", async () => {
    const reveal = vi.fn();
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => "offline",
      projectId: () => "proj-1",
      projectRoots: () => [],
      reveal,
    });
    const before = composerAttachFocusPulse();
    const result = await addToChat(
      {
        kind: "artifact",
        projectId: "proj-1",
        artifactId: "art-1",
        name: "shot.png",
      },
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(false);
    expect(reveal).not.toHaveBeenCalled();
    expect(composerAttachFocusPulse()).toBe(before);
  });

  it("refuses when the composer is offline", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => "offline",
      projectId: () => "proj-1",
      projectRoots: () => [],
      reveal: () => {},
    });
    const result = await addToChat(
      {
        kind: "artifact",
        projectId: "proj-1",
        artifactId: "art-1",
        name: "shot.png",
      },
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(false);
    if (result.ok) return;
    expect(result.reason).toMatch(/Connect/i);
    expect(pendingAttachmentsForSession("sess-a")).toHaveLength(0);
  });
});

describe("addSelectedTextToChat", () => {
  it("does not project-lock ungrounded text to the visible chat", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "session-visible",
      blockReason: () => null,
      projectId: () => "project-visible",
      projectRoots: () => [],
      reveal: () => {},
    });

    const result = addSelectedTextToChat("portable note");
    expect(pendingChatDestinationRequest()).toMatchObject({
      projectId: undefined,
      suggested: {
        projectId: "project-visible",
        sessionId: "session-visible",
      },
    });
    settleChatDestination({
      projectId: "project-other",
      sessionId: "session-other",
    });

    await expect(result).resolves.toMatchObject({ ok: true });
    expect(pendingAttachmentsForSession("session-visible")).toHaveLength(0);
    expect(pendingAttachmentsForSession("session-other")).toHaveLength(1);
  });

  it("pushes a text chip with provenance header and pulses focus", async () => {
    const reveal = vi.fn();
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => null,
      projectId: () => "proj-1",
      projectRoots: () => [],
      reveal,
    });
    const beforeFocus = composerAttachFocusPulse();
    const result = await addSelectedTextToChat(
      "hello from selection",
      null,
      {
        text: "hello from selection",
        sessionId: "sess-a",
        messageId: "msg-1",
      },
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(true);
    const chips = pendingAttachmentsForSession("sess-a");
    expect(chips).toHaveLength(1);
    expect(chips[0]?.kind).toBe("text");
    if (chips[0]?.kind === "text") {
      expect(chips[0].name).toBe("selection.txt");
      expect(chips[0].preview?.startsWith(USER_SELECTED_TEXT_PREFIX)).toBe(true);
      expect(chips[0].preview).toContain("session=sess-a");
      expect(chips[0].preview).toContain("message=msg-1");
      expect(chips[0].preview).toContain("\nhello from selection");
    }
    expect(reveal).toHaveBeenCalledTimes(1);
    expect(composerAttachFocusPulse()).toBe(beforeFocus + 1);
  });

  it("also attaches path-file when jailed coords are complete", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => null,
      projectId: () => "proj-1",
      projectRoots: () => [{ id: "root-a", path: "/proj" }],
      reveal: () => {},
    });
    const result = await addSelectedTextToChat(
      "snippet",
      null,
      {
        text: "snippet",
        projectId: "proj-1",
        rootId: "root-a",
        path: "src/main.go",
      },
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(true);
    const chips = pendingAttachmentsForSession("sess-a");
    expect(chips.map((c) => c.kind).sort()).toEqual(["path-file", "text"]);
    const text = chips.find((c) => c.kind === "text");
    expect(text?.kind === "text" && text.name).toBe("main.go (selection).txt");
  });

  it("refuses when the composer is offline", async () => {
    registerComposerAttachmentSink({
      sessionId: () => "sess-a",
      blockReason: () => "offline",
      projectId: () => "proj-1",
      projectRoots: () => [],
      reveal: () => {},
    });
    const result = await addSelectedTextToChat(
      "hello",
      null,
      undefined,
      { destination: { projectId: "proj-1", sessionId: "sess-a" } },
    );
    expect(result.ok).toBe(false);
    expect(pendingAttachmentsForSession("sess-a")).toHaveLength(0);
  });

  it("rejects whitespace-only selection", async () => {
    await expect(addSelectedTextToChat("   \n")).resolves.toEqual({
      ok: false,
      reason: "Nothing selected.",
    });
  });
});

describe("ingestDropClassification", () => {
  it("external file becomes a snapshot-backed byte chip", async () => {
    importExternalAttachment.mockResolvedValue({
      blob_id: "b".repeat(64),
      filename: "outside.txt",
      mime: "text/plain",
      kind: "text",
      bytes: 12,
    } satisfies AttachmentUploadResponse);

    const result = await ingestDropClassification(
      { kind: "external-file", absolutePath: "/outside/outside.txt" },
      { projectId: "proj-1", sessionId: "sess-external" },
      [],
    );

    expect(result).toEqual({ ok: true });
    expect(importExternalAttachment).toHaveBeenCalledWith("proj-1", "/outside/outside.txt");
    expect(pendingAttachmentsForSession("sess-external")).toMatchObject([
      {
        kind: "text",
        name: "outside.txt",
        blobId: "b".repeat(64),
        importedCopy: true,
      },
    ]);
  });

  it("preserves the structured reject from native import", async () => {
    importExternalAttachment.mockRejectedValue({
      code: "attachment_too_large",
      message: "file exceeds upload bound",
    });
    const result = await ingestDropClassification(
      { kind: "external-file", absolutePath: "/outside/huge.txt" },
      { projectId: "proj-1", sessionId: "sess-external" },
      [],
    );
    expect(result).toEqual({ ok: true });
    expect(pendingAttachmentsForSession("sess-external")).toMatchObject([
      { kind: "reject", rejectMessage: "Attachment is too large for one message" },
    ]);
  });

  it("web File becomes a byte chip", async () => {
    const file = new File(["hello"], "note.txt", { type: "text/plain" });
    const result = await ingestDropClassification(
      { kind: "web-file", file },
      { projectId: "proj-1", sessionId: "sess-drop" },
      [],
    );
    expect(result).toEqual({ ok: true });
    const chips = pendingAttachmentsForSession("sess-drop");
    expect(chips).toHaveLength(1);
    expect(chips[0]?.kind).toBe("text");
  });

  it("reject classification becomes a reject chip", async () => {
    const result = await ingestDropClassification(
      {
        kind: "reject",
        reason: "Drop an individual file to attach it. Folders must be inside a project folder.",
      },
      { projectId: "proj-1", sessionId: "sess-drop" },
      [],
    );
    expect(result).toEqual({ ok: true });
    const chips = pendingAttachmentsForSession("sess-drop");
    expect(chips[0]?.kind).toBe("reject");
  });
});

describe("runComposerPrefillEffect", () => {
  it("publishes a notice instead of silently doing nothing when no project is open", async () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);
    try {
      await runComposerPrefillEffect(undefined, "Review the current change.");

      const notices = selectAppNotices(store.index());
      expect(notices).toHaveLength(1);
      expect(notices[0]?.message).toBe("No project is open.");
    } finally {
      registerNoticePublisher(null);
    }
  });

  it("publishes a project-scoped notice instead of silently doing nothing when no chat is chosen", async () => {
    const store = createNoticeStore();
    registerNoticePublisher(store);
    try {
      const effect = runComposerPrefillEffect("proj-1", "Review the current change.");
      // Without a registered destination, the prefill request remains unsettled.
      settleChatDestination(null);
      await effect;

      const notices = selectAppNotices(store.index());
      expect(notices).toHaveLength(0);
      const projectGroup = selectProjectNoticeGroups(store.index()).find(
        (group) => group.projectId === "proj-1",
      );
      expect(projectGroup?.notices).toHaveLength(1);
      expect(projectGroup?.notices[0]?.message).toBe("No chat chosen.");
    } finally {
      registerNoticePublisher(null);
    }
  });
});
