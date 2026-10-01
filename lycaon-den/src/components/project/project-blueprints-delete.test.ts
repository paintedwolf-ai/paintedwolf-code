import { describe, expect, it, vi } from "vitest";
import { LycaonApiError } from "../../api/http.ts";
import {
  blueprintDeleteSummary,
  deleteBlueprints,
  type BlueprintDeleteClient,
  type BlueprintDeleteOutcome,
  type BlueprintDeleteReport,
} from "./project-blueprints-delete.ts";

/** Client whose delete resolves unless the blueprint id maps to a throw. */
function clientRefusing(
  refusals: Record<string, unknown>,
): BlueprintDeleteClient & { attempted: string[] } {
  const attempted: string[] = [];
  return {
    attempted,
    deleteBlueprint: vi.fn(async (_projectId: string, blueprintId: string) => {
      const path = TARGETS.find((target) => target.id === blueprintId)?.path ?? blueprintId;
      attempted.push(path);
      const refusal = refusals[path];
      // Exercise normalization of arbitrary rejected values.
      // eslint-disable-next-line @typescript-eslint/only-throw-error
      if (refusal !== undefined) throw refusal;
    }),
  };
}

const PATHS = [
  ".paintedwolf/blueprints/a.md",
  ".paintedwolf/blueprints/b.md",
  ".paintedwolf/blueprints/c.md",
];

const TARGETS = PATHS.map((path, index) => ({
  id: `00000000-0000-4000-8000-00000000000${index + 1}`,
  path,
}));

function reportOf(
  outcomes: BlueprintDeleteOutcome[],
): BlueprintDeleteReport {
  const deleted: string[] = [];
  const failed: { path: string; reason: string }[] = [];
  for (const outcome of outcomes) {
    if (outcome.ok) deleted.push(outcome.path);
    else failed.push({ path: outcome.path, reason: outcome.reason });
  }
  return { outcomes, deleted, failed };
}

describe("deleteBlueprints", () => {
  it("attempts every path when one delete fails", async () => {
    const client = clientRefusing({
      [PATHS[1]!]: new LycaonApiError("Blueprint is read-only.", 500, "internal_error"),
    });
    const report = await deleteBlueprints(client, "proj-1", TARGETS);
    expect(client.attempted).toEqual(PATHS);
    expect(report.deleted).toEqual([PATHS[0], PATHS[2]]);
    expect(report.failed).toEqual([
      { path: PATHS[1], reason: "Blueprint is read-only." },
    ]);
    expect(report.outcomes).toHaveLength(3);
  });

  it("keeps going after a failure on the first path", async () => {
    const client = clientRefusing({
      [PATHS[0]!]: new Error("network down"),
    });
    const report = await deleteBlueprints(client, "proj-1", TARGETS);
    expect(client.attempted).toEqual(PATHS);
    expect(report.deleted).toEqual([PATHS[1], PATHS[2]]);
    expect(report.failed).toEqual([{ path: PATHS[0], reason: "network down" }]);
  });

  it("counts a blueprint the host no longer has as deleted", async () => {
    const client = clientRefusing({
      [PATHS[1]!]: new LycaonApiError(
        "blueprint not found",
        404,
        "blueprint_not_found",
      ),
    });
    const report = await deleteBlueprints(client, "proj-1", TARGETS);
    expect(report.failed).toEqual([]);
    expect(report.deleted).toEqual(PATHS);
  });

  it("settles outcomes in request order", async () => {
    const client = clientRefusing({
      [PATHS[2]!]: new LycaonApiError("Permission denied.", 500, "internal_error"),
    });
    const seen: string[] = [];
    await deleteBlueprints(client, "proj-1", TARGETS, (outcome) => {
      seen.push(`${outcome.path}:${outcome.ok ? "ok" : outcome.reason}`);
    });
    expect(seen).toEqual([
      `${PATHS[0]}:ok`,
      `${PATHS[1]}:ok`,
      `${PATHS[2]}:Permission denied.`,
    ]);
  });

  it("names a reason for every refusal shape", async () => {
    const client = clientRefusing({
      [PATHS[0]!]: new LycaonApiError("", 500, "internal_error", {
        title: "Disk is full",
      }),
      [PATHS[1]!]: new LycaonApiError("", 500, "internal_error"),
      [PATHS[2]!]: "not an error at all",
    });
    const report = await deleteBlueprints(client, "proj-1", TARGETS);
    expect(report.failed.map((f) => f.reason)).toEqual([
      "Disk is full",
      "The app answered 500.",
      "The delete did not go through.",
    ]);
  });

  it("makes no host call for an empty batch", async () => {
    const client = clientRefusing({});
    const report = await deleteBlueprints(client, "proj-1", []);
    expect(client.deleteBlueprint).not.toHaveBeenCalled();
    expect(report).toEqual({ outcomes: [], deleted: [], failed: [] });
  });
});

describe("blueprintDeleteSummary", () => {
  const label = (path: string) => path.replace(/^.*\//, "");

  it("stays quiet when every delete landed", () => {
    const report = reportOf([
      { path: PATHS[0]!, ok: true },
      { path: PATHS[1]!, ok: true },
    ]);
    expect(blueprintDeleteSummary(report, label)).toBeNull();
  });

  it("names the reason when one blueprint of a batch survives", () => {
    const report = reportOf([
      { path: PATHS[0]!, ok: true },
      { path: PATHS[1]!, ok: false, reason: "Permission denied." },
      { path: PATHS[2]!, ok: true },
    ]);
    expect(blueprintDeleteSummary(report, label)).toBe(
      "Deleted 2 of 3. “b.md” could not be deleted — Permission denied.",
    );
  });

  it("counts survivors and defers to the rows past one failure", () => {
    const report = reportOf([
      { path: PATHS[0]!, ok: true },
      { path: PATHS[1]!, ok: false, reason: "Permission denied." },
      { path: PATHS[2]!, ok: false, reason: "Disk is full." },
    ]);
    expect(blueprintDeleteSummary(report, label)).toBe(
      "Deleted 1 of 3. 2 could not be deleted — each row says why.",
    );
  });

  it("reports a lone delete that failed outright", () => {
    const report = reportOf([
      { path: PATHS[0]!, ok: false, reason: "Permission denied." },
    ]);
    expect(blueprintDeleteSummary(report, label)).toBe(
      "Could not delete “a.md” — Permission denied.",
    );
  });

  it("reports a batch where nothing landed", () => {
    const report = reportOf([
      { path: PATHS[0]!, ok: false, reason: "Permission denied." },
      { path: PATHS[1]!, ok: false, reason: "Disk is full." },
    ]);
    expect(blueprintDeleteSummary(report, label)).toBe(
      "Could not delete any of the 2 selected blueprints — each row says why.",
    );
  });
});
