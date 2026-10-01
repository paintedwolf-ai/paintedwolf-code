import { afterEach, describe, expect, it } from "vitest";
import {
  armReplace,
  consumeReplaceArm,
  pendingReplaceArm,
  resetReplaceArmForTests,
} from "./replace-arm.ts";

afterEach(() => resetReplaceArmForTests());

describe("replace-arm", () => {
  it("peeks without consuming, consumes exactly once", () => {
    armReplace({
      originProjectId: "p1",
      query: "oldName",
      replacement: "newName",
      wholeWord: true,
      caseSensitive: true,
      renameFrom: "oldName",
    });
    expect(pendingReplaceArm()?.query).toBe("oldName");
    expect(pendingReplaceArm()?.renameFrom).toBe("oldName");
    const consumed = consumeReplaceArm();
    expect(consumed?.replacement).toBe("newName");
    expect(consumeReplaceArm()).toBeNull();
    expect(pendingReplaceArm()).toBeNull();
  });

  it("latest arm wins", () => {
    armReplace({ originProjectId: null, query: "a", replacement: "" });
    armReplace({ originProjectId: "p2", query: "b", replacement: "c" });
    expect(consumeReplaceArm()?.query).toBe("b");
  });
});
