import { describe, expect, it, vi } from "vitest";
import type { SourceCommandWindow, SourceWalkResponse } from "../../api/types.ts";
import { readReviewPages } from "./review-scope-pages.ts";

const gap: SourceCommandWindow = {
  id: "window", session_id: "chat", turn: 3, tool_name: "command",
  command_line: "cp note.txt output.txt", state: "ended", ordinal: 14,
  started_at: "2026-11-18T00:00:00Z",
  coverage: [{ root_id: "root", path: "", coverage: "not_observed", reason: "io_error" }],
};

function emptyPage(commands: SourceCommandWindow[], cursor?: string): SourceWalkResponse {
  return {
    baseline: "turn:chat,3", next_cursor: cursor, files: [], turns: [], git_changes: [],
    commands, commit_available: false,
  };
}

describe("review scope pages", () => {
  it("keeps one coverage notice for a window shared by pages with no file rows", async () => {
    const read = vi.fn()
      .mockResolvedValueOnce(emptyPage([gap], "next"))
      .mockResolvedValueOnce(emptyPage([gap]));
    const result = await readReviewPages(read, "turn:chat", () => true);
    expect(read.mock.calls).toEqual([["turn:chat", undefined], ["turn:chat,3", "next"]]);
    expect(result?.files).toEqual([]);
    expect(result?.notices).toEqual([expect.objectContaining({ key: "window:root:", text: expect.stringContaining("was not observed") })]);
  });

  it("drops a page from a superseded review request", async () => {
    const read = vi.fn().mockResolvedValue(emptyPage([gap], "next"));
    expect(await readReviewPages(read, "session:chat", () => false)).toBeUndefined();
    expect(read).toHaveBeenCalledTimes(1);
  });
});
