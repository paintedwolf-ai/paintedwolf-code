import { cleanup, render } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  EMPTY_APP_STATE_V1,
  NAV_WIDTH_DEFAULT_PX,
  NAV_WIDTH_MIN_PX,
} from "../../../shared/app-state-types.ts";
import {
  preferredNavWidthPx,
  resetLayoutStoreForTests,
  setLayoutViewportWidth,
  syncLayoutFromSnapshot,
} from "../../shell/layout-store.ts";
import {
  getAppStateSnapshot,
  resetAppStateSnapshotForTests,
} from "../../store/app-state-snapshot.ts";
import { NavResizeHandle } from "./NavResizeHandle.tsx";

function pointerEvent(type: string, clientX: number, pointerId = 1): MouseEvent {
  const event = new MouseEvent(type, {
    button: 0,
    clientX,
    bubbles: true,
    cancelable: true,
  });
  Object.defineProperty(event, "pointerId", { value: pointerId });
  return event;
}

function startResize(handle: HTMLElement): void {
  handle.setPointerCapture = vi.fn();
  handle.releasePointerCapture = vi.fn();
  handle.dispatchEvent(pointerEvent("pointerdown", 280));
}

describe("NavResizeHandle", () => {
  beforeEach(() => {
    resetAppStateSnapshotForTests({
      ...EMPTY_APP_STATE_V1,
      layout: { navWidthPx: 280 },
    });
    resetLayoutStoreForTests();
    syncLayoutFromSnapshot();
    setLayoutViewportWidth(1200);
  });

  afterEach(() => {
    cleanup();
    resetLayoutStoreForTests();
    document.documentElement.classList.remove("den-shell--nav-resizing");
  });

  it("cancels on blur and always releases the global resize lock", () => {
    const { container } = render(() => (
      <NavResizeHandle width={280} side="left" onHide={vi.fn()} />
    ));
    const handle = container.querySelector(".den-shell-nav-resize") as HTMLElement;

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointermove", 360));
    expect(document.documentElement.classList.contains("den-shell--nav-resizing")).toBe(
      true,
    );
    window.dispatchEvent(new Event("blur"));

    expect(document.documentElement.classList.contains("den-shell--nav-resizing")).toBe(
      false,
    );
    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(280);
  });

  it("cancels when pointer capture is lost", () => {
    const { container } = render(() => (
      <NavResizeHandle width={280} side="left" onHide={vi.fn()} />
    ));
    const handle = container.querySelector(".den-shell-nav-resize") as HTMLElement;

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointermove", 340));
    handle.dispatchEvent(new Event("lostpointercapture", { bubbles: true }));

    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(280);
  });

  it("commits a completed move but not a no-op click", () => {
    const { container } = render(() => (
      <NavResizeHandle width={280} side="left" onHide={vi.fn()} />
    ));
    const handle = container.querySelector(".den-shell-nav-resize") as HTMLElement;

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointerup", 280));
    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(280);

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointermove", 344));
    window.dispatchEvent(pointerEvent("pointerup", 344));
    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(344);
  });

  it("resets to the default width on double-click", () => {
    const { container } = render(() => (
      <NavResizeHandle width={280} side="left" onHide={vi.fn()} />
    ));
    const handle = container.querySelector(".den-shell-nav-resize") as HTMLElement;

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointermove", 344));
    window.dispatchEvent(pointerEvent("pointerup", 344));
    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(344);

    handle.dispatchEvent(pointerEvent("dblclick", 344));

    expect(getAppStateSnapshot().layout?.navWidthPx).toBeUndefined();
    expect(preferredNavWidthPx()).toBe(NAV_WIDTH_DEFAULT_PX);
  });

  it("commits the final pointer position and ignores unrelated pointers", () => {
    const { container } = render(() => (
      <NavResizeHandle width={280} side="left" onHide={vi.fn()} />
    ));
    const handle = container.querySelector(".den-shell-nav-resize") as HTMLElement;

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointermove", 500, 2));
    window.dispatchEvent(pointerEvent("pointerup", 500, 2));
    window.dispatchEvent(pointerEvent("pointermove", 344));
    window.dispatchEvent(pointerEvent("pointerup", 280));

    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(280);
  });

  it("hides the rail when released past half its minimum and keeps its width", () => {
    const onHide = vi.fn();
    const { container } = render(() => (
      <NavResizeHandle width={280} side="left" onHide={onHide} />
    ));
    const handle = container.querySelector(".den-shell-nav-resize") as HTMLElement;

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointerup", NAV_WIDTH_MIN_PX / 2 - 1));

    expect(onHide).toHaveBeenCalledOnce();
    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(280);
  });

  it("stops at the minimum width when released above half of it", () => {
    const onHide = vi.fn();
    const { container } = render(() => (
      <NavResizeHandle width={280} side="left" onHide={onHide} />
    ));
    const handle = container.querySelector(".den-shell-nav-resize") as HTMLElement;

    startResize(handle);
    window.dispatchEvent(pointerEvent("pointerup", NAV_WIDTH_MIN_PX / 2 + 1));

    expect(onHide).not.toHaveBeenCalled();
    expect(getAppStateSnapshot().layout?.navWidthPx).toBe(NAV_WIDTH_MIN_PX);
  });
});
