import { fireEvent, render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { PANE_HIDDEN_PX } from "../../layout/drag-to-hide.ts";
import type { ResizeSession } from "../../layout/resize-session.ts";
import {
  CHAT_COL_MIN,
  DIVIDER_PX,
  STAGE_COL_MIN,
} from "../../shell/stage-placement.ts";
import { SplitDivider } from "./SplitDivider.tsx";

function sessionHarness(setChatWidth: (value: number) => void) {
  const preview = vi.fn((value: number) => setChatWidth(value));
  const commit = vi.fn();
  const cancel = vi.fn();
  const session: ResizeSession = { preview, commit, cancel };
  return { session, preview, commit, cancel, onBegin: vi.fn(() => session) };
}

function pointerEvent(type: string, clientX: number): MouseEvent {
  const event = new MouseEvent(type, {
    button: 0,
    clientX,
    bubbles: true,
    cancelable: true,
  });
  Object.defineProperty(event, "pointerId", { value: 1 });
  return event;
}

function mockHostBox(separator: HTMLElement): void {
  vi.spyOn(separator.parentElement!, "getBoundingClientRect").mockReturnValue({
    left: 0, right: 1200, width: 1200,
  } as DOMRect);
}

describe("SplitDivider", () => {
  it("exposes the effective constrained range and commits one keyboard transaction", () => {
    const [chatWidth, setChatWidth] = createSignal(360);
    const tx = sessionHarness(setChatWidth);
    render(() => (
      <div style={{ width: "1400px", display: "grid" }}>
        <SplitDivider
          availableWidthPx={() => 1400}
          chatWidthPx={chatWidth}
          onBegin={tx.onBegin}
          onReset={vi.fn()}
          stageOnLeft={() => true}
        />
      </div>
    ));
    const usable = 1400 - DIVIDER_PX;
    const separator = screen.getByTestId("split-divider");
    expect(separator.getAttribute("role")).toBe("separator");
    expect(separator.getAttribute("aria-orientation")).toBe("vertical");
    expect(separator.getAttribute("aria-valuenow")).toBe(
      String(Math.round(((usable - 360) / usable) * 100)),
    );
    expect(Number(separator.getAttribute("aria-valuemin"))).toBe(
      Math.round((STAGE_COL_MIN / usable) * 100),
    );
    expect(Number(separator.getAttribute("aria-valuemax"))).toBe(
      Math.round(((usable - CHAT_COL_MIN) / usable) * 100),
    );

    // The stage sits left, so moving the divider right narrows the conversation.
    fireEvent.keyDown(separator, { key: "ArrowRight" });
    expect(tx.onBegin).toHaveBeenCalledOnce();
    expect(tx.preview).toHaveBeenCalledWith(360 - 16);
    expect(tx.commit).toHaveBeenCalledOnce();

    // Home reserves the stage minimum.
    fireEvent.keyDown(separator, { key: "Home" });
    expect(tx.preview.mock.lastCall?.[0]).toBe(usable - STAGE_COL_MIN);
  });

  it("continues a drag across sidebar collapse without moving the chat edge", async () => {
    const [hostWidth, setHostWidth] = createSignal(1120);
    const [chatWidth, setChatWidth] = createSignal(440);
    const tx = sessionHarness(setChatWidth);
    render(() => (
      <div>
        <SplitDivider availableWidthPx={() => 1400}
          chatWidthPx={chatWidth} stageOnLeft={() => true}
          onBegin={tx.onBegin} onReset={vi.fn()} />
      </div>
    ));
    const separator = screen.getByTestId("split-divider") as HTMLElement;
    vi.spyOn(separator.parentElement!, "getBoundingClientRect").mockImplementation(() => ({
      left: 1400 - hostWidth(), right: 1400, width: hostWidth(),
    }) as DOMRect);
    separator.setPointerCapture = vi.fn();
    separator.releasePointerCapture = vi.fn();
    separator.dispatchEvent(pointerEvent("pointerdown", 960));
    window.dispatchEvent(pointerEvent("pointermove", 600));
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    expect(tx.preview.mock.lastCall?.[0]).toBe(799.5);

    setHostWidth(1400);
    window.dispatchEvent(pointerEvent("pointerup", 599));
    expect(tx.preview.mock.lastCall?.[0]).toBe(800.5);
    expect(tx.commit).toHaveBeenCalledOnce();
  });

  it("does not preview a no-op pointer gesture", () => {
    const [chatWidth, setChatWidth] = createSignal(440);
    const tx = sessionHarness(setChatWidth);
    render(() => (
      <div style={{ width: "1200px" }}>
        <SplitDivider
          availableWidthPx={() => 1200}
          chatWidthPx={chatWidth}
          onBegin={tx.onBegin}
          onReset={vi.fn()}
          stageOnLeft={() => true}
        />
      </div>
    ));
    const separator = screen.getByTestId("split-divider") as HTMLElement;
    mockHostBox(separator);
    separator.setPointerCapture = vi.fn();
    separator.releasePointerCapture = vi.fn();
    separator.dispatchEvent(pointerEvent("pointerdown", 600));
    window.dispatchEvent(pointerEvent("pointerup", 600));

    expect(tx.preview).not.toHaveBeenCalled();
    expect(tx.commit).toHaveBeenCalledOnce();
  });

  it("cancels interruption paths and releases the page lock", () => {
    const [chatWidth, setChatWidth] = createSignal(440);
    const tx = sessionHarness(setChatWidth);
    render(() => (
      <div style={{ width: "1200px" }}>
        <SplitDivider
          availableWidthPx={() => 1200}
          chatWidthPx={chatWidth}
          onBegin={tx.onBegin}
          onReset={vi.fn()}
          stageOnLeft={() => true}
        />
      </div>
    ));
    const separator = screen.getByTestId("split-divider") as HTMLElement;
    mockHostBox(separator);
    separator.setPointerCapture = vi.fn();
    separator.releasePointerCapture = vi.fn();
    separator.dispatchEvent(pointerEvent("pointerdown", 600));
    window.dispatchEvent(pointerEvent("pointermove", 650));
    expect(document.documentElement.classList.contains("den-shell--split-resizing")).toBe(
      true,
    );

    window.dispatchEvent(new Event("blur"));
    expect(tx.cancel).toHaveBeenCalledOnce();
    expect(tx.commit).not.toHaveBeenCalled();
    expect(document.documentElement.classList.contains("den-shell--split-resizing")).toBe(
      false,
    );
  });

  it("hides the conversation once a drag leaves it under half its floor", () => {
    const [chatWidth, setChatWidth] = createSignal(440);
    const tx = sessionHarness(setChatWidth);
    render(() => (
      <div style={{ width: "1200px" }}>
        <SplitDivider
          availableWidthPx={() => 1200}
          chatWidthPx={chatWidth}
          onBegin={tx.onBegin}
          onReset={vi.fn()}
          stageOnLeft={() => true}
        />
      </div>
    ));
    const separator = screen.getByTestId("split-divider") as HTMLElement;
    mockHostBox(separator);
    separator.setPointerCapture = vi.fn();
    separator.releasePointerCapture = vi.fn();

    // Releasing below half the chat minimum commits a hide.
    const hideAt = 1199 - CHAT_COL_MIN / 2 + 1;
    separator.dispatchEvent(pointerEvent("pointerdown", 720));
    window.dispatchEvent(pointerEvent("pointerup", hideAt));
    expect(tx.preview.mock.lastCall?.[0]).toBe(PANE_HIDDEN_PX);
    expect(tx.commit).toHaveBeenCalledOnce();

    separator.dispatchEvent(pointerEvent("pointerdown", 720));
    window.dispatchEvent(pointerEvent("pointerup", hideAt - 4));
    expect(tx.preview.mock.lastCall?.[0]).toBe(CHAT_COL_MIN);
    expect(tx.commit).toHaveBeenCalledTimes(2);
  });
});

describe("conversation on the left", () => {
  it("resizes and hides from the physical seam, and cancels when sides change", () => {
    const [chatWidth, setChatWidth] = createSignal(440);
    const [stageOnLeft, setStageOnLeft] = createSignal(false);
    const tx = sessionHarness(setChatWidth);
    render(() => (
      <div>
        <SplitDivider availableWidthPx={() => 1400} chatWidthPx={chatWidth}
          stageOnLeft={stageOnLeft} onBegin={tx.onBegin} onReset={vi.fn()} />
      </div>
    ));
    const separator = screen.getByTestId("split-divider") as HTMLElement;
    vi.spyOn(separator.parentElement!, "getBoundingClientRect").mockReturnValue({
      left: 200, right: 1600, width: 1400,
    } as DOMRect);
    separator.setPointerCapture = vi.fn();
    separator.releasePointerCapture = vi.fn();
    fireEvent.keyDown(separator, { key: "ArrowRight" });
    expect(tx.preview.mock.lastCall?.[0]).toBe(456);
    fireEvent.keyDown(separator, { key: "ArrowLeft", shiftKey: true });
    expect(tx.preview.mock.lastCall?.[0]).toBe(392);

    separator.dispatchEvent(pointerEvent("pointerdown", 640));
    window.dispatchEvent(pointerEvent("pointerup", 320));
    expect(tx.preview.mock.lastCall?.[0]).toBe(PANE_HIDDEN_PX);

    setChatWidth(440);
    separator.dispatchEvent(pointerEvent("pointerdown", 640));
    window.dispatchEvent(pointerEvent("pointermove", 700));
    setStageOnLeft(true);
    expect(tx.cancel).toHaveBeenCalledOnce();
    expect(document.documentElement.classList.contains("den-shell--split-resizing")).toBe(false);
  });
});
