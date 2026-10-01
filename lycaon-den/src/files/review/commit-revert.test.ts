import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import type { SourceComparison } from "../../api/types.ts";
import { stubClient } from "../../test/client-fixture.ts";
import { planFileRevert } from "../history/file-revert.ts";

const target = {
  fileId: "", rootId: "r", path: "file.ts", baseline: "commit",
  comparisonTarget: { rootId: "r", path: "file.ts", baseline: "commit" as const, expectedHead: "head" },
};

function comparison(absent = false): SourceComparison {
  return {
    truncated: false,
    in_range: true, location_changed: false,
    before: { state: "content", availability: "available", content: "committed", size_bytes: 9 },
    after: absent
      ? { state: "absent", availability: "absent", content: "", size_bytes: 0 }
      : { state: "content", availability: "available", content: "working", sha256: "working-sha", size_bytes: 7 },
  };
}

describe("restore working file from commit", () => {
  it("does not create an empty file when both HEAD and the working path are absent", async () => {
    const diff = comparison(true);
    diff.before = { state: "absent", availability: "absent", content: "", size_bytes: 0 };
    const client = stubClient({ getProjectSourceComparison: async () => diff });
    expect(await planFileRevert(client, "p", target)).toEqual({ ok: false, conflict: false, error: "The working file already matches this baseline." });
  });

  it("uses the pinned path comparison without requiring a recorded identity", async () => {
    const compare = vi.fn(async () => comparison(true));
    const plan = await planFileRevert(stubClient({ getProjectSourceComparison: compare }), "p", target);
    expect(compare).toHaveBeenCalledWith("p", target.comparisonTarget, expect.anything());
    expect(plan).toMatchObject({ ok: true, member: { kind: "create", rootId: "r", path: "file.ts", content: "committed" } });
  });

  it("asks for refresh after HEAD moves", async () => {
    const client = stubClient({ getProjectSourceComparison: async () => { throw new LycaonApiError("HEAD changed", 409, "source_history_changed"); } });
    expect(await planFileRevert(client, "p", target)).toEqual({ ok: false, conflict: true });
  });

  it("refuses working bytes that changed after comparison", async () => {
    const client = stubClient({ getProjectSourceComparison: async () => comparison(),
      getProjectSource: async () => ({ content: "newer", sha256: "newer-sha", encoding: "utf-8" }) });
    expect(await planFileRevert(client, "p", target)).toEqual({ ok: false, conflict: true });
  });

  it("refuses a file recreated after the deleted row was shown", async () => {
    const client = stubClient({ getProjectSourceComparison: async () => comparison() });
    expect(await planFileRevert(client, "p", { ...target, tip: { state: "absent" } })).toEqual({ ok: false, conflict: true });
  });
});
