// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { stubClient } from "../../../test/client-fixture.ts";
import { composerDraftForSession, setComposerDraft } from "../../../chat/composer/composer-drafts.ts";
import { resetComposerDocumentStoreForTests } from "../../../chat/composer/composer-document-store.ts";
import { pendingAttachmentsForSession } from "../../../chat/composer/composer-attachment-store.ts";
import { resetComposerAttachmentSinkForTests } from "../../../chat/composer/add-to-chat.ts";
import { SELECTION_VERBS } from "../verbs/selection-verbs.ts";
import { inlineEditRunning } from "./inline-edit-controller.ts";
import { runSelectionVerbAction, type EditorActionContext } from "./run-editor-action.ts";

vi.mock("./editor-action-client.ts", () => ({
  editorActionOutput: () => "prose",
  runEditorAction: vi.fn().mockResolvedValue({ actionMessageId: "action", actionOrd: 1 }),
}));

beforeEach(() => {
  resetComposerDocumentStoreForTests();
  resetComposerAttachmentSinkForTests();
  setComposerDraft("session", "");
});
afterEach(() => {
  resetComposerDocumentStoreForTests();
  resetComposerAttachmentSinkForTests();
});

describe("selection prose remains an assistant response", () => {
  for (const verb of SELECTION_VERBS.filter((entry) => entry.id === "explain" || entry.id === "summarize")) {
    it.each(["", "My next question"])(`${verb.id} preserves the user draft %j`, async (draft) => {
      setComposerDraft("session", draft);
      const ctx: EditorActionContext = {
        client: stubClient({
          getProjectSource: vi.fn().mockResolvedValue({ content: "func Sample() {}\n" }),
          listSessionMessages: vi.fn().mockResolvedValue({ messages: [
            { role: "assistant", content: "## Explanation\nThis function has no side effects.", ord: 2 },
          ] }),
        }),
        projectId: "project", sessionId: "session", rootId: "root", path: "sample.go",
        bufferKey: "sample", rootLabel: "root", multiRoot: false,
        view: { state: EditorState.create({ doc: "func Sample() {}\n" }), dispatch: vi.fn() } as unknown as EditorView,
        hostEl: null, contentSha256: null, dirty: false,
        ensureCleanDisk: vi.fn().mockResolvedValue("proceed"),
        onHunkReview: vi.fn(), onError: vi.fn(), onFocusComposer: vi.fn(),
      };
      await runSelectionVerbAction(ctx, { verb });
      expect(ctx.onError).toHaveBeenCalledExactlyOnceWith(null);
      expect(composerDraftForSession("session")).toBe(draft);
      expect(pendingAttachmentsForSession("session")).toEqual([
        expect.objectContaining({ kind: "path-file", rootId: "root", path: "sample.go", startLine: 1 }),
      ]);
      expect(ctx.onHunkReview).not.toHaveBeenCalled();
      expect(inlineEditRunning()).toBe(false);
    });
  }
});
