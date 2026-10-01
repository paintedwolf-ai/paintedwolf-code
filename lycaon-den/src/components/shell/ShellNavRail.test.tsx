import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { attachThemedViewportScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { ShellNavRail } from "./ShellNavRail.tsx";

const detach = vi.fn();

vi.mock("../../platform/scrolling/themed-scrollbars.ts", () => ({
  attachThemedViewportScrollbar: vi.fn(() => detach),
}));

describe("ShellNavRail", () => {
  it("hosts the rail scrollbar beside the viewport, measured by the content box", () => {
    const result = render(() => (
      <ShellNavRail>
        <p>Chats</p>
      </ShellNavRail>
    ));
    const nav = screen.getByRole("navigation", { name: "Main" });
    const frame = nav.parentElement;
    const content = screen.getByText("Chats").parentElement;

    expect(frame?.classList.contains("den-shell-nav-frame")).toBe(true);
    expect(content?.parentElement).toBe(nav);
    // The chrome lives in the frame, and the extent it measures is the content box.
    expect(attachThemedViewportScrollbar).toHaveBeenCalledWith(frame, nav, { axis: "y", extent: content });

    result.unmount();
    expect(detach).toHaveBeenCalledOnce();
  });
});
