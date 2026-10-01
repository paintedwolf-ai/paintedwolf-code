import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@solidjs/testing-library";
import type { SearchFacet } from "../../api/types.ts";
import { SearchFilterMenu } from "./SearchFilterMenu.tsx";

function renderMenu(overrides?: {
  query?: string;
  include?: string;
  exclude?: string;
  facets?: SearchFacet[];
}) {
  const onQueryChange = vi.fn();
  const onIncludeChange = vi.fn();
  const onExcludeChange = vi.fn();
  render(() => (
    <SearchFilterMenu
      query={overrides?.query ?? "auth kind:code"}
      facets={
        overrides?.facets ?? [
          { key: "kind", values: [{ value: "code", count: 3 }] },
          {
            key: "verified",
            values: [
              { value: "false", count: 2 },
              { value: "true", count: 5 },
            ],
          },
          { key: "source", values: [{ value: "tool", count: 4 }] },
        ]
      }
      onQueryChange={onQueryChange}
      include={overrides?.include ?? ""}
      exclude={overrides?.exclude ?? ""}
      onIncludeChange={onIncludeChange}
      onExcludeChange={onExcludeChange}
    />
  ));
  return { onQueryChange, onIncludeChange, onExcludeChange };
}

describe("SearchFilterMenu", () => {
  it("keeps result types out of Filter and exposes other facets", () => {
    renderMenu();
    expect(screen.queryByText("Kind")).toBeNull();
    expect(screen.getByText("Source")).toBeTruthy();
    expect(screen.getByText("Tool")).toBeTruthy();
  });

  it("sets verification as one explicit status choice", () => {
    const { onQueryChange } = renderMenu();
    fireEvent.click(screen.getByTestId("search-filter-verified-false"));
    expect(onQueryChange).toHaveBeenCalledWith("auth kind:code verified:false");
  });

  it("returns verification to any status without touching selected types", () => {
    const { onQueryChange } = renderMenu({
      query: "auth kind:evidence verified:false project:current",
    });
    fireEvent.click(screen.getByTestId("search-filter-verified-any"));
    expect(onQueryChange).toHaveBeenCalledWith(
      "auth kind:evidence project:current",
    );
  });

  it("rewrites the DSL when a dynamic facet is toggled", () => {
    const { onQueryChange } = renderMenu();
    fireEvent.click(screen.getByTestId("search-facet-source-tool"));
    expect(onQueryChange).toHaveBeenCalledWith("auth kind:code source:tool");
  });

  it("keeps selector patterns and clears advanced filters", () => {
    const { onQueryChange, onIncludeChange, onExcludeChange } = renderMenu({
      query: "kind:code verified:false source:tool project:current",
      include: "src/**",
      exclude: "vendor/**",
    });
    fireEvent.input(screen.getByTestId("search-include-glob"), {
      target: { value: "docs/**" },
    });
    expect(onIncludeChange).toHaveBeenCalledWith("docs/**");

    fireEvent.click(screen.getByTestId("search-filter-clear"));
    expect(onQueryChange).toHaveBeenCalledWith("kind:code project:current");
    expect(onIncludeChange).toHaveBeenCalledWith("");
    expect(onExcludeChange).toHaveBeenCalledWith("");
  });

  it("always exposes verification and file-pattern filters", () => {
    renderMenu({ facets: [] });
    expect(screen.getByTestId("search-filter-menu")).toBeTruthy();
    expect(screen.getByText("Verification")).toBeTruthy();
    expect(screen.getByText("File patterns")).toBeTruthy();
  });
});
