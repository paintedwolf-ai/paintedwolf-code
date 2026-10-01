import {
  setupGlobalSearchViewTests,
  searchMock,
  replacePreviewMock,
  replaceApplyMock,
  reportSearchErrorMock,
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

  it("shows replace preview selection and disables apply-all when truncated", async () => {
    replacePreviewMock.mockResolvedValue({
      state: "limited", issues: [{ executor: "code", reason: "result_limit" }], truncated: true,
      files: [
        {
          root_id: "r1",
          path: "a.ts",
          sha256: "aa",
          hunks: [
            { line: 1, before: "foo", after: "bar" },
            { line: 2, before: "foo2", after: "bar2" },
          ],
        },
      ],
    });
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="foo"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    await waitFor(() => expect(searchMock).toHaveBeenCalled());
    fireEvent.click(screen.getByTestId("search-toggle-replace"));
    await waitFor(() =>
      expect(screen.getByTestId("search-replace-preview")).toBeTruthy(),
    );
    await waitFor(() =>
      expect(screen.getByTestId("search-replace-truncated")).toBeTruthy(),
    );
    expect(
      (screen.getByTestId("search-replace-apply") as HTMLButtonElement).disabled,
    ).toBe(true);
    expect(screen.getByTestId("search-replace-file-apply")).toBeTruthy();
    const hunkChecks = screen.getAllByTestId("search-replace-hunk-check");
    fireEvent.click(hunkChecks[0]!);
    expect(
      (screen.getByTestId("search-replace-apply") as HTMLButtonElement).disabled,
    ).toBe(true);
  });

  it("waits for complete discovery before enabling replacement", async () => {
    vi.useFakeTimers();
    replacePreviewMock.mockResolvedValueOnce({ state: "preparing", files: [], truncated: false,
      issues: [{ executor: "code", reason: "catalog_incomplete", count: 1 }] });
    replacePreviewMock.mockResolvedValue({ state: "ready", issues: [], truncated: false,
      files: [{ root_id: "r1", path: "late.ts", sha256: "aa", hunks: [{ line: 1, before: "foo", after: "bar" }] }] });
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="foo" replaceModeRequested appStore={createAppStore()} onNavigate={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(300);
    expect(screen.getByRole("button", { name: "Cancel preparation" })).toBeTruthy();
    expect(screen.queryByTestId("search-replace-empty")).toBeNull();
    expect((screen.getByTestId("search-replace-apply") as HTMLButtonElement).disabled).toBe(true);
    await vi.advanceTimersByTimeAsync(750);
    expect(replacePreviewMock).toHaveBeenCalledTimes(2);
    expect(screen.getByText("late.ts")).toBeTruthy();
    expect((screen.getByTestId("search-replace-apply") as HTMLButtonElement).disabled).toBe(false);
    expect(screen.queryByRole("button", { name: "Cancel preparation" })).toBeNull();
  });

  it("cancels pending preparation and rejects its late response until explicitly retried", async () => {
    vi.useFakeTimers();
    let finish!: (value: unknown) => void;
    replacePreviewMock.mockImplementationOnce(() => new Promise(resolve => { finish = resolve; }));
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="foo" replaceModeRequested appStore={createAppStore()} onNavigate={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(300);
    const signal = replacePreviewMock.mock.calls[0]?.[1];
    fireEvent.click(screen.getByRole("button", { name: "Cancel preparation" }));
    expect(signal).toMatchObject({ aborted: true });
    finish({ state: "preparing", files: [], truncated: false,
      issues: [{ executor: "code", reason: "catalog_warming" }] });
    await vi.advanceTimersByTimeAsync(10000);
    expect(replacePreviewMock).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Preview preparation cancelled.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry preview" }));
    await vi.advanceTimersByTimeAsync(0);
    expect(replacePreviewMock).toHaveBeenCalledTimes(2);
    expect(screen.getByTestId("search-replace-empty")).toBeTruthy();
  });

  it("offers retry after a preparation failure without continuing to poll", async () => {
    vi.useFakeTimers();
    replacePreviewMock.mockRejectedValueOnce(new Error("offline"));
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="foo" replaceModeRequested appStore={createAppStore()} onNavigate={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(10000);
    expect(replacePreviewMock).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Retry preview" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Cancel preparation" })).toBeNull();
    expect(reportSearchErrorMock).toHaveBeenCalledTimes(1);
  });

  it("allows explicit files but disables apply-all when discovery finished with gaps", async () => {
    vi.useFakeTimers();
    replacePreviewMock.mockResolvedValue({ state: "limited", truncated: false,
      issues: [{ executor: "code", reason: "catalog_bounded", count: 1 }],
      files: [{ root_id: "r1", path: "known.ts", sha256: "aa", hunks: [{ line: 1, before: "foo", after: "bar" }] }] });
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="foo" replaceModeRequested appStore={createAppStore()} onNavigate={vi.fn()} />);
    await vi.advanceTimersByTimeAsync(10000);
    expect(replacePreviewMock).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("search-replace-coverage").textContent).toContain("indexing budget");
    expect((screen.getByTestId("search-replace-apply") as HTMLButtonElement).disabled).toBe(true);
    const fileApply = screen.getByTestId("search-replace-file-apply") as HTMLButtonElement;
    expect(fileApply.disabled).toBe(false);
    fireEvent.click(fileApply);
    await vi.advanceTimersByTimeAsync(0);
    expect(replaceApplyMock).toHaveBeenCalledTimes(1);
    expect(replaceApplyMock.mock.calls[0]?.[0].files).toMatchObject([{ root_id: "r1", path: "known.ts", sha256: "aa" }]);
  });

  it("disables replacement immediately when inputs differ from the reviewed preview", async () => {
    const preview = {
      state: "ready", issues: [], truncated: false,
      files: [{ root_id: "r1", path: "a.ts", sha256: "aa", hunks: [
        { line: 1, end_line: 1, before: "foo", after: "bar", context: "code" },
      ] }],
    };
    replacePreviewMock.mockResolvedValue(preview);
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="foo" replaceModeRequested appStore={createAppStore()} onNavigate={vi.fn()} />);
    const apply = () => screen.getByTestId("search-replace-apply") as HTMLButtonElement;
    await waitFor(() => expect(apply().disabled).toBe(false));
    fireEvent.input(screen.getByTestId("search-replace-input"), { target: { value: "unreviewed" } });
    expect(apply().disabled).toBe(true);
    fireEvent.click(apply());
    expect(replaceApplyMock).not.toHaveBeenCalled();
    await waitFor(() => expect(replacePreviewMock).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(apply().disabled).toBe(false));
    fireEvent.click(apply());
    await waitFor(() => expect(replaceApplyMock).toHaveBeenCalledTimes(1));
    expect(replaceApplyMock.mock.calls[0]?.[0]).toMatchObject({ query: "foo", replacement: "unreviewed" });
  });

  it("rejects an old preview arriving during the next request's debounce", async () => {
    let resolveOld!: (value: unknown) => void;
    const preview = { state: "ready", issues: [], truncated: false, files: [{ root_id: "r1", path: "a.ts", sha256: "aa", hunks: [
      { line: 1, end_line: 1, before: "foo", after: "bar", context: "code" },
    ] }] };
    replacePreviewMock.mockImplementationOnce(() => new Promise((resolve) => { resolveOld = resolve; }));
    replacePreviewMock.mockResolvedValue(preview);
    render(() => <GlobalSearchView projects={[]} originProjectId="proj-a" originName="Alpha"
      seed="foo" replaceModeRequested appStore={createAppStore()} onNavigate={vi.fn()} />);
    await waitFor(() => expect(replacePreviewMock).toHaveBeenCalledTimes(1));
    fireEvent.input(screen.getByTestId("search-replace-input"), { target: { value: "new" } });
    resolveOld(preview);
    await Promise.resolve();
    await Promise.resolve();
    expect((screen.getByTestId("search-replace-apply") as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryAllByTestId("search-replace-hunk")).toHaveLength(0);
    await waitFor(() => expect(replacePreviewMock).toHaveBeenCalledTimes(2));
    await waitFor(() => expect((screen.getByTestId("search-replace-apply") as HTMLButtonElement).disabled).toBe(false));
  });

  it("renders skipped-files summary after apply", async () => {
    replacePreviewMock.mockResolvedValue({
      state: "ready", issues: [], truncated: false,
      files: [
        {
          root_id: "r1",
          path: "a.ts",
          sha256: "aa",
          hunks: [{ line: 1, before: "foo", after: "bar" }],
        },
      ],
    });
    replaceApplyMock.mockResolvedValue({
      files: [
        {
          root_id: "r1",
          path: "a.ts",
          applied: false,
          skipped: true,
          reason: "source_write_conflict",
        },
        {
          root_id: "r1",
          path: "b.ts",
          applied: true,
          skipped: false,
          matches: 1,
        },
      ],
    });
    render(() => (
      <GlobalSearchView
        projects={[]}
        originProjectId="proj-a"
        originName="Alpha"
        seed="foo"
        appStore={createAppStore()}
        onNavigate={vi.fn()}
      />
    ));
    fireEvent.click(screen.getByTestId("search-toggle-replace"));
    await waitFor(() =>
      expect(replacePreviewMock).toHaveBeenCalled(),
    );
    await waitFor(() =>
      expect(
        (screen.getByTestId("search-replace-apply") as HTMLButtonElement)
          .disabled,
      ).toBe(false),
    );
    fireEvent.click(screen.getByTestId("search-replace-apply"));
    await waitFor(() => expect(replaceApplyMock).toHaveBeenCalled());
    await waitFor(() =>
      expect(screen.getByTestId("search-replace-summary").textContent).toContain(
        "Replaced 1 match in 1 file",
      ),
    );
    expect(screen.getByTestId("search-replace-skipped").textContent).toContain(
      "file skipped",
    );
    expect(screen.getByTestId("search-replace-again")).toBeTruthy();
  });
});
