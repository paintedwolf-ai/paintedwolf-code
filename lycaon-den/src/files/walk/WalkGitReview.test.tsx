import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createNoticeStore, registerNoticePublisher } from "../../notices/notice-store.ts";
import { selectProjectNoticeGroups } from "../../notices/notice-select.ts";
import { createSignal } from "solid-js";
import type { LycaonClient } from "../../api/client.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { WalkGitReview } from "./WalkGitReview.tsx";
import { gitReview, gitReviewComparison, gitReviewFile, gitReviewStep } from "../review/git-review-fixtures.ts";
import type { SourceComparison, SourceGitReview } from "../../api/types.ts";
import type { GitReviewSelection } from "../documents/files-buffer-state.ts";

let notices = createNoticeStore();
const noticeRows = () => selectProjectNoticeGroups(notices.index()).find((group) => group.projectId === "p1")?.notices ?? [];
beforeEach(() => {
  notices = createNoticeStore();
  registerNoticePublisher(notices);
});
afterEach(() => {
  cleanup();
  registerNoticePublisher(null);
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

describe("Git walk review", () => {
  it("lists committed files when the movement recorded no working-tree effects", async () => {
    const open = vi.fn();
    const compare = vi.fn(async () => gitReviewComparison());
    const host = sourceReaderFixture(compare);
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} client={stubClient({ getProjectSourceGitReview: async () => gitReview(), ...host.methods })} onOpenGitFile={open} onOpenFile={vi.fn()} />);
    await screen.findByRole("button", { name: "Review src/app.ts" });
    expect(screen.getByTestId("git-review-count").textContent).toContain("1 file committed");
    expect(screen.getByText("Test author")).toBeTruthy();
    expect(screen.queryByText("No file changes")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Review src/app.ts" }));
    fireEvent.click(screen.getByRole("button", { name: "Review src/app.ts" }));
    await waitFor(() => expect(open).toHaveBeenCalledOnce());
    expect(compare).toHaveBeenCalledOnce();
    expect(compare).toHaveBeenCalledWith("p1", expect.objectContaining({ kind: "git_change", change_id: "commit-change", path: "src/app.ts" }), undefined);
    expect(open.mock.calls[0]![3].after).not.toHaveProperty("content");
    expect(open.mock.calls[0]![3].reference.view.comparison.summary.after.lines).toBe(1);
  });

  it("loads every page without turning the first page into the total", async () => {
    const list = vi.fn().mockResolvedValueOnce(gitReview([gitReviewFile("a.ts")], { files_total: 2, next_cursor: "1" }))
      .mockRejectedValueOnce(new Error("Temporary failure"))
      .mockResolvedValueOnce(gitReview([gitReviewFile("b.ts")], { files_total: 2 }));
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} client={stubClient({ getProjectSourceGitReview: list })} onOpenGitFile={vi.fn()} onOpenFile={vi.fn()} />);
    const more = await screen.findByRole("button", { name: "Load more files (1 of 2)" });
    expect(screen.getByTestId("git-review-count").textContent).toContain("2 files committed");
    fireEvent.click(more);
    await waitFor(() => expect(noticeRows()).toMatchObject([{ code: "files_git_review_page_failed", message: "Temporary failure" }]));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getByRole("button", { name: "Review a.ts" })).toBeTruthy();
    fireEvent.click(more);
    await screen.findByRole("button", { name: "Review b.ts" });
    expect(screen.getAllByTestId("git-review-file")).toHaveLength(2);
    expect(list.mock.calls[2]![2].cursor).toBe("1");
  });

  it("reports a failed review as a notice and never claims it is empty", async () => {
    const list = vi.fn().mockRejectedValue(new Error("Object unavailable"));
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} client={stubClient({ getProjectSourceGitReview: list })} onOpenGitFile={vi.fn()} onOpenFile={vi.fn()} />);
    await waitFor(() => expect(noticeRows()).toMatchObject([{ code: "files_git_review_unavailable", message: "Object unavailable" }]));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByTestId("git-review-count")).toBeNull();
    expect(screen.queryByText("This commit contains no file changes in this folder.")).toBeNull();
    expect(screen.queryByRole("button", { name: "Reload review" })).toBeNull();
  });

  it("lists a genuinely empty review", async () => {
    const list = vi.fn().mockResolvedValue(gitReview([], { insertions: 0, deletions: 0 }));
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} client={stubClient({ getProjectSourceGitReview: list })} onOpenGitFile={vi.fn()} onOpenFile={vi.fn()} />);
    await screen.findByText("This commit contains no file changes in this folder.");
    expect(noticeRows()).toEqual([]);
  });

  it("recovers an unavailable movement by reviewing the destination commit", async () => {
    const selection = vi.fn();
    const list = vi.fn<LycaonClient["getProjectSourceGitReview"]>(async (_project, _change, opts) => {
      if (opts?.movement !== false) throw new Error("The previous object is unavailable");
      return gitReview();
    });
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} selection={{ movement: true }} onSelectionChange={selection} client={stubClient({ getProjectSourceGitReview: list })} onOpenGitFile={vi.fn()} onOpenFile={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: "Review destination commit" }));
    await screen.findByRole("button", { name: "Review src/app.ts" });
    expect(list).toHaveBeenCalledTimes(2);
    expect(selection).toHaveBeenCalledWith({ movement: false });
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByRole("button", { name: "Review destination commit" })).toBeNull();
  });

  it("selects merge parents and cancels a pending file open when the comparison changes", async () => {
    const pending = deferred<SourceComparison>();
    const open = vi.fn();
    const first = gitReview();
    first.commit.parents.push("e".repeat(40));
    const list = vi.fn<LycaonClient["getProjectSourceGitReview"]>(async (_project, _change, opts) => ({ ...first,
      commit_comparison: !opts?.movement, before_commit: opts?.parent === 2 ? "e".repeat(40) : "a".repeat(40) }));
    const compare = vi.fn(() => pending.promise);
    const host = sourceReaderFixture(compare);
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} client={stubClient({ getProjectSourceGitReview: list, ...host.methods })} onOpenGitFile={open} onOpenFile={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", { name: "Review src/app.ts" }));
    fireEvent.click(screen.getByRole("button", { name: "Comparison parent" }));
    fireEvent.click(await screen.findByRole("option", { name: /^2 · / }));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    pending.resolve(gitReviewComparison());
    await Promise.resolve();
    expect(open).not.toHaveBeenCalled();
    expect(compare).toHaveBeenCalledOnce();
    await waitFor(() => expect(screen.getByTestId("git-review-basis").textContent).toContain("eeeeeeeeeeee"));
  });

  it("restores a comparison when returning to a resident group tab", async () => {
    const [selection, setSelection] = createSignal<GitReviewSelection>({ movement: false, parent: 2 });
    const first = gitReview();
    first.commit.parents.push("e".repeat(40));
    const list = vi.fn<LycaonClient["getProjectSourceGitReview"]>(async (_project, _change, opts) => ({
      ...first, before_commit: opts?.parent === 2 ? "e".repeat(40) : "a".repeat(40),
    }));
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} selection={selection()} client={stubClient({ getProjectSourceGitReview: list })} onOpenGitFile={vi.fn()} onOpenFile={vi.fn()} />);
    await waitFor(() => expect(screen.getByTestId("git-review-basis").textContent).toContain("eeeeeeeeeeee"));
    setSelection({ movement: false, parent: 1 });
    await waitFor(() => expect(screen.getByTestId("git-review-basis").textContent).toContain("aaaaaaaaaaaa"));
    expect(list).toHaveBeenCalledTimes(2);
    expect(screen.getByRole("button", { name: "Comparison parent" }).textContent).toContain("1 · ");
  });

  it("retains the published review and its DOM while a new comparison prepares", async () => {
    const pending = deferred<SourceGitReview>();
    const first = gitReview();
    first.commit.parents.push("e".repeat(40));
    const list = vi.fn().mockResolvedValueOnce(first).mockImplementationOnce(() => pending.promise);
    const client = stubClient({ getProjectSourceGitReview: list });
    render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} client={client} onOpenGitFile={vi.fn()} onOpenFile={vi.fn()} />);
    const row = await screen.findByRole("button", { name: "Review src/app.ts" });
    const title = screen.getByTestId("walk-page-title");
    fireEvent.click(screen.getByRole("button", { name: "Comparison parent" }));
    fireEvent.click(await screen.findByRole("option", { name: /^2 · / }));
    await waitFor(() => expect(list).toHaveBeenCalledTimes(2));
    expect(screen.getByRole("button", { name: "Review src/app.ts" })).toBe(row);
    expect(screen.getByTestId("walk-page-title")).toBe(title);
    expect(screen.getByTestId("git-review-basis").textContent).toContain("aaaaaaaaaaaa");
    expect(screen.queryByText("Loading committed files…")).toBeNull();
    pending.resolve({ ...first, before_commit: "e".repeat(40) });
    await waitFor(() => expect(screen.getByTestId("git-review-basis").textContent).toContain("eeeeeeeeeeee"));
    expect(screen.getByTestId("walk-page-title")).toBe(title);
  });

  it("ignores a stale request after unmount", async () => {
    const pending = deferred<SourceGitReview>();
    const list = vi.fn<LycaonClient["getProjectSourceGitReview"]>(() => pending.promise);
    const view = render(() => <WalkGitReview projectId="p1" step={gitReviewStep()} client={stubClient({ getProjectSourceGitReview: list })} onOpenGitFile={vi.fn()} onOpenFile={vi.fn()} />);
    view.unmount();
    pending.resolve(gitReview());
    await Promise.resolve();
    expect(screen.queryByTestId("walk-git-review")).toBeNull();
  });
});
