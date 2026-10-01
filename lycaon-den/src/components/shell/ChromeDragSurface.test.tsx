import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render } from "@solidjs/testing-library";
import { ChromeDragSurface } from "./ChromeDragSurface.tsx";

// The drag surface only exists under custom window chrome; drive that flag per test.
let customChrome = true;
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => customChrome,
}));

afterEach(() => {
  cleanup();
});

describe("ChromeDragSurface", () => {
  it("renders nothing without custom window chrome", () => {
    customChrome = false;
    const { container } = render(() => <ChromeDragSurface />);
    expect(container.firstChild).toBeNull();
  });

  it("renders an aria-hidden full-bleed drag layer under custom chrome", () => {
    customChrome = true;
    const { container } = render(() => <ChromeDragSurface />);
    const surface = container.querySelector(".den-shell-chrome-drag-surface");
    expect(surface).not.toBeNull();
    expect(surface?.getAttribute("aria-hidden")).toBe("true");
    expect(surface?.hasAttribute("data-den-chrome")).toBe(true);
  });

  it("honors a caller-supplied class", () => {
    customChrome = true;
    const { container } = render(() => <ChromeDragSurface class="custom-surface" />);
    expect(container.querySelector(".custom-surface")).not.toBeNull();
    expect(container.querySelector(".den-shell-chrome-drag-surface")).toBeNull();
  });

  it("stops click propagation so overlay parents do not dismiss", () => {
    customChrome = true;
    const onParentClick = vi.fn();
    const { container } = render(() => (
      <div onClick={onParentClick}>
        <ChromeDragSurface />
      </div>
    ));
    const surface = container.querySelector(".den-shell-chrome-drag-surface") as HTMLElement;
    fireEvent.click(surface);
    expect(onParentClick).not.toHaveBeenCalled();
  });
});
