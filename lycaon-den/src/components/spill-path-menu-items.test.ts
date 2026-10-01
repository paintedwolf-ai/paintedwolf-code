import { beforeEach, describe, expect, it, vi } from "vitest";
import { spillPathMenuItems } from "./spill-path-menu-items.ts";

const addSelectedTextToChat = vi.fn();
const copyTextToClipboard = vi.fn();

vi.mock("../chat/composer/add-to-chat.ts", () => ({
  addSelectedTextToChat: (...args: unknown[]) => addSelectedTextToChat(...args),
}));

vi.mock("../utils/clipboard.ts", () => ({
  copyTextToClipboard: (...args: unknown[]) => copyTextToClipboard(...args),
}));

describe("spillPathMenuItems", () => {
  beforeEach(() => {
    addSelectedTextToChat.mockReset();
    copyTextToClipboard.mockReset();
  });

  it("returns empty for blank spill path", () => {
    expect(spillPathMenuItems({ spillPath: "  " })).toEqual([]);
  });

  it("copies the host-data-relative path", () => {
    const items = spillPathMenuItems({ spillPath: "tool-output/a.json" });
    expect(items.map((i) => i.label)).toEqual(["Copy path"]);
    items[0]?.onSelect!();
    expect(copyTextToClipboard).toHaveBeenCalledWith("tool-output/a.json");
  });

  it("adds a text chip with path= provenance (no path-file)", () => {
    const items = spillPathMenuItems({
      spillPath: "tool-output/a.json",
      projectId: "proj-1",
      sessionId: "sess-1",
      toolCallId: "tc-1",
      chatDestination: { projectId: "proj-1", sessionId: "chat-1" },
    });
    items[1]?.onSelect!();
    expect(addSelectedTextToChat).toHaveBeenCalledWith(
      "tool-output/a.json",
      null,
      expect.objectContaining({
        text: "tool-output/a.json",
        path: "tool-output/a.json",
        projectId: "proj-1",
        sessionId: "sess-1",
        toolCallId: "tc-1",
      }),
      { destination: { projectId: "proj-1", sessionId: "chat-1" } },
    );
  });

  it("asks for a chat without a destination", () => {
    const items = spillPathMenuItems({
      spillPath: "tool-output/a.json",
      projectId: "proj-1",
      sessionId: "sess-1",
    });
    items[1]?.onSelect!();
    expect(addSelectedTextToChat).toHaveBeenLastCalledWith(
      "tool-output/a.json",
      null,
      expect.objectContaining({ sessionId: "sess-1" }),
      { destination: undefined },
    );
  });
});
