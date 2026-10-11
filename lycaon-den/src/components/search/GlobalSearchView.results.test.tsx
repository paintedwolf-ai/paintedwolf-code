import {
  setupGlobalSearchViewTests,
  searchMock,
  exportMock,
} from "./global-search-test-harness.ts";

import {
  describe,
  expect,
  it,
  vi,
} from "vitest";
import {
  fireEvent,
  render,
  screen,
  waitFor,
} from "@solidjs/testing-library";
import { createAppStore } from "../../store/app-state.ts";
import { GlobalSearchView } from "./GlobalSearchView.tsx";

describe("GlobalSearchView", () => {
  setupGlobalSearchViewTests();

  it("groups origin project first after search", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="auth"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    const firstGroup = await screen.findByTestId("search-group-proj-a");
    expect(firstGroup).toBeTruthy();
  });

  it("navigates cross-project hits through onNavigate", async () => {
    const onNavigate = vi.fn();
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="auth"
        appStore={createAppStore()}
        onNavigate={onNavigate}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    const rows = await screen.findAllByTestId("search-result-row");
    fireEvent.click(rows[0]!);
    fireEvent.click(screen.getAllByTestId("search-result-open")[0]!);
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: expect.any(String) }),
    );
  });

  it("reveals the tool chicklet anchored by call id for a tool hit", async () => {
    searchMock.mockResolvedValue({
      hits: [
        {
          hit_id: "tool-42",
          hit_kind: "tool",
          source: "message",
          project_id: "proj-a",
          project_name: "Alpha",
          session_id: "sess-a",
          source_ref: "call-42",
          title: "tool",
        },
      ],
      status: "complete",
      exhaustive: true,
      total_hits: 1,
      count_relation: "exact",
      facets_exhaustive: true,
      facets: [],
      interpreted: { scope: "global", filters: [] },
    });
    const onNavigate = vi.fn();
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="command"
        appStore={createAppStore()}
        onNavigate={onNavigate}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    const rows = await screen.findAllByTestId("search-result-row");
    fireEvent.click(rows[0]!);
    fireEvent.click(screen.getByTestId("search-result-open"));
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({
        projectId: "proj-a",
        sessionId: "sess-a",
        reveal: expect.objectContaining({
          anchorId: "call-42",
          chicklet: "tool",
        }),
      }),
    );
  });

  it("opens the parent session and worker reveal for a worker hit", async () => {
    searchMock.mockResolvedValue({
      hits: [
        {
          hit_id: "worker-msg-7",
          hit_kind: "evidence",
          source: "message",
          project_id: "proj-a",
          project_name: "Alpha",
          session_id: "child-sess",
          parent_session_id: "parent-sess",
          worker_id: "worker-1",
          source_ref: "msg-7",
          title: "Worker evidence",
        },
      ],
      status: "complete",
      exhaustive: true,
      total_hits: 1,
      count_relation: "exact",
      facets_exhaustive: true,
      facets: [],
      interpreted: { scope: "global", filters: [] },
    });
    const onNavigate = vi.fn();
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="auth"
        appStore={createAppStore()}
        onNavigate={onNavigate}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    const rows = await screen.findAllByTestId("search-result-row");
    fireEvent.click(rows[0]!);
    fireEvent.click(screen.getByTestId("search-result-open"));
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({
        projectId: "proj-a",
        sessionId: "parent-sess",
        reveal: expect.objectContaining({
          anchorId: "msg-7",
          chicklet: "citation",
          worker: { workerId: "worker-1", childSessionId: "child-sess" },
        }),
      }),
    );
  });

  it("disables SARIF export until the query is scan-scoped", async () => {
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
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    fireEvent.click(await screen.findByTestId("search-overflow-trigger"));
    const sarif = (await screen.findByTestId("search-export-sarif")) as HTMLButtonElement;
    expect(sarif.disabled).toBe(true);
  });

  it("downloads JSONL export and shows truncation notice", async () => {
    exportMock.mockResolvedValue({
      blob: new Blob(['{"hit_kind":"web"}\n'], { type: "application/x-ndjson" }),
      truncated: true,
      filename: "search-export.jsonl",
    });
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="auth"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    fireEvent.click(await screen.findByTestId("search-overflow-trigger"));
    fireEvent.click(await screen.findByTestId("search-export-jsonl"));
    await waitFor(() =>
      expect(exportMock).toHaveBeenCalledWith(
        "auth",
        "jsonl",
        "proj-a",
        {
          regex: false,
          includeDependencies: false,
          caseSensitive: false,
          wholeWord: false,
          include: undefined,
          exclude: undefined,
        },
      ),
    );
    expect(await screen.findByTestId("search-export-truncated")).toBeTruthy();
    clickSpy.mockRestore();
  });

  it("keeps Recent in the tools track with nothing to offer", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));

    const tools = await screen.findByTestId("search-tools");
    expect(tools.getAttribute("role")).toBe("toolbar");
    expect(tools.getAttribute("aria-label")).toBe("Search tools");
    expect([...tools.children].map((segment) => segment.textContent)).toEqual([
      "Filter",
      "Recent",
      "Export",
    ]);

    const recent = screen.getByTestId("search-recent-menu-trigger") as HTMLButtonElement;
    expect(recent.disabled).toBe(true);
    fireEvent.click(recent);
    expect(screen.queryByRole("menu")).toBeNull();
  });

  it("opens Recent once a query has run", async () => {
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
    await waitFor(() => expect(searchMock).toHaveBeenCalled());

    const recent = (await screen.findByTestId(
      "search-recent-menu-trigger",
    )) as HTMLButtonElement;
    await waitFor(() => expect(recent.disabled).toBe(false));
    fireEvent.click(recent);
    expect(await screen.findByRole("menuitem", { name: "needle" })).toBeTruthy();
  });

  it("empty-state example fills the query bar and searches", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId={null}
        originName={null}
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    const examples = await screen.findAllByTestId("search-empty-example");
    fireEvent.click(examples[0]!);
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "kind:code",
        undefined,
        expect.objectContaining({ regex: false }),
        expect.any(AbortSignal),
      ),
    );
  });

  it("replaces the visible page and returns to a cached previous page", async () => {
    const base = {
      status: "complete",
      exhaustive: true,
      total_hits: 2,
      count_relation: "exact",
      facets_exhaustive: true,
      facets: [],
      interpreted: { scope: "global", filters: [] },
    } as const;
    searchMock
      .mockResolvedValueOnce({
        ...base,
        next_cursor: "page-2",
        hits: [
          {
            hit_id: "code-1",
            hit_kind: "code",
            source: "code",
            project_id: "proj-a",
            path: "one.ts",
            title: "one.ts",
          },
        ],
      })
      .mockResolvedValueOnce({
        ...base,
        hits: [
          {
            hit_id: "code-2",
            hit_kind: "code",
            source: "code",
            project_id: "proj-a",
            path: "two.ts",
            title: "two.ts",
          },
        ],
      });

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

    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(1));
    expect(screen.getAllByTestId("search-result-row")[0]?.textContent).toContain(
      "one.ts",
    );
    fireEvent.click(screen.getByTestId("search-page-next"));

    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(2));
    expect(searchMock.mock.calls[1]?.[2]).toMatchObject({ cursor: "page-2" });
    expect(screen.getAllByTestId("search-result-row")).toHaveLength(1);
    expect(screen.queryByText("one.ts")).toBeNull();
    expect(screen.getAllByTestId("search-result-row")[0]?.textContent).toContain(
      "two.ts",
    );
    expect(screen.getByTestId("search-results-footer").textContent).toContain(
      "2–2 of 2 results",
    );
    fireEvent.click(screen.getByTestId("search-page-previous"));
    expect(screen.getAllByTestId("search-result-row")[0]?.textContent).toContain(
      "one.ts",
    );
    expect(searchMock).toHaveBeenCalledTimes(2);
  });

  it("pages through a bounded row set and retries the next cursor", async () => {
    const hits = Array.from({ length: 100 }, (_, index) => ({
      hit_id: `code-${index}`,
      hit_kind: "code" as const,
      source: "code",
      project_id: "proj-a",
      path: `src/file-${index}.ts`,
      title: `file-${index}.ts`,
    }));
    searchMock
      .mockResolvedValueOnce({
        hits,
        status: "complete",
        exhaustive: true,
        total_hits: 201,
        count_relation: "exact",
        facets_exhaustive: true,
        facets: [],
        interpreted: { scope: "global", filters: [] },
        next_cursor: "page-2",
      })
      .mockResolvedValueOnce({
        hits: Array.from({ length: 100 }, (_, index) => ({
          hit_id: `code-${index + 100}`,
          hit_kind: "code" as const,
          source: "code",
          project_id: "proj-a",
          path: `src/file-${index + 100}.ts`,
          title: `file-${index + 100}.ts`,
        })),
        status: "complete",
        exhaustive: true,
        total_hits: 201,
        count_relation: "exact",
        facets_exhaustive: true,
        facets: [],
        interpreted: { scope: "global", filters: [] },
        next_cursor: "page-3",
      })
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({
        hits: [
          {
            hit_id: "code-200",
            hit_kind: "code",
            source: "code",
            project_id: "proj-a",
            path: "src/file-200.ts",
            title: "file-200.ts",
          },
        ],
        status: "complete",
        exhaustive: true,
        total_hits: 201,
        count_relation: "exact",
        facets_exhaustive: true,
        facets: [],
        interpreted: { scope: "global", filters: [] },
      });

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

    const nextPage = await screen.findByTestId("search-page-next");
    expect(nextPage.textContent).toBe("Next");
    expect(screen.getByTestId("search-results-footer").textContent).toContain(
      "1–100 of 201 results",
    );
    fireEvent.click(nextPage);

    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(2));
    expect(searchMock.mock.calls[1]?.[2]).toMatchObject({ cursor: "page-2" });
    await waitFor(() =>
      expect(screen.getByTestId("search-results-footer").textContent).toContain(
        "101–200 of 201 results",
      ),
    );
    expect(screen.getAllByTestId("search-result-row")).toHaveLength(100);
    expect(screen.getAllByTestId("search-result-row")[0]?.textContent).toContain(
      "file-100.ts",
    );
    expect(screen.queryByText("file-0.ts")).toBeNull();

    fireEvent.click(screen.getByTestId("search-page-next"));
    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(3));
    expect(searchMock.mock.calls[2]?.[2]).toMatchObject({ cursor: "page-3" });
    const retry = await screen.findByTestId("search-page-next");
    await waitFor(() => expect(retry.textContent).toBe("Retry"));
    expect(screen.getByTestId("search-results-footer").textContent).toContain(
      "Couldn't load the next page.",
    );
    fireEvent.click(retry);

    await waitFor(() => expect(searchMock).toHaveBeenCalledTimes(4));
    expect(searchMock.mock.calls[3]?.[2]).toMatchObject({ cursor: "page-3" });
    await waitFor(() =>
      expect(screen.getByTestId("search-results-footer").textContent).toContain(
        "201–201 of 201 results",
      ),
    );
    expect(screen.getAllByTestId("search-result-row")).toHaveLength(1);
    expect(screen.getAllByTestId("search-result-row")[0]?.textContent).toContain(
      "file-200.ts",
    );
    expect(screen.queryByTestId("search-page-next")).toBeNull();
    fireEvent.click(screen.getByTestId("search-page-previous"));
    expect(screen.getAllByTestId("search-result-row")[0]?.textContent).toContain(
      "file-100.ts",
    );
    expect(searchMock).toHaveBeenCalledTimes(4);
  });

  it("walks results with arrow keys and opens the selection with Enter", async () => {
    const onNavigate = vi.fn();
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="auth"
        appStore={createAppStore()}
        onNavigate={onNavigate}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    await screen.findAllByTestId("search-result-row");
    const view = screen.getByTestId("global-search-view");

    fireEvent.keyDown(view, { key: "ArrowDown" });
    let rows = screen.getAllByTestId("search-result-row");
    expect(rows[0]?.classList.contains("den-search-row--selected")).toBe(true);
    expect(screen.getByTestId("search-selection-status").textContent).toContain(
      "Press Enter to open",
    );

    fireEvent.keyDown(view, { key: "ArrowDown" });
    rows = screen.getAllByTestId("search-result-row");
    expect(rows[0]?.classList.contains("den-search-row--selected")).toBe(false);
    expect(rows[1]?.classList.contains("den-search-row--selected")).toBe(true);

    fireEvent.keyDown(view, { key: "Enter" });
    expect(onNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ projectId: "proj-b" }),
    );

    fireEvent.keyDown(view, { key: "Escape" });
    rows = screen.getAllByTestId("search-result-row");
    expect(rows.some((row) => row.classList.contains("den-search-row--selected"))).toBe(
      false,
    );
  });

  it("offers a widen-scope recovery when a scoped query has no results", async () => {
    searchMock.mockResolvedValue({
      hits: [],
      status: "complete",
      exhaustive: true,
      total_hits: 0,
      count_relation: "exact",
      facets_exhaustive: true,
      facets: [],
      interpreted: { scope: "current", filters: [] },
    });
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="project:current nothing-matches"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    const widen = await screen.findByTestId("search-no-results-everything");
    fireEvent.click(widen);
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        expect.not.stringContaining("project:current"),
        "proj-a",
        expect.any(Object),
        expect.any(AbortSignal),
      ),
    );
  });

  it("clears refinements without clearing result types or scope", async () => {
    searchMock.mockResolvedValue({
      hits: [],
      status: "complete",
      exhaustive: true,
      total_hits: 0,
      count_relation: "exact",
      facets_exhaustive: true,
      facets: [],
      interpreted: { scope: "current", filters: [] },
    });
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="kind:code verified:false project:current"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());

    const filter = screen.getByTestId("search-filter");
    fireEvent.click(filter);
    fireEvent.input(screen.getByTestId("search-include-glob"), {
      target: { value: "src/**" },
    });
    fireEvent.click(filter);

    fireEvent.click(
      await screen.findByTestId("search-no-results-clear-filters"),
    );
    const input = screen.getByTestId("search-query-input") as HTMLInputElement;
    await waitFor(() => expect(input.value).toBe("kind:code"));
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "kind:code project:current",
        "proj-a",
        expect.any(Object),
        expect.any(AbortSignal),
      ),
    );

    fireEvent.click(filter);
    expect(
      (screen.getByTestId("search-include-glob") as HTMLInputElement).value,
    ).toBe("");
  });

  it("toggles result types from the single BrowseChrome selector row", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "kind:code project:current",
        "proj-a",
        expect.any(Object),
        expect.any(AbortSignal),
      ),
    );
    await waitFor(() =>
      expect(screen.getByTestId("browse-stage-panel").dataset.boot).toBe("ready"),
    );
    expect(screen.getByTestId("search-type-filters")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Code" }));
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "project:current",
        "proj-a",
        expect.any(Object),
        expect.any(AbortSignal),
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "Code" }));
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "kind:code project:current",
        "proj-a",
        expect.any(Object),
        expect.any(AbortSignal),
      ),
    );
  });

  it("persists match toggles and sends flags on search", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="toolbar"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    fireEvent.click(screen.getByTestId("search-toggle-case"));
    fireEvent.click(screen.getByTestId("search-toggle-regex"));
    await waitFor(() =>
      expect(searchMock).toHaveBeenCalledWith(
        "toolbar",
        "proj-a",
        expect.objectContaining({ caseSensitive: true, regex: true }),
        expect.any(AbortSignal),
      ),
    );
    expect(screen.getByTestId("search-toggle-case").getAttribute("aria-pressed")).toBe(
      "true",
    );
  });

  it("keeps highlighting tied to the displayed response while match flags change", async () => {
    const result = {
      hits: [{ hit_id: "case-code", hit_kind: "code", source: "code", project_id: "proj-a",
        title: "Maple maple MAPLE", score: 1 }],
      status: "complete", exhaustive: true, total_hits: 1, count_relation: "exact",
      facets_exhaustive: true, facets: [],
      interpreted: { scope: "current", fts_terms: ["Maple"], filters: [] },
    };
    searchMock.mockResolvedValue(result);
    const { container } = render(() => (
      <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
        seed="kind:code Maple" appStore={createAppStore()} onNavigate={vi.fn()} />
    ));
    const marks = () => Array.from(container.querySelectorAll(".den-search-row mark"),
      (mark) => mark.textContent);
    await waitFor(() => expect(marks()).toEqual(["Maple", "maple", "MAPLE"]));
    let resolveNext!: (value: typeof result) => void;
    searchMock.mockImplementation(() => new Promise((resolve) => { resolveNext = resolve; }));
    fireEvent.click(screen.getByTestId("search-toggle-case"));
    await waitFor(() => expect(searchMock).toHaveBeenLastCalledWith(
      "kind:code Maple", "proj-a", expect.objectContaining({ caseSensitive: true }),
      expect.any(AbortSignal),
    ));
    expect(marks()).toEqual(["Maple", "maple", "MAPLE"]);
    resolveNext(result);
    await waitFor(() => expect(marks()).toEqual(["Maple"]));
  });
});

