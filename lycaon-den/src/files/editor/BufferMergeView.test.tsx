import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import { BufferMergeView } from "./BufferMergeView.tsx";
import type { BufferMergeModel } from "../documents/buffer-merge.ts";
import { fileBufferKey } from "../components/project-files-model.ts";

const model: BufferMergeModel = {
  bufferKey: fileBufferKey("r1", "src/a.ts"),
  mine: "mine line\n",
  theirs: "theirs line\n",
  documentRevision: 1,
    theirsSha256: "sha-disk",
};

describe("BufferMergeView", () => {
  it("Apply emits PUT payload with fresh disk sha; Cancel preserves draft", async () => {
    const onApply = vi.fn();
    const onCancel = vi.fn();
    render(() => (
      <BufferMergeView model={model} onApply={onApply} onCancel={onCancel} />
    ));
    expect(screen.getByTestId("files-buffer-merge").textContent).toContain(
      "Your unsaved edits vs. the version on disk",
    );
    fireEvent.click(screen.getByTestId("files-buffer-merge-apply"));
    expect(onApply).toHaveBeenCalledWith({
      bufferKey: fileBufferKey("r1", "src/a.ts"),
      content: "mine line\n",
      documentRevision: 1,
      baseSha256: "sha-disk",
    });
    fireEvent.click(screen.getByTestId("files-buffer-merge-cancel"));
    expect(onCancel).toHaveBeenCalledOnce();
    expect(model.mine).toBe("mine line\n");
  });

  it("shows apply note when a raced Apply reloads theirs", () => {
    render(() => (
      <BufferMergeView
        model={model}
        onApply={vi.fn()}
        onCancel={vi.fn()}
        applyNote="Disk changed again — review the updated theirs."
      />
    ));
    expect(screen.getByTestId("files-buffer-merge-note").textContent).toContain(
      "Disk changed again",
    );
  });
});
