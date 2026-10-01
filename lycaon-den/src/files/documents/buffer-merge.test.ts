import { describe, expect, it } from "vitest";
import { mergeApplyPayload, type BufferMergeModel } from "./buffer-merge.ts";
import { fileBufferKey } from "../components/project-files-model.ts";

describe("buffer-merge", () => {
  const model: BufferMergeModel = {
    bufferKey: fileBufferKey("r1", "src/a.ts"),
    mine: "line1\nmine\n",
    theirs: "line1\ntheirs\n",
    documentRevision: 1,
    theirsSha256: "disk-sha",
  };

  it("Apply payload uses merged mine + fresh disk sha", () => {
    expect(mergeApplyPayload(model, "line1\nmixed\n")).toEqual({
      bufferKey: fileBufferKey("r1", "src/a.ts"),
      content: "line1\nmixed\n",
      documentRevision: 1,
      baseSha256: "disk-sha",
    });
  });

  it("Cancel path preserves draft bytes (model.mine untouched by helpers)", () => {
    const draft = model.mine;
    mergeApplyPayload(model, "ignored\n");
    expect(model.mine).toBe(draft);
  });
});
