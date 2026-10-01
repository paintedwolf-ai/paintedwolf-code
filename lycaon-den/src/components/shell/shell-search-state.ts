import { createSignal } from "solid-js";
import { crossbarServerQuery } from "../../search/crossbar-model.ts";
import type { CrossbarMode } from "../../search/crossbar-modes.ts";
import type { ShellScope } from "./shell-scope.ts";

type SearchArgs = {
  originProjectId: string | null;
  seed?: string;
  replaceMode?: boolean;
  /** Monotonic key for repeated searches on the mounted stage. */
  serial: number;
};

type CrossbarArgs = {
  originProjectId: string | null;
  seed?: string;
  mode?: CrossbarMode;
  commandId?: string;
  /** Each open reseeds the mounted search surface. */
  serial?: number;
};

type SearchStateDependencies = Pick<ShellScope, "nav" | "showProjects"> & {
  showSearch: () => void;
  closeLauncher: () => void;
  routeToSearch: (open: () => void) => void;
  searchIsSplitCompanion: () => boolean;
};

export type ShellSearchState = ReturnType<typeof createShellSearchState>;

export function createShellSearchState({ nav, showProjects, showSearch,
  closeLauncher, routeToSearch, searchIsSplitCompanion }: SearchStateDependencies) {
  const [searchArgs, setSearchArgs] = createSignal<SearchArgs | null>(null);
  const [crossbarArgs, setCrossbarArgs] = createSignal<CrossbarArgs | null>(null);
  let searchArgsSerial = 0;
  const searchOpen = () => nav() === "search";
  const crossbarOpen = () => crossbarArgs() != null;

  // Search participates in full-stage navigation.
  const closeSearch = () => {
    // A split companion retains its query while chat becomes foreground.
    if (searchIsSplitCompanion()) {
      if (nav() === "search") showProjects();
      return;
    }
    setSearchArgs(null);
    if (nav() === "search") showProjects();
  };

  const openSearch = (
    args: Omit<SearchArgs, "serial"> = { originProjectId: null },
  ) => {
    searchArgsSerial += 1;
    setSearchArgs({ ...args, serial: searchArgsSerial });
    showSearch();
  };

  const closeCrossbar = () => setCrossbarArgs(null);

  let crossbarSerial = 0;
  const openCrossbar = (args: CrossbarArgs = { originProjectId: null }) => {
    closeLauncher();
    crossbarSerial += 1;
    setCrossbarArgs({ ...args, serial: crossbarSerial });
  };

  const escalateCrossbarToSearch = (args: {
    query: string;
    originProjectId: string | null;
    mode: CrossbarMode;
  }) => {
    // Full-stage search retains the search box group filter.
    const seed = crossbarServerQuery(args.query.trim(), args.mode);
    routeToSearch(() =>
      openSearch({
        originProjectId: args.originProjectId,
        seed: seed || undefined,
      }),
    );
  };

  const ensureSearch = (projectId: string) => {
    if (searchArgs() == null) {
      searchArgsSerial += 1;
      setSearchArgs({ originProjectId: projectId, serial: searchArgsSerial });
    }
  };

  return {
    searchArgs, crossbarArgs, searchOpen, crossbarOpen,
    closeSearch, openSearch, closeCrossbar, openCrossbar,
    escalateCrossbarToSearch, ensureSearch
  };
}
