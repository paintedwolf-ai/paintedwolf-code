import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, waitFor } from "@solidjs/testing-library";
import {
  dispatchKeyboardEvent,
  registerCommandHandler,
  resetDispatcherForTests,
  setDispatcherPlatformForTests,
} from "../../shortcuts/dispatcher.ts";
import { DenOverlay } from "./DenOverlay.tsx";

let customChrome = true;
vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => customChrome,
}));

afterEach(() => {
  cleanup();
  resetDispatcherForTests();
  customChrome = true;
});

describe("DenOverlay", () => {
  it("covers the viewport with den-overlay and mounts a chrome drag strip", () => {
    const { container } = render(() => (
      <DenOverlay data-testid="overlay">
        <div class="den-overlay__panel">panel</div>
      </DenOverlay>
    ));
    const overlay = container.querySelector(".den-overlay");
    expect(overlay).not.toBeNull();
    expect(overlay?.getAttribute("data-testid")).toBe("overlay");
    expect(container.querySelector(".den-overlay__chrome-drag")).not.toBeNull();
  });

  it("lets the drag strip swallow clicks so the overlay does not dismiss", () => {
    const onClick = vi.fn();
    const { container } = render(() => (
      <DenOverlay onClick={onClick}>
        <div class="den-overlay__panel">panel</div>
      </DenOverlay>
    ));
    const strip = container.querySelector(".den-overlay__chrome-drag") as HTMLElement;
    fireEvent.click(strip);
    expect(onClick).not.toHaveBeenCalled();
  });

  it("still dismisses when the scrim outside the drag strip is clicked", () => {
    const onClick = vi.fn();
    const { container } = render(() => (
      <DenOverlay onClick={onClick}>
        <div class="den-overlay__panel">panel</div>
      </DenOverlay>
    ));
    fireEvent.click(container.querySelector(".den-overlay") as HTMLElement);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("controls focus and app shortcuts until the fullscreen dialog closes", async () => {
    const appRoot = document.createElement("div");
    appRoot.id = "root";
    const opener = document.createElement("button");
    appRoot.append(opener);
    document.body.append(appRoot);
    opener.focus();

    setDispatcherPlatformForTests("macos");
    const search = vi.fn();
    registerCommandHandler("search.open", search);
    const view = render(() => (
      <DenOverlay>
        <section role="dialog" aria-modal="true" aria-label="Preview">
          <button type="button">Close</button>
          <button type="button">Next</button>
        </section>
      </DenOverlay>
    ));

    await waitFor(() => {
      expect(document.activeElement?.textContent).toBe("Close");
      expect(appRoot.hasAttribute("inert")).toBe(true);
    });
    expect(
      dispatchKeyboardEvent({
        key: "k",
        code: "KeyK",
        metaKey: true,
        ctrlKey: false,
        altKey: false,
        shiftKey: false,
      }).handled,
    ).toBe(false);
    expect(search).not.toHaveBeenCalled();

    view.unmount();
    expect(appRoot.hasAttribute("inert")).toBe(false);
    expect(document.activeElement).toBe(opener);
    appRoot.remove();
  });
});
