import { stubClient } from "../test/client-fixture.ts";
import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../api/http.ts";
import { revertReplaceBatch } from "./replace-revert.ts";

function fixture(sha256 = "after") {
  const methods = {
    getProjectSource: vi.fn(async () => ({
      file_id: "file-a", content: "replacement", sha256, encoding: "utf-8",
      binary: false, over_limit: false,
    })),
    listProjectSourceVersions: vi.fn(async () => ({
      versions: [{ batch_id: "batch", effect_id: "effect-a" }], next_cursor: undefined as string | undefined,
    })),
    getProjectSourceComparison: vi.fn(async () => ({
      in_range: true,
      before: { availability: "available", content: "original", sha256: "before" },
      after: { availability: "available", content: "replacement", sha256: "after" },
    })),
    replaceProjectSource: vi.fn(async () => ({ sha256: "before" })),
  };
  return { methods, client: stubClient(methods) };
}

const targets = [{ root_id: "r", path: "a.txt" }];

describe("revertReplaceBatch", () => {
  it("restores the before-image guarded by the immutable batch after-image", async () => {
    const { client, methods } = fixture();
    const report = await revertReplaceBatch(client, "p", "batch", targets);
    expect(report.ok).toBe(1);
    expect(methods.replaceProjectSource).toHaveBeenCalledWith("p", expect.objectContaining({
      content: "original", base_sha256: "after", path: "a.txt", root_id: "r",
    }), undefined);
    expect(report.undos[0]).toMatchObject({ content: "replacement", baseSha256: "before" });
  });

  it("never overwrites edits saved after the replacement", async () => {
    const { client, methods } = fixture("newer-edits");
    const report = await revertReplaceBatch(client, "p", "batch", targets);
    expect(report).toMatchObject({ ok: 0, skipped: [{ path: "a.txt", reason: "changed since replacement" }] });
    expect(methods.replaceProjectSource).not.toHaveBeenCalled();
  });

  it("finds retained batch history across pages independent of presentation watermarks", async () => {
    const { client, methods } = fixture();
    methods.listProjectSourceVersions.mockResolvedValueOnce({ versions: [], next_cursor: "retained-page-2" });
    const report = await revertReplaceBatch(client, "p", "batch", targets);
    expect(report.ok).toBe(1);
    expect(methods.listProjectSourceVersions).toHaveBeenNthCalledWith(2, "p", { fileId: "file-a" }, {
      lane: "retained", limit: 100, cursor: "retained-page-2",
    });
  });

  it("reports unavailable history and continues with other files", async () => {
    const { client, methods } = fixture();
    methods.listProjectSourceVersions.mockRejectedValueOnce(new Error("History unavailable"));
    const report = await revertReplaceBatch(client, "p", "batch", [
      ...targets, { root_id: "r", path: "b.txt" },
    ]);
    expect(report.ok).toBe(1);
    expect(report.skipped).toEqual([{ path: "a.txt", reason: "History unavailable" }]);
  });

  it("keeps the compare-and-swap guard through the actual write", async () => {
    const { client, methods } = fixture();
    methods.replaceProjectSource.mockRejectedValueOnce(new LycaonApiError("Changed", 409, "source_write_conflict"));
    const report = await revertReplaceBatch(client, "p", "batch", targets);
    expect(report.ok).toBe(0);
    expect(report.skipped).toEqual([{ path: "a.txt", reason: "changed since" }]);
  });
});
