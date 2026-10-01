import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import {
  NavSidebarAutoRestoreButton,
  NavSidebarCollapseButton,
  NavSidebarExpandButton,
} from "./NavSidebarToggle.tsx";

describe("NavSidebarToggle", () => {
  it("collapse button calls onClick", () => {
    const onClick = vi.fn();
    render(() => <NavSidebarCollapseButton onClick={onClick} />);
    fireEvent.click(screen.getByTestId("nav-collapse-btn"));
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("expand button calls onClick", () => {
    const onClick = vi.fn();
    render(() => <NavSidebarExpandButton onClick={onClick} />);
    fireEvent.click(screen.getByTestId("nav-expand-btn"));
    expect(onClick).toHaveBeenCalledOnce();
  });

  it("reverses collapse and expand arrows on a right-side rail", () => {
    const mirrored = (testId: string) =>
      screen.getByTestId(testId).querySelector("svg")!.classList.contains(
        "den-theme-icon--mirror",
      );

    const left = render(() => (
      <NavSidebarCollapseButton onClick={vi.fn()} />
    ));
    expect(mirrored("nav-collapse-btn")).toBe(false);
    left.unmount();

    const right = render(() => (
      <NavSidebarCollapseButton side="right" onClick={vi.fn()} />
    ));
    expect(mirrored("nav-collapse-btn")).toBe(true);
    right.unmount();

    render(() => <NavSidebarExpandButton side="right" onClick={vi.fn()} />);
    expect(mirrored("nav-expand-btn")).toBe(true);
  });

  it("keeps a manual expand action interactive and visually singular", () => {
    const onClick = vi.fn();
    render(() => <NavSidebarExpandButton onClick={onClick} />);

    const button = screen.getByTestId("nav-expand-btn");
    expect(button.getAttribute("aria-disabled")).toBeNull();
    expect(button.querySelectorAll("svg")).toHaveLength(1);
  });

  it("presents automatic collapse as a transient double-chevron state", () => {
    const onClick = vi.fn();
    render(() => (
      <NavSidebarAutoRestoreButton onClick={onClick} />
    ));

    const indicator = screen.getByTestId("nav-auto-restore-btn");
    expect(indicator.getAttribute("aria-disabled")).toBeNull();
    expect(indicator.getAttribute("aria-label")).toBe(
      "Widen window to restore sidebar",
    );
    expect(indicator.querySelectorAll("svg")).toHaveLength(2);
    expect(indicator.textContent).not.toContain("A");
    indicator.click();
    expect(onClick).toHaveBeenCalledOnce();
  });
});
