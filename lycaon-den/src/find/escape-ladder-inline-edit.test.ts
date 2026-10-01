import { beforeEach, describe, expect, it } from "vitest";
import {
  registerEscapeLadderLayer,
  resetEscapeLadderForTests,
  tryConsumeEscapeLadder,
} from "./escape-ladder.ts";
import {
  closeInlineEdit,
  openInlineEdit,
  resetInlineEditForTests,
} from "../components/source/inline-edit/inline-edit-controller.ts";

describe("inline edit dismisses before find", () => {
  beforeEach(() => {
    resetEscapeLadderForTests();
    resetInlineEditForTests();
    openInlineEdit({
      projectId: "p",
      rootId: "r",
      path: "a.go",
      bufferKey: "r:a.go",
      scope: { startLine: 1, endLine: 2, kind: "selection" },
      mode: { kind: "inline" },
      anchor: { top: 10, left: 10, width: 300 },
    });
    registerEscapeLadderLayer("inlineEdit", {
      isOpen: () => true,
      dismiss: () => closeInlineEdit(),
    });
    registerEscapeLadderLayer("findBar", {
      isOpen: () => true,
      dismiss: () => {
        throw new Error("Find dismissed before inline edit");
      },
    });
  });

  it("consumes Escape on the open inline-edit panel first", () => {
    expect(tryConsumeEscapeLadder()).toBe(true);
  });
});
