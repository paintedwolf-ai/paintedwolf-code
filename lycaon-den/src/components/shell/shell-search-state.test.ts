import { describe, expect, it, vi } from "vitest";
import type { Nav } from "./shell-navigation-state.ts";
import { createShellSearchState } from "./shell-search-state.ts";

function searchState() {
  let navigation: Nav = "projects";
  let splitCompanion = false;
  const closeLauncher = vi.fn();
  const routeToSearch = vi.fn((open: () => void) => open());
  const search = createShellSearchState({
    nav: () => navigation,
    showProjects: () => { navigation = "projects"; },
    showSearch: () => { navigation = "search"; },
    closeLauncher, routeToSearch,
    searchIsSplitCompanion: () => splitCompanion,
  });
  return { search, closeLauncher, routeToSearch,
    navigation: () => navigation,
    split: (value: boolean) => { splitCompanion = value; } };
}

describe("shell search lifecycle", () => {
  it("retains a split companion query when chat becomes foreground", () => {
    const { search, split, navigation } = searchState();
    search.openSearch({ originProjectId: "project", seed: "needle" });
    const retained = search.searchArgs();
    split(true);
    search.closeSearch();
    expect(navigation()).toBe("projects");
    expect(search.searchArgs()).toBe(retained);
    search.ensureSearch("another-project");
    expect(search.searchArgs()).toBe(retained);
    split(false);
    search.closeSearch();
    expect(search.searchArgs()).toBeNull();
  });

  it("reseeds repeated opens and keeps search and Crossbar serials independent", () => {
    const { search, closeLauncher, navigation } = searchState();
    search.ensureSearch("project");
    expect(navigation()).toBe("projects");
    expect(search.searchArgs()?.serial).toBe(1);
    search.openSearch({ originProjectId: "project", seed: "same" });
    search.closeSearch();
    search.openSearch({ originProjectId: "project", seed: "same" });
    expect(search.searchArgs()?.serial).toBe(3);
    search.openCrossbar();
    search.closeCrossbar();
    search.openCrossbar();
    expect(search.crossbarArgs()?.serial).toBe(2);
    expect(closeLauncher).toHaveBeenCalledTimes(2);
    expect(search.searchArgs()?.serial).toBe(3);
  });
});
