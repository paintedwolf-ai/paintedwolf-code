import { render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { scrollportFrameParts, syncThemedScrollbar } from "../../platform/scrolling/themed-scrollbars.ts";
import { Scrollport } from "./Scrollport.tsx";

vi.mock("../../platform/scrolling/themed-scrollbars.ts", async (importOriginal) => ({
  ...await importOriginal<typeof import("../../platform/scrolling/themed-scrollbars.ts")>(),
  syncThemedScrollbar: vi.fn(),
}));

describe("Scrollport", () => {
  it("renders the frame contract, routing attributes to the box they describe", () => {
    let viewportEl: HTMLDivElement | undefined;
    let contentEl: HTMLElement | undefined;
    render(() => (
      <Scrollport
        class="probe-frame"
        data-testid="frame"
        contentAs="ul"
        contentClass="probe-list"
        content={{ "data-testid": "list" }}
        viewport={{ tabIndex: 0, "aria-label": "Rows" }}
        viewportRef={(el) => { viewportEl = el; }}
        contentRef={(el) => { contentEl = el; }}
      >
        <li>one</li>
      </Scrollport>
    ));
    const frame = screen.getByTestId("frame");
    expect(frame.className).toBe("den-scrollport probe-frame");
    const parts = scrollportFrameParts(frame);
    expect(parts?.axis).toBe("y");
    expect(parts?.viewport).toBe(viewportEl);
    expect(parts?.viewport.getAttribute("aria-label")).toBe("Rows");
    expect(parts?.viewport.tabIndex).toBe(0);
    const list = screen.getByTestId("list");
    expect(list).toBe(contentEl);
    expect(list.tagName).toBe("UL");
    expect(list.className).toBe("den-scrollport__content probe-list");
    expect(list.firstElementChild?.textContent).toBe("one");
    expect(syncThemedScrollbar).not.toHaveBeenCalled();
  });

  it("attaches in the mount frame only when eager, and marks deferred frames", () => {
    render(() => (
      <>
        <Scrollport eager axis="x" data-testid="eager">wide</Scrollport>
        <Scrollport defer axis="both" data-testid="deferred">code</Scrollport>
      </>
    ));
    const eager = screen.getByTestId("eager");
    expect(eager.getAttribute("data-den-scrollport")).toBe("x");
    expect(syncThemedScrollbar).toHaveBeenCalledTimes(1);
    expect(syncThemedScrollbar).toHaveBeenCalledWith(eager);
    const deferred = screen.getByTestId("deferred");
    expect(deferred.getAttribute("data-den-scrollport")).toBe("both");
    expect(deferred.hasAttribute("data-den-scrollport-defer")).toBe(true);
    expect(eager.hasAttribute("data-den-scrollport-defer")).toBe(false);
  });
});
