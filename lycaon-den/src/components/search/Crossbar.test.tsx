import { applySourceChangesEvent } from "../../files/source/source-events.ts";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createSignal } from "solid-js";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { Crossbar } from "./Crossbar.tsx";
import { getLycaonClient } from "../../platform/connection/app-connection.ts";
import { openSourceLocation } from "../../platform/navigation/open-source.ts";
import {
  listRecentActionIds,
  recordRecentQuery,
} from "../../search/search-history.ts";
import {
  invokeCommand,
  registerCommandHandler,
  resetDispatcherForTests,
} from "../../shortcuts/dispatcher.ts";
import { setShortcutPlatformForTests } from "../../shortcuts/platform.ts";
import {
  resetShortcutPrefsForTests,
  saveShortcutOverride,
} from "../../settings/system/shortcut-prefs.ts";
import { crossbarCommands } from "../../contributions/dispatch.ts";
import {
  seedStockFrame,
  stockBindingId,
  stockId,
} from "../../contributions/stock-frame-test.ts";
import {
  seedContributionFrameForTest,
  resetContributionStoreForTest,
} from "../../contributions/contribution-store.ts";
import type { ContributionCommand, ContributionFrameResponse, SourceSearchResponse } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { CROSSBAR_GROUP_HIT_CAP } from "../../search/crossbar-model.ts";
import { openFilesBuffer, resetProjectFilesForTests } from "../../files/documents/project-files-buffers.ts";

vi.mock("../../platform/connection/app-connection.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/connection/app-connection.ts")>();
  return { ...actual, getLycaonClient: vi.fn(() => null) };
});

vi.mock("../../platform/navigation/open-source.ts", async (importOriginal) => {
  const actual =
    await importOriginal<typeof import("../../platform/navigation/open-source.ts")>();
  return { ...actual, openSourceLocation: vi.fn(async () => undefined) };
});

beforeEach(() => {
  localStorage.clear();
  vi.clearAllMocks();
  resetShortcutPrefsForTests();
  seedStockFrame();
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  resetDispatcherForTests();
  resetShortcutPrefsForTests();
  setShortcutPlatformForTests(null);
  localStorage.clear();
  resetContributionStoreForTest();
});

