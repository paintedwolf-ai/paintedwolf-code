// @vitest-environment jsdom
import { resetAppStateSnapshotForTests } from "../../store/app-state-snapshot.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  registerComposerAttachmentSink,
  resetComposerAttachmentSinkForTests,
} from "../../chat/composer/add-to-chat.ts";
import {
  clearComposerDraft,
  composerDraftForSession,
} from "../../chat/composer/composer-drafts.ts";
import {
  pendingAttachmentsForSession,
  resetComposerAttachmentsForTests,
} from "../../chat/composer/composer-attachment-store.ts";
import {
  replaceComposerDocumentDraft,
  resetComposerDocumentStoreForTests,
} from "../../chat/composer/composer-document-store.ts";
import {
  pendingChatDestinationRequest,
  settleChatDestination,
} from "../../chat/composer/chat-destination.ts";
import {
  addEditorSelectionToChat,
  editorSelectionRange,
} from "./files-selection-to-chat.ts";

import { setLycaonClientForTest } from "../../platform/connection/app-connection.ts";
import type { AttachmentUploadResponse } from "../../api/types.ts";
import { attachmentChipLabel, attachmentPreviewSnippet } from "../../chat/composer/composer-attachments.ts";

/** Stand-in host that accepts every upload — staging is what these tests drive,
 * not admission, which the host decides and its own suites cover. */
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


function makeView(doc: string, from: number, to: number): EditorView {
  const parent = document.createElement("div");
  document.body.appendChild(parent);
  return new EditorView({
    state: EditorState.create({
      doc,
      selection: { anchor: from, head: to },
    }),
    parent,
  });
}

async function addSelection(
  args: Parameters<typeof addEditorSelectionToChat>[0],
) {
  const result = addEditorSelectionToChat(args);
  expect(pendingChatDestinationRequest()).toMatchObject({
    projectId: "proj-1",
    suggested: { projectId: "proj-1", sessionId: "sess-sel" },
  });
  settleChatDestination({ projectId: "proj-1", sessionId: "sess-sel" });
  return result;
}

describe("files-selection-to-chat", () => {
  beforeEach(() => {
  resetAppStateSnapshotForTests();
  seedUploadHost();
    resetComposerAttachmentsForTests();
    resetComposerAttachmentSinkForTests();
    resetComposerDocumentStoreForTests();
    clearComposerDraft("sess-sel");
    registerComposerAttachmentSink({
      sessionId: () => "sess-sel",
      blockReason: () => null,
      projectId: () => "proj-1",
      projectRoots: () => [{ id: "root-a", path: "/proj" }],
      reveal: () => {},
    });
  });

  afterEach(() => {
    resetComposerAttachmentsForTests();
    resetComposerAttachmentSinkForTests();
    clearComposerDraft("sess-sel");
    document.body.replaceChildren();
  });

  it("reads a non-empty CM selection as an inclusive line range", () => {
    const view = makeView("a\nb\nc\nd\n", 0, 5);
    expect(editorSelectionRange(view)).toEqual({
      startLine: 1,
      endLine: 3,
      empty: false,
    });
    view.destroy();
  });

  it.each([
    { path: "src/main.ts", multiRoot: false, name: "main.ts", label: "main.ts:2–3" },
    { path: "src/main.ts", multiRoot: true, name: "@app/main.ts", label: "@app/main.ts:2–3" },
    { path: "src/café 🐺.ts", multiRoot: true, name: "@app/café 🐺.ts", label: "@app/café 🐺.ts:2–3" },
    { path: "src/report:2–3", multiRoot: false, name: "report:2–3", label: "report:2–3:2–3" },
  ])("formats the selected range once for $path (multiple roots: $multiRoot)", async ({ path, multiRoot, name, label }) => {
    const view = makeView("a\nb\nc\nd\n", 2, 5);
    const result = await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path,
      rootLabel: "app",
      multiRoot,
      view,
    });
    expect(result).toEqual({ ok: true, attachedRange: true });
    const chips = pendingAttachmentsForSession("sess-sel");
    expect(chips).toHaveLength(1);
    expect(chips[0]).toMatchObject({
      kind: "path-file",
      path,
      name,
      startLine: 2,
      endLine: 3,
    });
    expect(attachmentChipLabel(chips[0]!)).toBe(label);
    expect(attachmentPreviewSnippet(chips[0]!)).toBe(`${path}:2–3`);
    view.destroy();
  });

  it("attaches a whole-file chip when the selection is empty", async () => {
    const view = makeView("a\nb\nc\n", 2, 2);
    const result = await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path: "src/main.ts",
      rootLabel: "app",
      multiRoot: false,
      view,
    });
    expect(result).toEqual({ ok: true, attachedRange: false });
    const chips = pendingAttachmentsForSession("sess-sel");
    expect(chips[0]).toMatchObject({
      kind: "path-file",
      name: "main.ts",
      startLine: undefined,
      endLine: undefined,
    });
    view.destroy();
  });

  it("does not stack an exact duplicate range chip", async () => {
    const view = makeView("a\nb\nc\nd\n", 2, 5);
    await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path: "a.ts",
      rootLabel: "app",
      multiRoot: false,
      view,
    });
    await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path: "a.ts",
      rootLabel: "app",
      multiRoot: false,
      view,
    });
    expect(pendingAttachmentsForSession("sess-sel")).toHaveLength(1);
    const other = makeView("a\nb\nc\nd\n", 0, 1);
    await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path: "a.ts",
      rootLabel: "app",
      multiRoot: false,
      view: other,
    });
    expect(pendingAttachmentsForSession("sess-sel")).toHaveLength(2);
    view.destroy();
    other.destroy();
  });

  it("prefills a verb template only when the draft is empty", async () => {
    const view = makeView("a\nb\nc\n", 0, 3);
    await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path: "a.ts",
      rootLabel: "app",
      multiRoot: false,
      view,
      verb: "explain",
    });
    expect(composerDraftForSession("sess-sel")).toBe("Explain this.");

    await replaceComposerDocumentDraft(
      { projectId: "proj-1", sessionId: "sess-sel" },
      "keep me",
    );
    await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path: "a.ts",
      rootLabel: "app",
      multiRoot: false,
      view,
      range: { startLine: 2, endLine: 3 },
      verb: "improve",
    });
    expect(composerDraftForSession("sess-sel")).toBe("keep me");
    expect(pendingAttachmentsForSession("sess-sel")).toHaveLength(2);
    view.destroy();
  });

  it("prefills Add-a-test template on empty draft", async () => {
    const view = makeView("a\nb\n", 0, 2);
    await addSelection({
      projectId: "proj-1",
      rootId: "root-a",
      path: "a.ts",
      rootLabel: "app",
      multiRoot: false,
      view,
      verb: "addTest",
    });
    expect(composerDraftForSession("sess-sel")).toBe(
      "Add a test covering this.",
    );
    view.destroy();
  });
});
