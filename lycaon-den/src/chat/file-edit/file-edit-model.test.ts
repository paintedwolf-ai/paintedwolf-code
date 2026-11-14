import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import { fileEditsFromPart, partHasFileEdits } from "./file-edit-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

function part(overrides: Partial<ToolPartView> = {}): ToolPartView {
  return {
    id: "tc1",
    toolCallId: "tc1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "command",
    kind: "command",
    status: "completed",
    ...overrides,
  };
}

describe("fileEditsFromPart", () => {
  const first = fileEditPreviewFixture({ path: "src/foo.ts", before: "const a = 1;", after: "const a = 2;" });
  const second = fileEditPreviewFixture({ path: "src/bar.ts", after: "export {};" });

  it("returns every recorded edit in recorded order", () => {
    expect(fileEditsFromPart(part({ fileEdits: [first, second] }))).toEqual([first, second]);
  });

  it("returns edits whatever the call's status", () => {
    for (const status of ["error", "running"] as const) {
      expect(fileEditsFromPart(part({ status, fileEdits: [first] }))).toEqual([first]);
    }
  });

  it("returns nothing for a call without edits and skips an edit that names no path", () => {
    const unnamed = fileEditPreviewFixture({ path: "  ", before: "", after: "" });
    expect(fileEditsFromPart(part())).toEqual([]);
    expect(fileEditsFromPart(part({ fileEdits: [] }))).toEqual([]);
    expect(fileEditsFromPart(part({ fileEdits: [unnamed, second] }))).toEqual([second]);
    expect(partHasFileEdits(part({ fileEdits: [unnamed] }))).toBe(false);
    expect(partHasFileEdits(part({ fileEdits: [second] }))).toBe(true);
  });
});
