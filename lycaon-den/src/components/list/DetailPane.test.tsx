import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@solidjs/testing-library";
import { DetailPane } from "./DetailPane.tsx";

describe("DetailPane", () => {
  it("always renders a close control, even with no eyebrow or meta", () => {
    const { getByTestId } = render(() => (
      <DetailPane onClose={() => {}}>body</DetailPane>
    ));
    const close = getByTestId("detail-close");
    expect(close.getAttribute("aria-label")).toBe("Close details");
  });

  it("calls onClose when the close control is clicked", () => {
    const onClose = vi.fn();
    const { getByTestId } = render(() => (
      <DetailPane onClose={onClose}>body</DetailPane>
    ));
    fireEvent.click(getByTestId("detail-close"));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("closes on Escape from inside the pane", () => {
    const onClose = vi.fn();
    const { getByTestId } = render(() => (
      <DetailPane testId="pane" onClose={onClose}>
        <button type="button" data-testid="inner">
          inner
        </button>
      </DetailPane>
    ));
    fireEvent.keyDown(getByTestId("inner"), { key: "Escape" });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("ignores other keys", () => {
    const onClose = vi.fn();
    const { getByTestId } = render(() => (
      <DetailPane testId="pane" onClose={onClose}>
        body
      </DetailPane>
    ));
    fireEvent.keyDown(getByTestId("pane"), { key: "Enter" });
    expect(onClose).not.toHaveBeenCalled();
  });

  it("puts the body in its own scroller so nothing nests a second scroll", () => {
    const { getByTestId } = render(() => (
      <DetailPane testId="pane" onClose={() => {}}>
        body
      </DetailPane>
    ));
    const body = getByTestId("pane").querySelector(".den-detail-pane__body");
    expect(body?.getAttribute("data-den-scrollport")).toBe("y");
    expect(body?.textContent).toBe("body");
  });
});
