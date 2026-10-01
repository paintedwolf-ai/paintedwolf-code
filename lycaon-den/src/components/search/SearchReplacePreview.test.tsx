import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { createSignal } from "solid-js";
import { afterEach, expect, it, vi } from "vitest";
import { SearchReplacePreview, type ReplaceApplySummary } from "./SearchReplacePreview.tsx";
import type { FileBatchReport } from "../../files/commands/file-mutations.ts";

afterEach(cleanup);

function summary(batchId: string): ReplaceApplySummary {
  return {
    batchId, appliedMatches: 1, appliedFiles: 1, skipped: [],
    applied: [{ root_id: "root", path: "a.txt" }], renameFrom: null, replacement: "new",
  };
}

it("keeps revert state with its batch, including late completions", async () => {
  let finish!: (report: FileBatchReport) => void;
  const revert = vi.fn(() => new Promise<FileBatchReport>((resolve) => { finish = resolve; }));
  const [current, setCurrent] = createSignal<ReplaceApplySummary | null>(summary("a"));
  render(() => <SearchReplacePreview
    preview={null} selection={null} onSelectionChange={() => {}}
    applying={false} previewCurrent={true} applySummary={current()}
    onApply={() => {}} onApplyFile={() => {}} onCancel={() => {}}
    onSearchAgain={() => {}} applyAllDisabled={false} onRevertBatch={revert}
  />);
  fireEvent.click(screen.getByRole("button", { name: "Revert" }));
  await waitFor(() => expect(revert).toHaveBeenCalledTimes(1));
  setCurrent(null);
  setCurrent(summary("b"));
  expect(screen.getByRole("button", { name: "Revert" }).hasAttribute("disabled")).toBe(false);
  finish({ ok: 1, skipped: [], undos: [] });
  await Promise.resolve();
  expect(screen.queryByTestId("search-replace-revert-report")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Revert" }));
  await waitFor(() => expect(revert).toHaveBeenCalledTimes(2));
  finish({ ok: 1, skipped: [], undos: [] });
  await waitFor(() => expect(screen.getByTestId("search-replace-revert-report").textContent).toContain("Reverted 1 file"));
  setCurrent(null);
  setCurrent(summary("c"));
  expect(screen.queryByTestId("search-replace-revert-report")).toBeNull();
  expect(screen.getByRole("button", { name: "Revert" }).hasAttribute("disabled")).toBe(false);
});

it.each([
  [0, "0 matches", "0 files"],
  [1, "1 match", "1 file"],
  [2, "2 matches", "2 files"],
])("reports replacement counts for %i", (count, matches, files) => {
  render(() => <SearchReplacePreview
    preview={null} selection={null} onSelectionChange={() => {}}
    applying={false} previewCurrent={true}
    applySummary={{ ...summary("counts"), appliedMatches: count, appliedFiles: count }}
    onApply={() => {}} onApplyFile={() => {}} onCancel={() => {}}
    onSearchAgain={() => {}} applyAllDisabled={false}
  />);
  expect(screen.getByTestId("search-replace-summary").textContent).toContain(`Replaced ${matches} in ${files}.`);
});
