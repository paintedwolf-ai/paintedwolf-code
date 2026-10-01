// @vitest-environment jsdom
import type { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import { stubClient } from "../../../test/client-fixture.ts";
import { submitInlineEditPanel, type EditorActionContext } from "./run-editor-action.ts";
import { inlineEditError, inlineEditOpen, inlineEditRunning, openInlineEdit, resetInlineEditForTests } from "./inline-edit-controller.ts";

vi.mock("./editor-action-client.ts", () => ({
  editorActionOutput: () => "edits",
  runEditorAction: vi.fn().mockResolvedValue({ actionMessageId: "action", actionOrd: 2 }),
}));

afterEach(resetInlineEditForTests);

describe("inline action completion", () => {
  it.each([
    { kind: "inline" as const },
    { kind: "rename" as const, symbolName: "before" },
  ].flatMap((mode) => [true, false].map((changed) => ({ mode, changed }))))("settles $mode.kind with changed=$changed", async ({ mode, changed }) => {
    openInlineEdit({
      projectId: "project", rootId: "root", path: "a.txt", bufferKey: "a",
      scope: { kind: "selection", startLine: 1, endLine: 1 },
      mode, anchor: { top: 0, left: 0, width: 300 },
    });
    const getProjectSource = vi.fn()
      .mockResolvedValueOnce({ content: "before", sha256: "before-sha", encoding: "utf-8" })
      .mockResolvedValueOnce({ content: changed ? "after" : "before", sha256: changed ? "after-sha" : "before-sha", encoding: "utf-8" });
    const onHunkReview = vi.fn();
    const ctx: EditorActionContext = {
      client: stubClient({ getProjectSource }), projectId: "project", sessionId: "session",
      rootId: "root", path: "a.txt", bufferKey: "a", rootLabel: "root", multiRoot: false,
      view: {} as EditorView, hostEl: null, contentSha256: null, dirty: false,
      ensureCleanDisk: async () => "proceed", onHunkReview, onError: vi.fn(),
    };
    await submitInlineEditPanel(ctx, "after");
    if (changed) {
      expect(onHunkReview).toHaveBeenCalledOnce();
      expect(inlineEditOpen()).toBeNull();
    } else {
      expect(onHunkReview).not.toHaveBeenCalled();
      expect(inlineEditOpen()?.mode).toEqual(mode);
      expect(inlineEditError()).toBe("No changes were made to this file. Review the conversation for the action’s result.");
    }
    expect(inlineEditRunning()).toBe(false);
  });
});
