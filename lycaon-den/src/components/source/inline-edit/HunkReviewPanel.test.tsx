import { stubClient } from "../../../test/client-fixture.ts";
import { cleanup, fireEvent, render, screen, waitFor } from "@solidjs/testing-library";
import { afterEach, describe, expect, it, vi } from "vitest";
import { HunkReviewPanel, type HunkReviewTarget } from "./HunkReviewPanel.tsx";

const target: HunkReviewTarget = {
  projectId: "project-1",
  sessionId: "session-1",
  rootId: "root-1",
  path: "src/file.ts",
  before: "one\ntwo\nthree",
  after: "ONE\ntwo\nTHREE",
  headSha256: "head-1",
  encoding: "utf-8",
};

afterEach(cleanup);

describe("HunkReviewPanel", () => {
  it("accepts individual hunks and closes after the final decision", () => {
    const onClose = vi.fn();
    const replaceProjectSource = vi.fn();
    render(() => (
      <HunkReviewPanel
        client={stubClient({ replaceProjectSource })}
        target={target}
        onClose={onClose}
      />
    ));

    expect(screen.getAllByTestId("hunk-review-row")).toHaveLength(2);
    fireEvent.click(screen.getAllByTestId("hunk-review-accept")[0]!);
    expect(screen.getAllByTestId("hunk-review-row")).toHaveLength(1);
    expect(replaceProjectSource).not.toHaveBeenCalled();
    fireEvent.click(screen.getByTestId("hunk-review-accept"));
    expect(onClose).toHaveBeenCalledOnce();
  });

  it("chains SHA-guarded rejections without adding a final newline", async () => {
    const shiftingTarget = {
      ...target,
      before: "zero\none\ntwo\nthree",
      after: "ONE\ntwo\nTHREE",
    };
    const replaceProjectSource = vi.fn()
      .mockResolvedValueOnce({ sha256: "head-2" })
      .mockResolvedValueOnce({ sha256: "head-3" });
    const onClose = vi.fn();
    render(() => (
      <HunkReviewPanel
        client={stubClient({ replaceProjectSource })}
        target={shiftingTarget}
        onClose={onClose}
      />
    ));

    fireEvent.click(screen.getAllByTestId("hunk-review-reject")[0]!);
    await waitFor(() => {
      expect(replaceProjectSource).toHaveBeenCalledTimes(1);
      expect(screen.getAllByTestId("hunk-review-row")).toHaveLength(1);
    });
    expect(replaceProjectSource.mock.calls[0]![1]).toMatchObject({
      content: "zero\none\ntwo\nTHREE",
      base_sha256: "head-1",
    });

    fireEvent.click(screen.getByTestId("hunk-review-reject"));
    await waitFor(() => {
      expect(replaceProjectSource).toHaveBeenCalledTimes(2);
      expect(onClose).toHaveBeenCalledOnce();
    });
    expect(replaceProjectSource.mock.calls[1]![1]).toMatchObject({
      content: shiftingTarget.before,
      base_sha256: "head-2",
    });
  });
});
