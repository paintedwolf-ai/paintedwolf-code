import { assertNamedControl } from "../../test/controls.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SearchFilterChip } from "./SearchFilterChip.tsx";

afterEach(cleanup);

describe("SearchFilterChip", () => {
  it("uses the shared anchored surface and dismisses outside it", () => {
    render(() => (
      <SearchFilterChip
        facets={[]}
        query=""
        onQueryChange={vi.fn()}
        include=""
        exclude=""
        onIncludeChange={vi.fn()}
        onExcludeChange={vi.fn()}
      />
    ));

    const trigger = screen.getByTestId("search-filter");
    expect(trigger.getAttribute("aria-expanded")).toBe("false");

    fireEvent.click(trigger);
    const panel = assertNamedControl(screen.getByTestId("search-filter-panel"), "dialog");
    expect(panel.dataset.denAnchoredSurface).toBeTruthy();
    expect(trigger.getAttribute("aria-expanded")).toBe("true");

    fireEvent.pointerDown(document.body);
    expect(screen.queryByTestId("search-filter-panel")).toBeNull();
  });

  it("shows active state for advanced query and glob filters", () => {
    render(() => (
      <SearchFilterChip
        facets={[]}
        query="kind:code verified:false"
        onQueryChange={vi.fn()}
        include="src/**"
        exclude=""
        onIncludeChange={vi.fn()}
        onExcludeChange={vi.fn()}
      />
    ));
    expect(screen.getByTestId("search-filter").getAttribute("aria-pressed")).toBe(
      "true",
    );
  });

  it("moves focus into the filter panel and restores it on Escape", async () => {
    render(() => (
      <SearchFilterChip
        facets={[]}
        query=""
        onQueryChange={vi.fn()}
        include=""
        exclude=""
        onIncludeChange={vi.fn()}
        onExcludeChange={vi.fn()}
      />
    ));
    const trigger = screen.getByTestId("search-filter");
    fireEvent.click(trigger);
    const firstChoice = screen.getByTestId("search-filter-verified-any");
    await waitFor(() => expect(document.activeElement).toBe(firstChoice));

    fireEvent.keyDown(firstChoice, { key: "Escape" });
    await waitFor(() =>
      expect(screen.queryByTestId("search-filter-panel")).toBeNull(),
    );
    expect(document.activeElement).toBe(trigger);
  });
});
