import { render, screen } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { describe, expect, it, vi } from "vitest";
import { TablePager } from "./TablePager.tsx";

function visibleText(testId: string) {
  return screen.getByTestId(testId).textContent;
}

function reserved(testId: string) {
  const label = screen.getByTestId(testId).parentElement;
  return [...(label?.querySelectorAll(".den-stable-label-sizer") ?? [])].map((el) => el.textContent);
}

describe("TablePager", () => {
  it("keeps its controls unchanged while a page loads", () => {
    const [loading, setLoading] = createSignal(false);
    render(() => (
      <TablePager page={1} pageSize={25} total={120} loading={loading()} onPageChange={vi.fn()} />
    ));
    const prev = screen.getByTestId("table-pager-prev") as HTMLButtonElement;
    const next = screen.getByTestId("table-pager-next") as HTMLButtonElement;
    setLoading(true);
    expect(next.textContent).toBe("Next");
    expect(prev.disabled).toBe(false);
    expect(next.disabled).toBe(false);
    expect(screen.getByTestId("table-pager").getAttribute("aria-busy")).toBe("true");
  });

  it("describes the rows on screen and steps from the requested page", () => {
    const onPageChange = vi.fn();
    const [shown, setShown] = createSignal(0);
    render(() => (
      <TablePager page={1} shownPage={shown()} pageSize={25} total={120} onPageChange={onPageChange} />
    ));
    expect(visibleText("table-pager-range")).toBe("1–25 of 120");
    expect(visibleText("table-pager-page")).toBe("Page 1 of 5");
    screen.getByTestId("table-pager-next").click();
    expect(onPageChange).toHaveBeenCalledWith(2);
    setShown(1);
    expect(visibleText("table-pager-range")).toBe("26–50 of 120");
    expect(visibleText("table-pager-page")).toBe("Page 2 of 5");
  });

  it("reserves the width of the longest label for the total", () => {
    render(() => <TablePager page={0} pageSize={10} total={1200} onPageChange={vi.fn()} />);
    expect(reserved("table-pager-range")).toEqual(["0000–0000 of 0000"]);
    expect(reserved("table-pager-page")).toEqual(["Page 000 of 000"]);
  });

  it("disables only at the page bounds", () => {
    render(() => <TablePager page={4} pageSize={25} total={120} loading onPageChange={vi.fn()} />);
    expect((screen.getByTestId("table-pager-prev") as HTMLButtonElement).disabled).toBe(false);
    expect((screen.getByTestId("table-pager-next") as HTMLButtonElement).disabled).toBe(true);
  });
});
