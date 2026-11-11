import { describe, expect, it } from "vitest";
import type { SourceStepBriefingTarget } from "../../api/types.ts";
import { WalkBriefingTargets } from "./walk-briefing-targets.ts";

const target = (key: string, fingerprint: string): SourceStepBriefingTarget => ({
  kind: "command", target_key: key, scope_fingerprint: fingerprint, scope: { kind: "command", root_ids: ["r"] },
});

describe("WalkBriefingTargets", () => {
  it("keeps targets every page agrees on", () => {
    const targets = new WalkBriefingTargets();
    targets.add({ "command:a": target("command:a", "f1") });
    targets.add({ "command:a": target("command:a", "f1"), "e1": target("e1", "f2") });
    expect([...targets.resolved().keys()].sort()).toEqual(["command:a", "e1"]);
  });

  it("drops a group two pages list differently instead of joining them", () => {
    const targets = new WalkBriefingTargets();
    targets.add({ "command:a": target("command:a", "first-page") });
    targets.add({ "command:a": target("command:a", "second-page") });
    targets.add({ "command:a": target("command:a", "first-page") });
    expect(targets.resolved().has("command:a")).toBe(false);
  });
});
