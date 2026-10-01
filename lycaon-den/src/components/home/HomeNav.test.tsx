import { fireEvent, render, screen } from "@solidjs/testing-library";
import { describe, expect, it, vi } from "vitest";
import { HomeNav } from "./HomeNav.tsx";

describe("HomeNav", () => {
  it("provides the Main navigation landmark on Home", () => {
    render(() => (
      <HomeNav
        section="recents"
        searchActive={false}
        onSectionChange={vi.fn()}
        onOpenSearch={vi.fn()}
        draftCount={0}
      />
    ));
    expect(screen.getByRole("navigation", { name: "Main" })).toBeTruthy();
  });

  it("marks Search active and clears Recents while search is open", () => {
    render(() => (
      <HomeNav
        section="recents"
        searchActive
        onSectionChange={vi.fn()}
        onOpenSearch={vi.fn()}
        draftCount={0}
      />
    ));
    expect(screen.getByTestId("home-nav-search").className).toContain(
      "home-nav__item--active",
    );
    expect(screen.getByTestId("home-nav-recents").className).not.toContain(
      "home-nav__item--active",
    );
  });

  it("leaves search when a section is selected", () => {
    const onSectionChange = vi.fn();
    render(() => (
      <HomeNav
        section="recents"
        searchActive
        onSectionChange={onSectionChange}
        onOpenSearch={vi.fn()}
        draftCount={0}
      />
    ));
    fireEvent.click(screen.getByTestId("home-nav-drafts"));
    expect(onSectionChange).toHaveBeenCalledWith("drafts");
  });
});