describe("Crossbar", () => {

  it("shows one compact note for overlapping search and file-index gaps without blocking matches", async () => {
    vi.useFakeTimers();
    const onNavigateHit = vi.fn();
    const hit = { hit_id: "code", hit_kind: "code", source: "code", project_id: "p1",
      root_id: "r", path: "schema.sql", title: "Schema definition" };
    const search = vi.fn().mockResolvedValue({ hits: [hit], exhaustive: false, issues: [
      { executor: "code", reason: "catalog_incomplete" },
      { executor: "code", reason: "catalog_bounded" },
      { executor: "code", reason: "time_budget" },
      { executor: "code", reason: "result_limit" },
    ] });
    const searchProjectSource = vi.fn().mockResolvedValue({ state: "ready", revision: 1, refreshing: true,
      coverage: [{ root_id: "r", state: "ready", discovery_complete: false, refreshing: true,
        bounded_directories: 1, failed_directories: 0 }],
      matches: [{ root_id: "r", path: "schema.sql", highlights: [] }],
    });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search, searchProjectSource }));
    render(() => <Crossbar open originProjectId="p1" seed="schema"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={onNavigateHit} />);
    await vi.advanceTimersByTimeAsync(280);
    expect(screen.getAllByTestId("crossbar-coverage")).toHaveLength(1);
    expect(screen.getByTestId("crossbar-coverage").textContent)
      .toBe("Results may be incomplete. Still indexing…");
    expect(screen.getByTestId("crossbar-inventory-file").textContent).toContain("schema.sql");
    fireEvent.click(screen.getByText("Schema definition"));
    await vi.advanceTimersByTimeAsync(0);
    expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
      projectId: "p1", rootId: "r", path: "schema.sql",
    }));
  });

  it("keeps a readable file query live until discovery finishes and cancels it on close", async () => {
    vi.useFakeTimers();
    const searchProjectSource = vi.fn()
      .mockResolvedValueOnce({ state: "ready", revision: 1, refreshing: true,
        coverage: [{ root_id: "r", state: "ready", discovery_complete: false, refreshing: true, bounded_directories: 0, failed_directories: 0 }],
        matches: [{ root_id: "r", path: "early.ts", highlights: [] }] })
      .mockResolvedValue({ state: "ready", revision: 2, refreshing: false, coverage: [],
        matches: [{ root_id: "r", path: "early.ts", highlights: [] }, { root_id: "r", path: "late.ts", highlights: [] }] });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ searchProjectSource }));
    const [open, setOpen] = createSignal(true);
    render(() => <Crossbar open={open()} originProjectId="p1" initialMode="files"
      onClose={() => setOpen(false)} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: "ts" } });
    await vi.advanceTimersByTimeAsync(280);
    expect(screen.getAllByTestId("crossbar-inventory-file")).toHaveLength(1);
    expect(screen.getByTestId("crossbar-coverage").textContent).toBe("Results may be incomplete. Still indexing…");
    await vi.advanceTimersByTimeAsync(750);
    expect(screen.getAllByTestId("crossbar-inventory-file")).toHaveLength(2);
    expect(screen.queryByTestId("crossbar-coverage")).toBeNull();
    expect(searchProjectSource.mock.calls.map(call => call[1])).toEqual(["ts", "ts"]);
    const signal = searchProjectSource.mock.calls[1]?.[2]?.signal;
    setOpen(false);
    expect(signal).toMatchObject({ aborted: true });
    await vi.advanceTimersByTimeAsync(10000);
    expect(searchProjectSource).toHaveBeenCalledTimes(2);
  });

  it("renders the host's ranking for a pasted full path and opens at its location", async () => {
    const searchProjectSource = vi.fn(async (_projectId: string, _query: string) => ({
      state: "ready" as const, revision: 1, refreshing: false, coverage: [],
      location: { line: 12, column: 3 },
      matches: [
        { root_id: "r", path: "docs/operations/release.md", highlights: [{ start: 0, end: 26 }] },
        { root_id: "r", path: ".task/before/docs/operations/release.md", highlights: [{ start: 13, end: 39 }] },
      ],
    }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ searchProjectSource }));
    render(() => <Crossbar open originProjectId="p1" initialMode="files"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    const pasted = "/Users/me/repo/docs/operations/release.md:12:3";
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: pasted } });
    const rows = await screen.findAllByTestId("crossbar-inventory-file");
    expect(searchProjectSource.mock.calls[0]?.[1]).toBe(pasted);
    expect(rows.map((row) => row.textContent)).toEqual([
      expect.stringContaining("docs/operations"),
      expect.stringContaining(".task/before/docs/operations"),
    ]);
    const underlined = (row: HTMLElement) =>
      [...row.querySelectorAll(".den-crossbar__match")].map((mark) => mark.textContent);
    expect(underlined(rows[0]!)).toEqual(["release.md", "docs/operations"]);
    expect(underlined(rows[1]!)).toEqual(["release.md", "docs/operations"]);
    fireEvent.click(rows[0]!);
    await waitFor(() => expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
      projectId: "p1", rootId: "r", path: "docs/operations/release.md", line: 12, column: 3, focus: true,
    })));
  });

  it("jumps to a line in the file the editor shows", async () => {
    resetProjectFilesForTests();
    openFilesBuffer("p1", { rootId: "r", rootLabel: "repo", path: "src/app.ts", intent: "permanent" });
    const searchProjectSource = vi.fn();
    const search = vi.fn();
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search, searchProjectSource }));
    render(() => <Crossbar open originProjectId="p1"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    const input = screen.getByTestId("crossbar-input");
    fireEvent.input(input, { target: { value: ":" } });
    expect(screen.getByTestId("crossbar-hit-status").textContent).toBe("Type a line number");
    fireEvent.input(input, { target: { value: ":42:7" } });
    const row = await screen.findByTestId("crossbar-line");
    expect(row.textContent).toContain("Go to line 42, column 7");
    expect(row.textContent).toContain("src/app.ts");
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() => expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
      projectId: "p1", rootId: "r", path: "src/app.ts", line: 42, column: 7, focus: true,
    })));
    expect(search).not.toHaveBeenCalled();
    expect(searchProjectSource).not.toHaveBeenCalled();
    resetProjectFilesForTests();
  });

  it("finds declarations through search under #, in the Symbols tab", async () => {
    const search = vi.fn().mockResolvedValue({
      hits: [{
        hit_id: "s1", hit_kind: "symbol", source: "code", project_id: "p1", root_id: "r",
        path: "internal/config/parse.go", line: 12, title: "ParseConfig",
        context: "internal/config/parse.go:12", symbol_kind: "function",
        title_highlights: [{ start: 0, end: 5 }],
      }],
    });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search }));
    render(() => <Crossbar open originProjectId="p1"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: "#parse" } });
    const row = await screen.findByTestId("crossbar-hit");
    expect(search).toHaveBeenCalledWith("kind:symbol parse", "p1", expect.objectContaining({ budget: "interactive" }), expect.anything());
    expect(screen.getByTestId("crossbar-mode-symbols").getAttribute("aria-selected")).toBe("true");
    expect(row.dataset.hitKind).toBe("symbol");
    expect(row.textContent).toContain("internal/config/parse.go:12");
    expect(row.textContent).toContain("Function");
    expect([...row.querySelectorAll(".den-crossbar__match")].map((mark) => mark.textContent)).toEqual(["Parse"]);
    fireEvent.click(row);
    await waitFor(() => expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
      projectId: "p1", rootId: "r", path: "internal/config/parse.go", line: 12, focus: true,
    })));
  });

  it("asks for a name before the Symbols tab searches", async () => {
    const search = vi.fn();
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search }));
    render(() => <Crossbar open originProjectId="p1"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: "#" } });
    expect((await screen.findByTestId("crossbar-hit-status")).textContent).toBe("Type a symbol name");
    fireEvent.click(screen.getByTestId("crossbar-mode-code"));
    expect((screen.getByTestId("crossbar-input") as HTMLInputElement).value).toBe("");
    expect(search).not.toHaveBeenCalled();
  });

  it("says a line jump needs an open file", () => {
    resetProjectFilesForTests();
    render(() => <Crossbar open originProjectId="p1" seed=":12"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    expect(screen.getByTestId("crossbar-hit-status").textContent).toBe("Open a file to go to a line");
  });

  it("treats > as the Actions tab and leaves it when another tab is chosen", async () => {
    const search = vi.fn();
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search }));
    render(() => <Crossbar open originProjectId="p1" contributionCommands={crossbarCommands()}
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    const input = screen.getByTestId("crossbar-input") as HTMLInputElement;
    fireEvent.input(input, { target: { value: ">settings" } });
    expect(screen.getByTestId("crossbar-mode-actions").getAttribute("aria-selected")).toBe("true");
    expect(screen.queryAllByTestId(/^crossbar-action-/).length).toBeGreaterThan(0);
    fireEvent.click(screen.getByTestId("crossbar-mode-everything"));
    expect(input.value).toBe("settings");
    expect(screen.getByTestId("crossbar-mode-everything").getAttribute("aria-selected")).toBe("true");
  });

  it("offers a path outside every root as a project", async () => {
    const onOpenFolderAsProject = vi.fn();
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: vi.fn().mockResolvedValue({ hits: [] }),
      searchProjectSource: vi.fn(async () => ({
        state: "ready" as const, revision: 1, refreshing: false, coverage: [], matches: [],
        outside: { path: "/elsewhere/notes/today.md", kind: "file" as const },
      })),
    }));
    render(() => <Crossbar open originProjectId="p1" initialMode="files"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} onOpenFolderAsProject={onOpenFolderAsProject} />);
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: "/elsewhere/notes/today.md" } });
    const open = await screen.findByTestId("crossbar-outside-open-project");
    expect(open.textContent).toContain("Open notes as a project");
    // The browser test runtime cannot reveal paths.
    expect(screen.queryByTestId("crossbar-outside-reveal")).toBeNull();
    fireEvent.click(open);
    await waitFor(() => expect(onOpenFolderAsProject).toHaveBeenCalledWith("/elsewhere/notes"));
  });

  it("opens a code host range at its lines", async () => {
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      searchProjectSource: vi.fn(async () => ({
        state: "ready" as const, revision: 1, refreshing: false, coverage: [],
        location: { line: 10, end_line: 20 },
        matches: [{ root_id: "r", path: "src/a.go", highlights: [{ start: 0, end: 8 }] }],
      })),
    }));
    render(() => <Crossbar open originProjectId="p1" initialMode="files"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "https://github.com/painted/wolf/blob/main/src/a.go#L10-L20" },
    });
    fireEvent.click(await screen.findByTestId("crossbar-inventory-file"));
    await waitFor(() => expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
      path: "src/a.go", line: 10, endLine: 20,
    })));
  });

  it("opens an indexed file in its attached root", async () => {
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      searchProjectSource: vi.fn(async () => ({
        state: "ready", revision: 1, refreshing: false, coverage: [],
        matches: [{ root_id: "secondary-root", path: "src/Model.ts", highlights: [] }],
      })),
    }));
    render(() => <Crossbar open originProjectId="p1" initialMode="files"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: "MOD" } });
    const row = await screen.findByTestId("crossbar-inventory-file");
    fireEvent.click(row);
    await waitFor(() => expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
      projectId: "p1", rootId: "secondary-root", path: "src/Model.ts",
    })));
  });

  it.each(["README.md", "src/Model.ts", "nested folder/café.ts"])(
    "distinguishes the same %s path in separate folders during keyboard activation", async (path) => {
      vi.mocked(getLycaonClient).mockReturnValue(stubClient({
        searchProjectSource: vi.fn(async () => ({
          state: "ready", revision: 1, refreshing: false, coverage: [],
          matches: [
            { root_id: "primary-root", path, highlights: [] }, { root_id: "secondary-root", path, highlights: [] },
          ],
        })),
      }));
      render(() => <Crossbar open originProjectId="p1" initialMode="files"
        projectRoots={[{ id: "primary-root", label: "Primary" }, { id: "secondary-root", label: "Second folder" }]}
        onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
      const input = screen.getByTestId("crossbar-input");
      fireEvent.input(input, { target: { value: path } });
      const rows = await screen.findAllByTestId("crossbar-inventory-file");
      expect(rows.map((row) => row.textContent)).toEqual([
        expect.stringContaining("@Primary"),
        expect.stringContaining("@Second folder"),
      ]);
      fireEvent.keyDown(input, { key: "ArrowDown" });
      fireEvent.keyDown(input, { key: "Enter" });
      await waitFor(() => expect(openSourceLocation).toHaveBeenCalledWith(expect.objectContaining({
        projectId: "p1", rootId: "secondary-root", path,
      })));
    },
  );

  it("aborts superseded searches and refreshes indexing only while open", async () => {
    vi.useFakeTimers();
    const search = vi.fn().mockImplementationOnce(() => new Promise(() => {}))
      .mockResolvedValue({ hits: [], issues: [{ reason: "index_warming" }] });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search }));
    const [open, setOpen] = createSignal(true);
    render(() => <Crossbar open={open()} originProjectId={null} onClose={() => setOpen(false)} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    const input = screen.getByTestId("crossbar-input");
    fireEvent.input(input, { target: { value: "first" } });
    await vi.advanceTimersByTimeAsync(280);
    expect(search).toHaveBeenCalledTimes(1);
    const firstSignal = search.mock.calls[0]?.[3];
    fireEvent.input(input, { target: { value: "second" } });
    expect(firstSignal).toMatchObject({ aborted: true });
    await vi.advanceTimersByTimeAsync(280);
    expect(search).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(750);
    expect(search).toHaveBeenCalledTimes(3);
    expect(search.mock.calls[2]?.[0]).toBe("second");
    const activeSignal = search.mock.calls[2]?.[3];
    setOpen(false);
    expect(activeSignal).toMatchObject({ aborted: true });
    await vi.advanceTimersByTimeAsync(30_000);
    expect(search).toHaveBeenCalledTimes(3);
  });

  it("retains results when an indexing refresh fails", async () => {
    vi.useFakeTimers();
    const search = vi.fn()
      .mockResolvedValueOnce({
        hits: [{
          hit_id: "current", hit_kind: "web", source: "tool",
          project_id: "p", title: "Current result",
        }],
        issues: [{ reason: "index_warming" }],
      })
      .mockRejectedValueOnce(new Error("refresh failed"));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search }));
    render(() => <Crossbar open originProjectId={null} onClose={vi.fn()}
      onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);

    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "current" },
    });
    await vi.advanceTimersByTimeAsync(280);
    expect(screen.getByText("Current result")).toBeTruthy();
    await vi.advanceTimersByTimeAsync(750);
    expect(search).toHaveBeenCalledTimes(2);

    expect(screen.getByText("Current result")).toBeTruthy();
    expect(screen.getByTestId("crossbar-coverage").textContent).toContain("could not refresh");
    expect(screen.queryByTestId("crossbar-searching")).toBeNull();
  });

  it("never calls an extension source for unprefixed input, then isolates prefixed results", async () => {
    vi.useFakeTimers();
    const activation: ContributionCommand = {
      id: "acme/issues:open",
      provider: "acme/issues",
      title: "Open issue",
      category: "Issues",
      icon: "tool",
      executor: "host",
      invocation: "project",
      action_kind: "mcp_tool",
      result_treatment: "discard",
      input: [{ id: "issue-id", title: "Issue", type: "string", required: true }],
    };
    const frame: ContributionFrameResponse = {
      frame_revision: "frame-source",
      commands: [activation],
      menus: [], keybindings: [], binding_defaults: [], editor_actions: [], themes: [],
      configuration: [], requirements: [], operations: [], notes: [],
      search_sources: [{
        id: "acme/issues:search", provider: "acme/issues", label: "Issues", prefix: "issues",
        requirement: "acme/issues:tracker", tool: "search", min_query_length: 2,
        max_results: 20, ready: true, activation_command: activation.id,
      }],
    };
    seedContributionFrameForTest(frame);
    const searchContributionSource = vi.fn(async () => ({
      frame_revision: "frame-source",
      source_id: "acme/issues:search",
      provider: "tracker",
      results: [{ id: "I-1", title: "Ship release", arguments: { "issue-id": "I-1" } }],
    }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: vi.fn(async () => ({ hits: [] })),
      searchProjectSource: vi.fn(async () => ({ state: "failed", revision: 1, refreshing: false, coverage: [], matches: [] })),
      searchContributionSource,
    }));

    render(() => (
      <Crossbar
        open
        originProjectId="p1"
        contributionCommands={[activation]}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));
    const input = screen.getByTestId("crossbar-input");
    fireEvent.input(input, { target: { value: "ship" } });
    expect(screen.queryByTestId("crossbar-searching")).toBeNull();
    await vi.advanceTimersByTimeAsync(350);
    expect(searchContributionSource).not.toHaveBeenCalled();

    fireEvent.input(input, { target: { value: "issues: sh" } });
    expect(screen.queryByTestId("crossbar-searching")).toBeNull();
    await vi.advanceTimersByTimeAsync(280);
    expect(searchContributionSource).toHaveBeenCalledOnce();
    expect(searchContributionSource).toHaveBeenCalledWith(
      "p1", "acme/issues:search",
      { frame_revision: "frame-source", query: "sh" },
      expect.any(AbortSignal),
    );
    const row = screen.getByText("Ship release");
    expect(row.closest("ul")?.textContent).toContain("From acme/issues / Issues");
    expect(screen.getAllByRole("tab", { selected: true }).map((tab) => tab.textContent)).toEqual(["Issues"]);
    for (const label of ["Everything", "Actions", "Messages", "Files", "Code", "Evidence"]) {
      fireEvent.click(screen.getByRole("tab", { name: label }));
      expect(screen.getAllByRole("tab", { selected: true }).map((tab) => tab.textContent)).toEqual([label]);
      expect((input as HTMLInputElement).value).toBe("sh");
      fireEvent.click(screen.getByRole("tab", { name: "Issues" }));
      expect((input as HTMLInputElement).value).toBe("issues: sh");
      expect(screen.getAllByRole("tab", { selected: true }).map((tab) => tab.textContent)).toEqual(["Issues"]);
    }
  });

  it("renders idle actions and closes after invoking settings", () => {
    const onClose = vi.fn();
    const settings = vi.fn();
    registerCommandHandler("settings.open", settings);

    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={onClose}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));

    expect(screen.getByTestId("crossbar")).toBeTruthy();
    expect(screen.getByTestId("crossbar-input").getAttribute("placeholder")).toBe(
      "Crossbar…",
    );
    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "settings" },
    });
    const settingsRow = screen.getByTestId(
      `crossbar-action-${stockId("settings-open")}`,
    );
    fireEvent.click(settingsRow);
    expect(settings).toHaveBeenCalledOnce();
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("closes before the command handler runs", () => {
    const onClose = vi.fn();
    let closedWhenHandlerRan: boolean | undefined;
    registerCommandHandler("settings.open", () => {
      closedWhenHandlerRan = onClose.mock.calls.length > 0;
    });

    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={onClose}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));

    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "settings" },
    });
    fireEvent.click(
      screen.getByTestId(`crossbar-action-${stockId("settings-open")}`),
    );
    expect(closedWhenHandlerRan).toBe(true);
  });

  it("cycles modes via mode pills", () => {
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByTestId("crossbar-mode-actions"));
    expect(
      screen.getByTestId("crossbar-mode-actions").getAttribute("aria-selected"),
    ).toBe("true");
    fireEvent.click(screen.getByTestId("crossbar-mode-evidence"));
    expect(
      screen.getByTestId("crossbar-mode-evidence").getAttribute("aria-selected"),
    ).toBe("true");
  });

  it("uses platform display and live rebinds for local search shortcuts", async () => {
    setShortcutPlatformForTests("macos");
    await saveShortcutOverride(
      stockBindingId("search-toggle-match-case"),
      "Mod+J",
    );
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));

    const toggle = screen.getByTestId("crossbar-toggle-case");
    expect(toggle.getAttribute("data-tip")).toBe("Match case (⌘J)");
    expect(toggle.getAttribute("aria-keyshortcuts")).toBe("Meta+J");

    const panel = screen.getByTestId("crossbar");
    fireEvent.keyDown(panel, {
      key: "j",
      code: "KeyJ",
      metaKey: true,
    });
    expect(toggle.getAttribute("aria-pressed")).toBe("true");

    expect(
      screen.getByTestId("crossbar-mode-symbols").getAttribute("data-tip"),
    ).toBe("Symbols (⌘5)");
    expect(
      screen.getByTestId("crossbar-mode-evidence").getAttribute("data-tip"),
    ).toBe("Evidence (⌘7)");
    fireEvent.keyDown(panel, {
      key: "5",
      code: "Digit5",
      metaKey: true,
    });
    expect(
      screen
        .getByTestId("crossbar-mode-symbols")
        .getAttribute("aria-selected"),
    ).toBe("true");
    fireEvent.keyDown(panel, {
      key: "7",
      code: "Digit7",
      metaKey: true,
    });
    expect(
      screen
        .getByTestId("crossbar-mode-evidence")
        .getAttribute("aria-selected"),
    ).toBe("true");
  });

  it("escalates with the current query after closing", async () => {
    const onClose = vi.fn();
    const onEscalate = vi.fn();
    render(() => (
      <Crossbar
        open
        originProjectId="proj-1"
        seed="needle"
        onClose={onClose}
        onEscalate={onEscalate}
        onNavigateHit={vi.fn()}
      />
    ));

    fireEvent.click(screen.getByTestId("crossbar-escalate"));
    expect(onClose).toHaveBeenCalledOnce();
    expect(onEscalate).toHaveBeenCalledWith({
      query: "needle",
      originProjectId: "proj-1",
      mode: "everything",
    });
  });

  it("restores focus on dismissal and relinquishes it on navigation", async () => {
    const [open, setOpen] = createSignal(false);
    render(() => (
      <>
        <button type="button" data-testid="palette-opener">Open</button>
        <button type="button" data-testid="search-destination">Destination</button>
        <Crossbar
          open={open()}
          originProjectId="proj-1"
          seed="needle"
          onClose={() => setOpen(false)}
          onEscalate={() => {
            screen.getByTestId("search-destination").focus();
            setOpen(false);
          }}
          onNavigateHit={vi.fn()}
        />
      </>
    ));

    const opener = screen.getByTestId("palette-opener");
    opener.focus();
    setOpen(true);
    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByTestId("crossbar-input"));
    });
    fireEvent.click(screen.getByTestId("crossbar-backdrop"));
    expect(document.activeElement).toBe(opener);

    setOpen(true);
    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByTestId("crossbar-input"));
    });
    fireEvent.click(screen.getByTestId("crossbar-escalate"));
    await waitFor(() => {
      expect(document.activeElement).toBe(screen.getByTestId("search-destination"));
    });
  });

  it("idle home lists recent queries; activating one seeds the input", () => {
    recordRecentQuery("kind:code auth", null);
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));

    const recent = screen.getByTestId("crossbar-recent");
    expect(recent.textContent).toContain("kind:code auth");
    fireEvent.click(recent);
    expect(
      (screen.getByTestId("crossbar-input") as HTMLInputElement).value,
    ).toBe("kind:code auth");
    expect(screen.getByTestId("crossbar")).toBeTruthy();
  });

  it("records last-run action ids for recency boost", () => {
    registerCommandHandler("settings.open", vi.fn());
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));
    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "settings" },
    });
    fireEvent.click(
      screen.getByTestId(`crossbar-action-${stockId("settings-open")}`),
    );
    expect(listRecentActionIds()).toEqual([stockId("settings-open")]);
  });

  it("footer keeps hints only — escalate lives in the list", () => {
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        seed="needle"
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));
    expect(screen.queryByTestId("crossbar-escalate-footer")).toBeNull();
    expect(screen.getByTestId("crossbar-escalate")).toBeTruthy();
    expect(screen.getByTestId("crossbar-footer").textContent).toContain("Esc");
  });

  it("go-to chat row navigates via onNavigateTarget after closing", async () => {
    const onClose = vi.fn();
    const onNavigateTarget = vi.fn();
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        gotoTargets={[
          {
            kind: "session",
            projectId: "p1",
            sessionId: "s1",
            label: "fix auth bug",
            context: "Api",
          },
        ]}
        onClose={onClose}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
        onNavigateTarget={onNavigateTarget}
      />
    ));

    const row = screen.getByTestId("crossbar-goto");
    expect(row.textContent).toContain("fix auth bug");
    expect(row.textContent).toContain("Chat");
    fireEvent.click(row);
    await waitFor(() => {
      expect(onNavigateTarget).toHaveBeenCalledWith({
        kind: "session",
        projectId: "p1",
        sessionId: "s1",
        label: "fix auth bug",
        context: "Api",
      });
    });
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("walks adjacent visible results with repeated arrow keys", () => {
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={[]}
        gotoTargets={[
          { kind: "project", projectId: "p1", label: "Target one" },
          { kind: "project", projectId: "p2", label: "Target two" },
          { kind: "project", projectId: "p3", label: "Target three" },
        ]}
        seed="target"
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));

    const rows = screen.getAllByTestId("crossbar-goto");
    const input = screen.getByTestId("crossbar-input");
    expect(
      rows[0]?.classList.contains("den-crossbar__row--active"),
    ).toBe(true);

    fireEvent.keyDown(input, { key: "ArrowDown", code: "ArrowDown" });
    expect(
      rows[1]?.classList.contains("den-crossbar__row--active"),
    ).toBe(true);

    fireEvent.keyDown(input, { key: "ArrowDown", code: "ArrowDown" });
    expect(
      rows[2]?.classList.contains("den-crossbar__row--active"),
    ).toBe(true);

    fireEvent.keyDown(input, { key: "ArrowUp", code: "ArrowUp" });
    expect(
      rows[1]?.classList.contains("den-crossbar__row--active"),
    ).toBe(true);

    fireEvent.input(input, { target: { value: "target t" } });
    const filteredRows = screen.getAllByTestId("crossbar-goto");
    expect(
      filteredRows[0]?.classList.contains("den-crossbar__row--active"),
    ).toBe(true);

    fireEvent.keyDown(input, { key: "ArrowDown", code: "ArrowDown" });
    expect(
      filteredRows[1]?.classList.contains("den-crossbar__row--active"),
    ).toBe(true);
  });

  it("Enter before results land opens the top hit, not depth search", async () => {
    let resolveSearch: ((v: unknown) => void) | undefined;
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: () =>
        new Promise((resolve) => {
          resolveSearch = resolve;
        }),
    }));

    const onEscalate = vi.fn();
    const onNavigateHit = vi.fn();
    const onClose = vi.fn();
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={onClose}
        onEscalate={onEscalate}
        onNavigateHit={onNavigateHit}
      />
    ));

    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "crossbar.md" },
    });
    await waitFor(() => expect(resolveSearch).toBeDefined());

    invokeCommand("list.confirm");
    expect(onEscalate).not.toHaveBeenCalled();

    resolveSearch?.({
      hits: [
        {
          hit_id: "file:crossbar",
          hit_kind: "file",
          source: "code",
          project_id: "p1",
          root_id: "secondary-root",
          path: "docs/crossbar.md",
          title: "crossbar.md",
          score: 0.95,
        },
      ],
    });

    await waitFor(() =>
      expect(vi.mocked(openSourceLocation)).toHaveBeenCalledTimes(1),
    );
    expect(vi.mocked(openSourceLocation).mock.calls[0]?.[0]).toMatchObject({
      projectId: "p1",
      rootId: "secondary-root",
      path: "docs/crossbar.md",
    });
    expect(onEscalate).not.toHaveBeenCalled();
    expect(onClose).toHaveBeenCalledOnce();
    expect(onNavigateHit).not.toHaveBeenCalled();
  });

  it("typing again revokes a queued Enter", async () => {
    vi.useFakeTimers();
    let resolveSearch: ((v: unknown) => void) | undefined;
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: () =>
        new Promise((resolve) => {
          resolveSearch = resolve;
        }),
    }));

    const onNavigateHit = vi.fn();
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={onNavigateHit}
      />
    ));

    const input = screen.getByTestId("crossbar-input");
    fireEvent.input(input, { target: { value: "omni" } });
    await vi.advanceTimersByTimeAsync(280);
    expect(resolveSearch).toBeDefined();
    invokeCommand("list.confirm");
    fireEvent.input(input, { target: { value: "crossbar.md" } });

    resolveSearch?.({
      hits: [
        {
          hit_kind: "file",
          source: "code",
          project_id: "p1",
          root_id: "secondary-root",
          path: "docs/crossbar.md",
          score: 0.95,
        },
      ],
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.mocked(openSourceLocation)).not.toHaveBeenCalled();
    expect(onNavigateHit).not.toHaveBeenCalled();
  });

  it("closing the box revokes a queued Enter and drops the in-flight search", async () => {
    vi.useFakeTimers();
    let resolveSearch: ((v: unknown) => void) | undefined;
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: () =>
        new Promise((resolve) => {
          resolveSearch = resolve;
        }),
    }));

    const [open, setOpen] = createSignal(true);
    const onNavigateHit = vi.fn();
    render(() => (
      <Crossbar
        open={open()}
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        onClose={() => setOpen(false)}
        onEscalate={vi.fn()}
        onNavigateHit={onNavigateHit}
      />
    ));

    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "omni" },
    });
    await vi.advanceTimersByTimeAsync(280);
    expect(resolveSearch).toBeDefined();
    invokeCommand("list.confirm");
    setOpen(false);
    resolveSearch?.({
      hits: [
        {
          hit_kind: "file",
          source: "code",
          project_id: "p1",
          root_id: "secondary-root",
          path: "docs/crossbar.md",
          score: 0.95,
        },
      ],
    });
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.mocked(openSourceLocation)).not.toHaveBeenCalled();
    expect(onNavigateHit).not.toHaveBeenCalled();

    setOpen(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByTestId("crossbar-hit")).toBeNull();
  });

  it("asks the host for the interactive search budget", async () => {
    const search = vi.fn().mockResolvedValue({ hits: [] });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search,
    }));

    render(() => (
      <Crossbar
        open
        originProjectId="proj-1"
        contributionCommands={crossbarCommands()}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));
    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "toolbar" },
    });
    await waitFor(() => expect(search).toHaveBeenCalled());
    expect(search.mock.calls[0]?.[2]).toMatchObject({ budget: "interactive" });
  });

  it("seeds Evidence mode when requested", () => {
    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={crossbarCommands()}
        initialMode="evidence"
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));
    expect(
      screen.getByTestId("crossbar-mode-evidence").getAttribute("aria-selected"),
    ).toBe("true");
  });
});

