import { describe, expect, it, vi } from "vitest";
import { windowMenuItems } from "./window-menu-items.ts";
describe("shared window actions", () => {
  it("omits empty focus and close menus", () => {
    const items = windowMenuItems({ views: [], testIdPrefix: "window", open: vi.fn(), focus: vi.fn(), close: vi.fn() });
    expect(items.map((item) => item.label)).toEqual(["Open in new window"]);
  });
  it("orders operations consistently and captures the exact selected window", () => {
    const focus = vi.fn();
    const close = vi.fn();
    const items = windowMenuItems({ views: [{ id: "window-a", number: 2 }, { id: "window-b", number: 5 }], testIdPrefix: "window", open: vi.fn(), focus, close });
    expect(items.map((item) => item.label)).toEqual(["Open another window", "Focus window", "Close window"]);
    items[1]?.submenu?.[1]?.onSelect?.();
    expect(focus).toHaveBeenCalledWith("window-b");
    expect(close).not.toHaveBeenCalled();
  });
});
