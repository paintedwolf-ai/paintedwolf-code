import {
  setupGlobalSearchViewTests,
  searchMock,
  reportSearchErrorMock,
} from "./global-search-test-harness.ts";
import { applySourceChangesEvent } from "../../files/source/source-events.ts";
import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import { createAppStore } from "../../store/app-state.ts";
import { GlobalSearchView } from "./GlobalSearchView.tsx";

describe("GlobalSearchView", () => {
  setupGlobalSearchViewTests();

  it("refreshes settled results after external source changes without editing the query", async () => {
    vi.useFakeTimers();
    searchMock.mockResolvedValue({ hits: [], issues: [], total_hits: 0, status: "complete" });
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      appStore={createAppStore()} onNavigate={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(300);
    searchMock.mockClear();
    applySourceChangesEvent({ project_id: "proj-a", workspace_id: "workspace",
      workspace_kind: "project", resync: true, changes: [] });
    await vi.advanceTimersByTimeAsync(450);
    expect(searchMock).toHaveBeenCalledTimes(1);
    expect(searchMock.mock.calls[0]?.[2]).toMatchObject({ budget: "complete" });
  });

  it("keeps settled results in place during repeated background refreshes", async () => {
    vi.useFakeTimers();
    const result = {
      hits: [{ hit_id: "tool-a", hit_kind: "tool", source: "tool", project_id: "proj-a", title: "summarize" }],
      issues: [], total_hits: 1, status: "complete", exhaustive: true,
    };
    searchMock.mockResolvedValue(result);
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="tool:summarize" appStore={createAppStore()} onNavigate={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(300);
    const results = screen.getByRole("list", { name: "Search results" });
    const groupChildren = Array.from(screen.getByTestId("search-result-groups").children);
    expect(screen.getAllByTestId("search-result-row")).toHaveLength(1);

    for (let refresh = 0; refresh < 2; refresh++) {
      let finish!: (value: typeof result) => void;
      searchMock.mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
      applySourceChangesEvent({ project_id: "proj-a", workspace_id: "workspace",
        workspace_kind: "project", resync: true, changes: [] });
      await vi.advanceTimersByTimeAsync(450);
      expect(results.getAttribute("aria-busy")).toBe("true");
      expect(screen.queryByTestId("search-issue-note")).toBeNull();
      expect(Array.from(screen.getByTestId("search-result-groups").children)).toEqual(groupChildren);
      expect(screen.getAllByTestId("search-result-row")).toHaveLength(1);
      finish(result);
      await vi.advanceTimersByTimeAsync(0);
      expect(results.getAttribute("aria-busy")).toBe("false");
      expect(screen.queryByTestId("search-issue-note")).toBeNull();
    }
  });

  it("renders as a stage pane, not a modal dialog", () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    const view = screen.getByTestId("global-search-view");
    expect(view.getAttribute("role")).toBeNull();
    expect(view.getAttribute("aria-modal")).toBeNull();
    expect(view.getAttribute("aria-label")).toBe("Search");
  });

  it("re-runs the unchanged query while a project root is still being indexed", async () => {
    vi.useFakeTimers();
    const warming = {
      hits: [],
      status: "partial",
      exhaustive: false,
      total_hits: 0,
      count_relation: "lower_bound",
      facets_exhaustive: false,
      facets: [],
      interpreted: { scope: "current", filters: [] },
      issues: [{ executor: "code", reason: "catalog_warming", count: 1 }],
    };
    const settled = {
      ...warming,
      status: "complete",
      exhaustive: true,
      count_relation: "exact",
      facets_exhaustive: true,
      issues: [],
    };
    let resolveSettled!: (value: typeof settled) => void;
    const settledRequest = new Promise<typeof settled>((resolve) => {
      resolveSettled = resolve;
    });
    searchMock.mockReset();
    searchMock.mockResolvedValueOnce(warming).mockResolvedValueOnce(warming)
      .mockReturnValueOnce(settledRequest);
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await vi.advanceTimersByTimeAsync(280);
    expect(searchMock).toHaveBeenCalledTimes(2);
    expect(screen.getByText(/still indexing/i)).toBeTruthy();
    await vi.advanceTimersByTimeAsync(750);
    expect(searchMock).toHaveBeenCalledTimes(3);
    expect(searchMock.mock.calls[1]?.[0]).toBe(searchMock.mock.calls[0]?.[0]);
    expect(searchMock.mock.calls[0]?.[2]).toMatchObject({ budget: "interactive" });
    expect(searchMock.mock.calls[1]?.[2]).toMatchObject({ budget: "complete" });
    expect(searchMock.mock.calls[2]?.[0]).toBe(searchMock.mock.calls[0]?.[0]);
    expect(searchMock.mock.calls[2]?.[2]).toMatchObject({ budget: "complete" });
    expect(screen.queryByTestId("search-no-results")).toBeNull();
    expect(screen.getByTestId("search-stage-boundary").dataset.retained).toBe(
      "false",
    );
    expect(screen.queryByText("Updating results…")).toBeNull();
    expect(screen.getByText(/still indexing/i)).toBeTruthy();
    resolveSettled(settled);
    await vi.advanceTimersByTimeAsync(0);
    expect(screen.queryByText(/still indexing/i)).toBeNull();
    await vi.advanceTimersByTimeAsync(30_000);
    expect(searchMock).toHaveBeenCalledTimes(3);
  });

  it("summarizes overlapping coverage gaps while keeping partial matches visible", async () => {
    const partial = {
      hits: [{ hit_id: "one", hit_kind: "code", source: "code", project_id: "proj-a", path: "one.ts", title: "one.ts" }],
      status: "partial", exhaustive: false, total_hits: 1, count_relation: "lower_bound",
      facets_exhaustive: false, facets: [], interpreted: { scope: "current", filters: [] },
      issues: [
        { executor: "code", reason: "catalog_incomplete", count: 1 },
        { executor: "code", reason: "catalog_bounded", count: 3 },
      ],
    };
    searchMock.mockReset();
    searchMock.mockResolvedValue(partial);
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="needle"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(screen.getAllByTestId("search-result-row").length).toBeGreaterThan(0));
    expect(screen.getByText(/still indexing/i)).toBeTruthy();
    expect(screen.getByTestId("search-issue-note").textContent).toBe("Results may be incomplete. Still indexing…");
  });

  it("shows fast matches while the complete search is still pending", async () => {
    const first = {
      hits: [{ hit_id: "fast", hit_kind: "code", source: "code", project_id: "proj-a", path: "fast.ts", title: "fast.ts" }],
      status: "partial", exhaustive: false, total_hits: 1, count_relation: "lower_bound",
      facets_exhaustive: false, facets: [], interpreted: { scope: "current", filters: [] },
      issues: [{ executor: "code", reason: "result_limit" }],
    };
    let finish!: (value: unknown) => void;
    searchMock.mockReset();
    searchMock.mockResolvedValueOnce(first).mockImplementationOnce(() => new Promise((resolve) => { finish = resolve; }));
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="needle"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(2));
    expect(screen.getAllByTestId("search-result-row")[0]?.textContent).toContain("fast.ts");
    expect(screen.getByRole("list", { name: "Search results" }).getAttribute("aria-busy")).toBe("true");
    finish({ ...first, status: "complete", exhaustive: true, issues: [], count_relation: "exact", total_hits: 2,
      hits: [...first.hits, { ...first.hits[0], hit_id: "later", path: "later.ts", title: "later.ts" }],
    });
    await waitFor(() => expect(screen.getAllByTestId("search-result-row")).toHaveLength(2));
    expect(screen.getByRole("list", { name: "Search results" }).getAttribute("aria-busy")).toBe("false");
  });

  it("shows the stage while its initial project search runs", async () => {
    searchMock.mockImplementation(() => new Promise(() => {}));
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    const stage = screen.getByTestId("browse-stage-panel");
    expect(stage.getAttribute("data-boot")).toBe("ready");
    expect(screen.getByTestId("search-empty-state")).toBeTruthy();
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "kind:code project:current",
        "proj-a",
        expect.objectContaining({
          regex: false,
          caseSensitive: false,
          wholeWord: false,
        }),
        expect.any(AbortSignal),
      ),
    );
    expect(screen.getByTestId("search-loading-state")).toBeTruthy();
    expect(screen.getByTestId("search-hits-count").textContent).toBe(
      "Searching…",
    );
  });

  it("opens unscoped from the front page when there is no origin", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId={null}
        originName={null}
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    expect((screen.getByTestId("search-query-input") as HTMLInputElement).value).toBe("");
    expect(searchMock).not.toHaveBeenCalled();
    const stage = screen.getByTestId("browse-stage-panel");
    expect(stage.getAttribute("data-boot")).toBe("ready");
    expect(stage.classList.contains("den-enter-fade")).toBe(false);
  });

  it("clears loading state when an in-flight query is emptied", async () => {
    let resolveSearch: ((value: Awaited<ReturnType<typeof searchMock>>) => void) | undefined;
    searchMock.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolveSearch = resolve;
        }),
    );
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId={null}
        originName={null}
        seed="auth"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    fireEvent.input(screen.getByTestId("search-query-input"), {
      target: { value: "" },
    });
    await waitFor(() => expect(screen.getByTestId("search-empty-state")).toBeTruthy());
    resolveSearch?.({
      hits: [],
      status: "complete",
      exhaustive: true,
      total_hits: 0,
      count_relation: "exact",
      facets_exhaustive: true,
      facets: [],
      interpreted: { scope: "global", filters: [] },
    });
    await Promise.resolve();
    expect(screen.getByTestId("search-empty-state")).toBeTruthy();
  });

  it("shows a one-line empty state before a query", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId={null}
        originName={null}
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));

    expect(await screen.findByTestId("search-empty-state")).toBeTruthy();
    expect(screen.getByTestId("search-empty-state").textContent).toContain(
      "Search code, messages, and web",
    );
  });

  it("does not turn settled discovery failures into an exhaustive empty result", async () => {
    vi.useFakeTimers();
    searchMock.mockResolvedValue({ hits: [], status: "partial", exhaustive: false, total_hits: 0,
      count_relation: "lower_bound", facets: [], facets_exhaustive: false,
      issues: [{ executor: "code", reason: "catalog_failed", count: 1 }] });
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="foo" appStore={createAppStore()} onNavigate={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(10000);
    expect(screen.getByTestId("search-no-results").textContent).toContain("No results found so far");
    expect(screen.getByTestId("search-no-results").textContent).not.toContain("Nothing matched");
    expect(screen.getByTestId("search-issue-note").textContent).toBe("Some results are unavailable. Try again.");
    expect(searchMock).toHaveBeenCalledTimes(1);
  });

  it("dims complete results while a replacement query runs", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await screen.findByText("Alpha result");
    searchMock.mockImplementationOnce(() => new Promise(() => {}));

    fireEvent.click(screen.getByRole("button", { name: "Code" }));
    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(2));
    const boundary = screen.getByTestId("search-stage-boundary");
    expect(boundary.dataset.retained).toBe("true");
    expect(boundary.inert).toBe(true);
    expect(await screen.findByText("Updating results…")).toBeTruthy();
  });

  it("keeps the query field interactive while replacement results load", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await screen.findByText("Alpha result");
    searchMock.mockImplementation(() => new Promise(() => {}));
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;

    fireEvent.input(input, { target: { value: "alpha project:current" } });
    expect(input.value).toBe("alpha project:current");
    expect(input.closest("[inert]")).toBeNull();
    expect(screen.getByTestId("search-stage-boundary").dataset.retained).toBe(
      "false",
    );
    expect(screen.queryByText("Updating results…")).toBeNull();

    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(2));
    expect(screen.getByTestId("search-stage-boundary").inert).toBe(true);
    expect(input.closest("[inert]")).toBeNull();

    fireEvent.input(input, { target: { value: "alphabet project:current" } });
    expect(input.value).toBe("alphabet project:current");
    expect(input.closest("[inert]")).toBeNull();
  });
});