describe("source lane keyboard", () => {
  it("Enter activates the highlighted source result instead of escalating", async () => {
    const onEscalate = vi.fn();
    const activation: ContributionCommand = {
      id: "acme/issues:open",
      provider: "acme/issues",
      title: "Open issue",
      category: "Issues",
      icon: "tool",
      executor: "host",
      invocation: "project",
      action_kind: "mcp_tool",
      result_treatment: "discard",
      input: [{ id: "issue-id", title: "Issue", type: "string", required: true }],
    };
    const frame: ContributionFrameResponse = {
      frame_revision: "frame-source",
      commands: [activation],
      menus: [], keybindings: [], binding_defaults: [], editor_actions: [], themes: [],
      configuration: [], requirements: [], operations: [], notes: [],
      search_sources: [{
        id: "acme/issues:search", provider: "acme/issues", label: "Issues", prefix: "issues",
        requirement: "acme/issues:tracker", tool: "search", min_query_length: 2,
        max_results: 20, ready: true, activation_command: activation.id,
      }],
    };
    seedContributionFrameForTest(frame);
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: vi.fn(async () => ({ hits: [] })),
      searchContributionSource: vi.fn(async () => ({
        frame_revision: "frame-source",
        source_id: "acme/issues:search",
        provider: "tracker",
        results: [
          { id: "I-1", title: "Ship release", arguments: { "issue-id": "I-1" } },
          { id: "I-2", title: "Fix bug", arguments: { "issue-id": "I-2" } },
        ],
      })),
      resolveContributionChoices: vi.fn(async () => ({ choices: [] })),
    }));

    render(() => (
      <Crossbar
        open
        originProjectId="p1"
        contributionCommands={[activation]}
        onClose={vi.fn()}
        onEscalate={onEscalate}
        onNavigateHit={vi.fn()}
      />
    ));
    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "issues: sh" },
    });
    await screen.findByText("Fix bug");

    invokeCommand("list.down");
    const rows = screen.getAllByTestId("contribution-search-result");
    await waitFor(() => {
      expect(rows[1]?.classList.contains("den-crossbar__row--active")).toBe(true);
    });
    invokeCommand("list.confirm");
    await screen.findByTestId("contribution-command-flow");
    expect(onEscalate).not.toHaveBeenCalled();
  });
});

