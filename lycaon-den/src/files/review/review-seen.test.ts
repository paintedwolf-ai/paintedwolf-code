import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { SourceSeenFile, SourceWalkEffect } from "../../api/types.ts";
import { buildSeenRows } from "./review-model.ts";
import {
  getSeenLimit,
  resetFilesStagePaneForTests,
  SEEN_PAGE,
  showMoreSeen,
  subscribeReviewScope,
} from "./review-pane.ts";

function effect(id: string, ordinal: number, over: Partial<SourceWalkEffect> = {}): SourceWalkEffect {
  return sourceEffectFixture({
    id,
    project_id: "p1",
    operation_id: `operation-${id}`,
    file_id: "file-a",
    after_version_id: `version-${id}`,
    workspace_kind: "project",
    root_id: "r1",
    path: "src/a.ts",
    entry_kind: "file",
    op: "write",
    origin: "agent",
    turn: 1,
    ordinal,
    cause: "tool",
    capture_quality: "exact",
    observed_at: `2026-09-11T10:0${ordinal}:00Z`,
    ...over,
  });
}

function seenFile(effects: SourceWalkEffect[]): SourceSeenFile {
  return {
    file_id: "file-a",
    root_id: "r1",
    path: "src/a.ts",
    tip: { state: "content", sha256: "tip" },
    seen_at: "2026-09-11T11:00:00Z",
    through_ordinal: 9,
    effects,
    effects_truncated: false,
  };
}

describe("seen rows", () => {
  it("read like any review row and name the look they came from", () => {
    const [row] = buildSeenRows([
      seenFile([effect("agent", 3), effect("mine", 2, { origin: "user" })]),
    ]);

    expect(row).toMatchObject({
      fileId: "file-a",
      basename: "a.ts",
      dirname: "src",
      op: "write",
      lastTs: "2026-09-11T10:03:00Z",
      attribution: "You + Agent",
      contributors: [{ kind: "user", sessionId: "", label: "" }, { kind: "agent", sessionId: "", label: "" }],
      unpresentedAgentEffects: 0,
      liveVerb: null,
      seenAt: "2026-09-11T11:00:00Z",
      throughOrdinal: 9,
    });
  });

  it("read as added when the look covered the file's creation", () => {
    const [row] = buildSeenRows([
      seenFile([effect("edit", 5), effect("create", 4, { op: "create" })]),
    ]);
    expect(row?.op).toBe("create");
  });
});

describe("seen page size", () => {
  beforeEach(() => resetFilesStagePaneForTests());

  it("grows one page at a time up to the host's cap", () => {
    const spy = vi.fn();
    const stop = subscribeReviewScope(spy);
    expect(getSeenLimit("p1")).toBe(SEEN_PAGE);

    for (let i = 0; i < 5; i += 1) showMoreSeen("p1");
    stop();

    expect(getSeenLimit("p1")).toBe(100);
    expect(spy).toHaveBeenCalledTimes(3);
  });
});