describe("result type selectors", () => {
  it("adds every Evidence kind to Code as one positive OR selection", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="Nomos kind:code project:current"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    const chip = screen.getByTestId("search-type-evidence");
    expect(chip.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(chip);
    const input = screen.getByTestId(
      "search-query-input",
    ) as HTMLInputElement;
    await waitFor(() => {
      expect(input.value).toContain("(kind:code OR kind:evidence OR kind:claim");
    });
    expect(input.value).toContain("kind:web");
    expect(input.value).not.toContain("NOT kind:");
    expect(screen.getByTestId("search-type-code").getAttribute("aria-pressed")).toBe(
      "true",
    );
    expect(chip.getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(chip);
    await waitFor(() => {
      expect(input.value).toContain("kind:code");
      expect(input.value).not.toContain("kind:evidence");
      expect(input.value).not.toContain("kind:web");
    });
  });

  it("selecting two kinds composes an OR group instead of an empty AND", async () => {
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="Nomos kind:code project:current"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    fireEvent.click(screen.getByTestId("search-type-symbols"));
    const input = screen.getByTestId(
      "search-query-input",
    ) as HTMLInputElement;
    await waitFor(() => {
      expect(input.value).toContain("(kind:code OR kind:symbol)");
    });
    expect(screen.getByTestId("search-type-symbols").getAttribute("aria-pressed")).toBe("true");
  });

  it("searches explicitly inside dependency folders only when requested", async () => {
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha" seed="auth" appStore={createAppStore()} onNavigate={vi.fn()} />);
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    expect(searchMock).toHaveBeenLastCalledWith("auth", "proj-a", expect.objectContaining({ includeDependencies: false }), expect.any(AbortSignal));
    fireEvent.click(await screen.findByTestId("search-filter"));
    fireEvent.click(await screen.findByTestId("search-include-dependencies"));
    await waitFor(() => expect(searchMock).toHaveBeenLastCalledWith("auth", "proj-a", expect.objectContaining({ includeDependencies: true }), expect.any(AbortSignal)));
  });
});