describe("in-flight search invalidation", () => {
  it.each([false, true])("retains the coverage note during source refresh, with matches: %s", async (hasMatches) => {
    vi.useFakeTimers();
    const partial: SourceSearchResponse = {
      state: "ready", revision: 1, refreshing: true,
      coverage: [{ root_id: "r", state: "ready", discovery_complete: false,
        refreshing: true, bounded_directories: 0, failed_directories: 0 }],
      matches: hasMatches ? [{ root_id: "r", path: "schema.sql", highlights: [] }] : [],
    };
    let finishRefresh!: (value: SourceSearchResponse) => void;
    const searchProjectSource = vi.fn().mockResolvedValueOnce(partial)
      .mockImplementationOnce(() => new Promise<SourceSearchResponse>((resolve) => { finishRefresh = resolve; }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ searchProjectSource }));
    render(() => <Crossbar open originProjectId="proj-a" initialMode="files" seed="schema"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(280);
    const noteId = hasMatches ? "crossbar-coverage" : "crossbar-hit-status";
    const note = screen.getByTestId(noteId);
    expect(note.textContent).toBe("Results may be incomplete. Still indexing…");

    applySourceChangesEvent({ project_id: "proj-a", workspace_id: "workspace",
      workspace_kind: "project", resync: true, changes: [] });
    await vi.advanceTimersByTimeAsync(400);
    expect(screen.getByTestId(noteId)).toBe(note);
    expect(note.textContent).toBe("Results may be incomplete. Still indexing…");
    await vi.advanceTimersByTimeAsync(280);
    expect(searchProjectSource).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId(noteId)).toBe(note);
    expect(note.textContent).toBe("Results may be incomplete. Still indexing…");
    expect(screen.queryAllByTestId("crossbar-inventory-file")).toHaveLength(hasMatches ? 1 : 0);

    finishRefresh({ ...partial, refreshing: false, coverage: [] });
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByText("Results may be incomplete. Still indexing…")).toBeNull();
  });

  it("retains search coverage while a replacement query is pending", async () => {
    vi.useFakeTimers();
    let finish!: (value: unknown) => void;
    const search = vi.fn().mockResolvedValueOnce({
      hits: [{ hit_id: "one", hit_kind: "message", source: "message", project_id: "p",
        title: "Schema discussion" }],
      exhaustive: false, issues: [{ executor: "code", reason: "catalog_incomplete" }],
    }).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search }));
    render(() => <Crossbar open originProjectId={null} seed="schema"
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(280);
    const note = screen.getByTestId("crossbar-coverage");
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: "schemas" } });
    expect(screen.getByTestId("crossbar-coverage")).toBe(note);
    await vi.advanceTimersByTimeAsync(280);
    expect(screen.getByTestId("crossbar-coverage")).toBe(note);
    expect(screen.getByText("Schema discussion")).toBeTruthy();
    finish({ hits: [], exhaustive: true, issues: [] });
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByTestId("crossbar-coverage")).toBeNull();
  });

  it("refreshes the open search box after source changes", async () => {
    vi.useFakeTimers();
    const search = vi.fn().mockResolvedValue({ hits: [], issues: [] });
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({ search }));
    render(() => <Crossbar open originProjectId="proj-a" contributionCommands={[]}
      onClose={vi.fn()} onEscalate={vi.fn()} onNavigateHit={vi.fn()} />);
    fireEvent.input(screen.getByTestId("crossbar-input"), { target: { value: "needle" } });
    await vi.advanceTimersByTimeAsync(300);
    search.mockClear();
    applySourceChangesEvent({ project_id: "proj-a", workspace_id: "workspace",
      workspace_kind: "project", resync: true, changes: [] });
    await vi.advanceTimersByTimeAsync(450);
    expect(search).toHaveBeenCalledTimes(1);
    expect(search.mock.calls[0]?.[0]).toContain("needle");
  });

  it("drops a slow response that lands after a newer query was scheduled", async () => {
    vi.useFakeTimers();
    const resolvers: Array<(value: unknown) => void> = [];
    const search = vi.fn(
      () => new Promise((resolve) => resolvers.push(resolve)),
    );
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search,
    }));

    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={[]}
        onClose={vi.fn()}
        onEscalate={vi.fn()}
        onNavigateHit={vi.fn()}
      />
    ));
    const input = screen.getByTestId("crossbar-input");
    fireEvent.input(input, { target: { value: "alpha" } });
    await vi.advanceTimersByTimeAsync(280);
    expect(search).toHaveBeenCalledTimes(1);

    fireEvent.input(input, { target: { value: "alphabet" } });
    resolvers[0]?.({
      hits: [{
        hit_id: "stale-1", hit_kind: "web", source: "tool",
        project_id: "p", title: "Stale alpha hit",
      }],
    });
    await vi.advanceTimersByTimeAsync(50);
    expect(screen.queryByText("Stale alpha hit")).toBeNull();
    expect(screen.queryByTestId("crossbar-searching")).toBeNull();
    await vi.advanceTimersByTimeAsync(280);
    expect(search).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId("crossbar-searching")).toBeTruthy();
  });
});

