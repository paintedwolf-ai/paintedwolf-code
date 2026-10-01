import type { ComparisonSnapshot } from "../../api/source-reader.ts";
import { sourceReaderFixture } from "../../test/source-reader-fixture.ts";
import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { describe, expect, it } from "vitest";
import type { SourceWalkEffect } from "../../api/types.ts";
import {
  buildReviewWalkDetails,
  reviewWalkFileProgress,
} from "./review-walk-details.ts";
import type { WalkEffectStep, WalkGitStep } from "../walk/walk-model.ts";

function effectOf(
  id: string,
  path: string,
  overrides: Partial<SourceWalkEffect> = {},
): SourceWalkEffect {
  return sourceEffectFixture({
    id,
    project_id: "project",
    operation_id: `operation-${id}`,
    file_id: `file-${path}`,
    after_version_id: `version-${id}`,
    workspace_kind: "project",
    root_id: "root",
    path,
    op: "write",
    entry_kind: "file",
    origin: "agent",
    turn: 1,
    ordinal: 1,
    cause: "tool",
    capture_quality: "exact",
    observed_at: "2026-08-25T12:00:00Z",
    ...overrides,
  });
}

function step(
  id: string,
  path: string,
  overrides: Partial<SourceWalkEffect> = {},
): WalkEffectStep {
  const effect = effectOf(id, path, overrides);
  return {
    kind: "effect",
    key: effect.id,
    ordinal: effect.ordinal,
    label: "edit",
    toolCallId: null,
    effect,
  };
}

function comparison(before: string, after: string): ComparisonSnapshot {
  return sourceReaderFixture().comparison({
    in_range: true,
    truncated: false,
    before: {
      state: "content",
      size_bytes: before.length,
      availability: "available",
      content: before,
    },
    after: {
      state: "content",
      size_bytes: after.length,
      availability: "available",
      content: after,
    },
    location_changed: false,
  });
}

describe("review walk details", () => {
  it("summarizes line impact and changed areas", () => {
    const details = buildReviewWalkDetails(
      effectOf("one", "src/a.ts"),
      comparison("one\n", "one\ntwo\n"),
    );

    expect(details.summary).toBe("Added 1 line across 1 area.");
    expect(details).toMatchObject({
      added: 1,
      removed: 0,
      beforeLines: 1,
      afterLines: 2,
      remainingAreas: 0,
    });
    expect(details.areas).toEqual([
      { label: "Line 2", added: 1, removed: 0 },
    ]);
  });

  it("describes a content-preserving rename", () => {
    const details = buildReviewWalkDetails(
      effectOf("rename", "src/new.ts", {
        op: "rename",
        from_path: "src/old.ts",
      }),
      comparison("same", "same"),
    );

    expect(details.summary).toBe(
      "Renamed this file without changing its contents.",
    );
    expect(details.areas).toEqual([]);
  });

  it("summarizes a created file", () => {
    const created = comparison("", "one");
    created.before = {
      state: "absent",
      size_bytes: 0,
      availability: "absent",
    };

    const details = buildReviewWalkDetails(
      effectOf("create", "src/new.ts", { op: "create" }),
      created,
    );

    expect(details.summary).toBe("Created a file with 1 line.");
  });

  it("explains why line details are absent", () => {
    const unavailable: ComparisonSnapshot = {
      ...comparison("", ""),
      after: {
        state: "content",
        size_bytes: 20,
        availability: "binary",
        },
    };

    const details = buildReviewWalkDetails(
      effectOf("binary", "image.png"),
      unavailable,
    );

    expect(details.summary).toBe("Modified this file.");
    expect(details.availabilityNote).toBe("This step changes a binary file.");
  });

  it("locates the step within the current file", () => {
    const first = step("one", "src/a.ts");
    const second = step("two", "src/a.ts", { ordinal: 2 });
    const other = step("three", "src/b.ts", { ordinal: 3 });

    expect(reviewWalkFileProgress(second, [first, second, other])).toEqual({
      position: 2,
      count: 2,
    });
  });

  it("omits file progress without durable identity", () => {
    const current = step("one", "src/a.ts", { file_id: "" });

    expect(reviewWalkFileProgress(current, [current])).toBeNull();
  });

  it("omits file progress for a git step, which spans files", () => {
    const rewritten = effectOf("cg1", "src/a.ts", { ordinal: 4 });
    const git: WalkGitStep = {
      kind: "git",
      key: "git:t1",
      ordinal: 3,
      label: "git checkout",
      toolCallId: null,
      change: {
      session_id: "", turn: 0, tool_call_id: "", tool_name: "",
        id: "t1",
        root_id: "root",
        kind: "checkout",
        ordinal: 3,
        observed_at: "2026-08-25T12:00:00Z",
      },
      effects: [rewritten],
    };

    expect(reviewWalkFileProgress(git, [step("one", "src/a.ts"), git]))
      .toBeNull();
    // The movement's effect still counts toward the file's step tally.
    expect(
      reviewWalkFileProgress(step("one", "src/a.ts"), [
        step("one", "src/a.ts"),
        git,
      ]),
    ).toEqual({ position: 1, count: 2 });
  });
});
