import { sourceEffectFixture } from "../source/source-effect-fixture.ts";
import { describe, expect, it } from "vitest";
import { buildWalk } from "./walk-model.ts";
import { latestWalkTurnStart, walkChapterAt } from "./walk-chapters.ts";
import { walkEffectFixture, walkResponseFixture } from "./walk-fixtures.ts";

describe("Walk chapters", () => {
  it("groups by recorded turn identity while preserving source order", () => {
    const response = walkResponseFixture([
      walkEffectFixture("a", 2, 1), walkEffectFixture("b", 5, 2), walkEffectFixture("c", 2, 3),
    ]);
    const walk = buildWalk("session:s1", response.files, [], [], response.turns);
    expect(walk.steps.map((step) => step.key)).toEqual(["a", "b", "c"]);
    expect(walk.chapters.map((chapter) => chapter.stepKeys)).toEqual([["a", "c"], ["b"]]);
    expect(latestWalkTurnStart(walk)).toBe(1);
    expect(walkChapterAt(walk, 2)?.messageId).toBe("s1-user-2");
    expect(walkChapterAt(walk, 1)?.prompt).toBe("Make change 5");
  });

  it("never assigns unattributed changes to a chat turn", () => {
    const response = walkResponseFixture([{ ...walkEffectFixture("outside", 0, 1), session_id: undefined }]);
    const walk = buildWalk("commit", response.files);
    expect(walk.chapters[0]).toMatchObject({ title: "Source activity", sessionId: "", turn: 0 });
  });

  it("places an outside run inside the turn it was observed during", () => {
    const outside = (id: string, ordinal: number) => sourceEffectFixture({
      ...walkEffectFixture(id, 0, ordinal), contributors: undefined,
      origin: "external" as const, cause: "filesystem_reconcile", session_id: undefined,
    });
    const response = walkResponseFixture([
      walkEffectFixture("a", 2, 1), outside("o1", 2), outside("o2", 3), walkEffectFixture("b", 2, 4),
    ]);
    const walk = buildWalk("session:s1", response.files, [], [], response.turns);
    expect(walk.steps.map((step) => step.key)).toEqual(["a", "outside:o1", "b"]);
    expect(walk.chapters).toHaveLength(1);
    expect(walk.chapters[0]).toMatchObject({ title: "Turn 2", stepKeys: ["a", "outside:o1", "b"] });
  });

  it("keeps an outside run between two turns as source activity", () => {
    const outside = (id: string, ordinal: number) => sourceEffectFixture({
      ...walkEffectFixture(id, 0, ordinal), contributors: undefined,
      origin: "external" as const, cause: "filesystem_reconcile", session_id: undefined,
    });
    const response = walkResponseFixture([
      walkEffectFixture("a", 2, 1), outside("o1", 2), outside("o2", 3), walkEffectFixture("b", 3, 4),
    ]);
    const walk = buildWalk("session:s1", response.files, [], [], response.turns);
    expect(walk.chapters.map((chapter) => chapter.title)).toEqual(["Turn 2", "Source activity", "Turn 3"]);
  });

  it("keeps identical turn ordinals in different chats distinct", () => {
    const response = walkResponseFixture([walkEffectFixture("a", 2, 1), { ...walkEffectFixture("b", 2, 2), session_id: "s2" }]);
    const walk = buildWalk("commit", response.files);
    expect(walk.chapters).toHaveLength(2);
    expect(walk.chapters.map((chapter) => chapter.sessionId)).toEqual(["s1", "s2"]);
  });
});
