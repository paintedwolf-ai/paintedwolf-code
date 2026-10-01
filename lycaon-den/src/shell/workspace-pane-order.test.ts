import { describe, expect, it } from "vitest";
import { workspacePaneOrder } from "./workspace-pane-order.ts";

describe("workspace pane order", () => {
  it.each([
    ["standard", "context-first", "left", "right", true, false],
    ["standard", "chat-first", "left", "left", false, true],
    ["mirrored", "context-first", "right", "left", false, false],
    ["mirrored", "chat-first", "right", "right", true, true],
  ] as const)("resolves %s / %s once for chrome and geometry", (orientation, order, navSide, conversationSide, stageOnLeft, chatBesideNav) => {
    expect(workspacePaneOrder(orientation, order)).toEqual({
      navSide, conversationSide, stageOnLeft, chatBesideNav,
    });
  });
});
