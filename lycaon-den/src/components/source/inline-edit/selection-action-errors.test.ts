// @vitest-environment jsdom
import { EditorState } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { describe, expect, it, vi } from "vitest";
import { stubClient } from "../../../test/client-fixture.ts";
import { SELECTION_VERBS } from "../verbs/selection-verbs.ts";
import { inlineEditRunning } from "./inline-edit-controller.ts";
import { runSelectionVerbAction, type EditorActionContext } from "./run-editor-action.ts";

function context(error: unknown): EditorActionContext {
  return {
    client: stubClient({ getProjectSource: vi.fn().mockRejectedValue(error) }),
    projectId: "project", sessionId: "session", rootId: "root", path: "sample.go",
    bufferKey: "sample", rootLabel: "root", multiRoot: false,
    view: { state: EditorState.create({ doc: "func Sample() {}\n" }), dispatch: vi.fn() } as unknown as EditorView,
    hostEl: null, contentSha256: null, dirty: false,
    ensureCleanDisk: vi.fn().mockResolvedValue("proceed"),
    onHunkReview: vi.fn(), onError: vi.fn(),
  };
}

describe("selection action failures", () => {
  it.each(SELECTION_VERBS)("reports $id failures and clears progress", async (verb) => {
    const ctx = context(new Error("Source is unavailable"));
    await expect(runSelectionVerbAction(ctx, { verb })).resolves.toBeUndefined();
    expect(ctx.onError).toHaveBeenNthCalledWith(1, null);
    expect(ctx.onError).toHaveBeenLastCalledWith("Source is unavailable");
    expect(inlineEditRunning()).toBe(false);
  });

  it("reports a failed dirty-file save before starting an action", async () => {
    const ctx = context(new Error("must not run"));
    ctx.dirty = true;
    ctx.ensureCleanDisk = vi.fn().mockRejectedValue(new Error("Save failed"));
    await runSelectionVerbAction(ctx, { verb: SELECTION_VERBS[0]! });
    expect(ctx.onError).toHaveBeenCalledExactlyOnceWith("Save failed");
    expect(ctx.client.getProjectSource).not.toHaveBeenCalled();
  });

  it.each([false, true])("does not report cancellation (signal=%s)", async (withSignal) => {
    const ctx = context(new DOMException("Cancelled", "AbortError"));
    if (withSignal) ctx.abortSignal = AbortSignal.abort();
    await expect(runSelectionVerbAction(ctx, { verb: SELECTION_VERBS[0]! })).resolves.toBeUndefined();
    expect(ctx.onError).not.toHaveBeenCalledWith(expect.any(String));
    expect(inlineEditRunning()).toBe(false);
  });
});