describe("search request lifecycle", () => {
  beforeEach(() => {
    cleanup();
    searchMock.mockReset().mockResolvedValue({
      hits: [], status: "complete", exhaustive: true, total_hits: 0,
      count_relation: "exact", facets_exhaustive: true, facets: [],
      interpreted: { scope: "current", filters: [] },
    });
    reportSearchErrorMock.mockClear();
  });

  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("does not re-send an identical search when the Filter popover toggles", async () => {
    vi.useFakeTimers();
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="needle project:current"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await vi.advanceTimersByTimeAsync(280);
    expect(searchMock).toHaveBeenCalledTimes(1);
    searchMock.mockClear();
    fireEvent.click(screen.getByTestId("search-filter"));
    fireEvent.click(screen.getByTestId("search-filter"));
    await vi.advanceTimersByTimeAsync(400);
    expect(searchMock).not.toHaveBeenCalled();
  });

  it("clears stale results and offers retry when the search fails", async () => {
    searchMock.mockRejectedValueOnce(new Error("engine down"));
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="needle project:current"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => {
      expect(screen.getByTestId("search-failed")).toBeTruthy();
    });
    expect(screen.queryAllByTestId("search-result-row")).toHaveLength(0);
    expect(reportSearchErrorMock).toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("search-failed-retry"));
    await waitFor(() => {
      expect(screen.queryByTestId("search-failed")).toBeNull();
    });
  });
});