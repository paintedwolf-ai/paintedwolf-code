import { describe, expect, it, vi } from "vitest";
import { sessionLifecycleMenuItems } from "./session-lifecycle-menu.ts";

describe("chat lifecycle menus", () => {
  it.each([false, true])("shares order and preserves callbacks when archived=%s", (archived) => {
    const onToggleArchive = vi.fn();
    const onDelete = vi.fn();
    const onExport = vi.fn();
    const items = sessionLifecycleMenuItems({
      archived, exportDisabled: false, onToggleArchive, onDelete, onExport,
      testIdPrefix: "chat-list-menu",
    });
    expect(items.map((item) => item.label)).toEqual([
      "Export", archived ? "Unarchive chat" : "Archive chat", "Delete chat…",
    ]);
    expect(items[2]?.danger).toBe(true);
    items[0]?.submenu?.[1]?.onSelect?.();
    items[1]?.onSelect?.();
    items[2]?.onSelect?.();
    expect(onExport).toHaveBeenCalledWith("json");
    expect(onToggleArchive).toHaveBeenCalledOnce();
    expect(onDelete).toHaveBeenCalledOnce();
  });
});
