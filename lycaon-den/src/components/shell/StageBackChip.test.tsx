import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { StageBackChip } from "./StageBackChip.tsx";

vi.mock("../../platform/runtime.ts", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../platform/runtime.ts")>()),
  usesCustomWindowChrome: () => true,
}));

vi.mock("@tauri-apps/api/window", () => ({
  getCurrentWindow: () => ({
    startDragging: vi.fn().mockResolvedValue(undefined),
  }),
}));

function pressAndMove(button: HTMLElement, dy: number) {
  button.dispatchEvent(
    new MouseEvent("pointerdown", {
      button: 0,
      clientX: 80,
      clientY: 10,
      bubbles: true,
    }),
  );
  if (dy !== 0) {
    window.dispatchEvent(
      new MouseEvent("pointermove", { clientX: 80, clientY: 10 + dy }),
    );
  }
  window.dispatchEvent(new MouseEvent("pointerup"));
}

describe("StageBackChip", () => {
  it("navigates on a tap", () => {
    const onBack = vi.fn();
    render(() => <StageBackChip back={{ label: "Back to chat", onBack }} />);
    const button = screen.getByTestId("stage-back");
    pressAndMove(button, 0);
    fireEvent.click(button);
    expect(onBack).toHaveBeenCalledOnce();
    expect(button.querySelector(".den-stage-back__mark")).toBeNull();
  });

  it("drags the window without navigating when the press moves", () => {
    const onBack = vi.fn();
    render(() => <StageBackChip back={{ label: "Back to chat", onBack }} />);
    const button = screen.getByTestId("stage-back");
    pressAndMove(button, 30);
    fireEvent.click(button);
    expect(onBack).not.toHaveBeenCalled();
  });
});