describe("selection identity across async rows", () => {
  it("keeps Enter on the escalate row when hits insert above it", async () => {
    const onEscalate = vi.fn();
    const onNavigateHit = vi.fn();
    const resolvers: Array<(value: unknown) => void> = [];
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: vi.fn(() => new Promise((resolve) => resolvers.push(resolve))),
    }));

    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={[]}
        onClose={vi.fn()}
        onEscalate={onEscalate}
        onNavigateHit={onNavigateHit}
      />
    ));
    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "needle" },
    });
    await waitFor(() => expect(resolvers).toHaveLength(1));
    invokeCommand("list.down");
    resolvers[0]?.({
      hits: [{
        hit_id: "hit-1", hit_kind: "web", source: "tool",
        project_id: "p", title: "Landed hit",
      }],
    });
    await screen.findByText("Landed hit");
    invokeCommand("list.confirm");
    await waitFor(() => expect(onEscalate).toHaveBeenCalledOnce());
    expect(onNavigateHit).not.toHaveBeenCalled();
  });

  it("Everything sections hits by group; See all opens depth in that group", async () => {
    const onClose = vi.fn();
    const onEscalate = vi.fn();
    const fileHits = Array.from({ length: 6 }, (_, i) => ({
      hit_id: `f${i}`, hit_kind: "file", source: "code",
      project_id: "p", path: `docs/needle-${i}.md`, title: `needle-${i}.md`,
    }));
    vi.mocked(getLycaonClient).mockReturnValue(stubClient({
      search: vi.fn().mockResolvedValue({
        hits: [
          ...fileHits,
          {
            hit_id: "c0", hit_kind: "code", source: "code",
            project_id: "p", path: "src/a.ts", line: 3, title: "const needle = 1",
          },
        ],
      }),
    }));

    render(() => (
      <Crossbar
        open
        originProjectId={null}
        contributionCommands={[]}
        onClose={onClose}
        onEscalate={onEscalate}
        onNavigateHit={vi.fn()}
      />
    ));
    fireEvent.input(screen.getByTestId("crossbar-input"), {
      target: { value: "needle" },
    });
    await screen.findByText("const needle = 1");

    expect(screen.getByTestId("crossbar-section-section-hits-files").textContent).toContain("Files");
    expect(screen.getByTestId("crossbar-section-section-hits-code").textContent).toContain("Code");
    expect(
      screen.getAllByTestId("crossbar-hit").filter((el) => el.dataset.hitKind === "file"),
    ).toHaveLength(CROSSBAR_GROUP_HIT_CAP);
    expect(screen.queryByTestId("crossbar-see-all-code")).toBeNull();

    fireEvent.click(screen.getByTestId("crossbar-see-all-files"));
    expect(onClose).toHaveBeenCalledOnce();
    await waitFor(() => {
      expect(onEscalate).toHaveBeenCalledWith({
        query: "needle",
        originProjectId: null,
        mode: "files",
      });
    });
  });
});
