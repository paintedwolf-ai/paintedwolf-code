import { fileEditPreviewFixture } from "../../test/file-edit-fixture.ts";
import { describe, expect, it } from "vitest";
import { fileEditFromPart } from "./file-edit-model.ts";
import type { ToolPartView } from "../tool/tool-part-model.ts";

function part(overrides: Partial<ToolPartView> = {}): ToolPartView {
  return {
    id: "tc1",
    toolCallId: "tc1",
    assistantMessageId: "assistant-message",
    messageId: "m1",
    tool: "edit",
    kind: "write",
    status: "completed",
    ...overrides,
  };
}

describe("fileEditFromPart", () => {
  it("returns the snapshot when the path is set", () => {
    const edit = fileEditFromPart(
      part({
        fileEdit: fileEditPreviewFixture({
          path: "src/foo.ts",
          before: "const a = 1;",
          after: "const a = 2;",
        }),
      }),
    );
    expect(edit).toEqual(fileEditPreviewFixture({ path: "src/foo.ts", before: "const a = 1;", after: "const a = 2;" }));
  });

  it("returns the snapshot on error and running parts", () => {
    const snap = {
      path: "src/foo.ts",
      before: "const a = 1;",
      after: "const a = 2;",
    };
    expect(fileEditFromPart(part({ status: "error", fileEdit: fileEditPreviewFixture(snap) }))).toEqual(
      fileEditPreviewFixture(snap),
    );
    expect(fileEditFromPart(part({ status: "running", fileEdit: fileEditPreviewFixture(snap) }))).toEqual(
      fileEditPreviewFixture(snap),
    );
  });

  it("returns null when the path is missing", () => {
    expect(fileEditFromPart(part({ status: "running" }))).toBeNull();
    expect(fileEditFromPart(part({ fileEdit: null }))).toBeNull();
    expect(
      fileEditFromPart(part({ fileEdit: fileEditPreviewFixture({ path: "  ", before: "", after: "" }) })),
    ).toBeNull();
  });
});
