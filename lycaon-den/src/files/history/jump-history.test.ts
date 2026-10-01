import { describe, expect, it } from "vitest";
import {
  JUMP_HISTORY_CAP,
  createJumpHistory,
  jumpBack,
  jumpForward,
  pushJump,
  retargetJumpHistory,
  type JumpEntry,
} from "./jump-history.ts";

function entry(bufferKey: string, line: number): JumpEntry {
  return { bufferKey, rootId: "root", path: `${bufferKey}.ts`, line };
}

describe("jump-history", () => {
  it("citation → quick-open → back returns to citation line; forward works", () => {
    let hist = createJumpHistory();
    hist = pushJump(hist, entry("a", 10), 1_000);
    hist = pushJump(hist, entry("b", 20), 2_000);
    const back = jumpBack(hist);
    expect(back.entry).toEqual(entry("a", 10));
    hist = back.hist;
    const fwd = jumpForward(hist);
    expect(fwd.entry).toEqual(entry("b", 20));
  });

  it("caps at 100", () => {
    let hist = createJumpHistory();
    for (let i = 1; i <= JUMP_HISTORY_CAP + 20; i++) {
      hist = pushJump(hist, entry("k", i), i * 1_000);
    }
    expect(hist.back.length).toBe(JUMP_HISTORY_CAP);
    expect(hist.back[0]?.line).toBe(21);
  });

  it("coalesces rapid same-buffer pushes", () => {
    let hist = createJumpHistory();
    hist = pushJump(hist, entry("a", 1), 1_000);
    hist = pushJump(hist, entry("a", 5), 1_100);
    hist = pushJump(hist, entry("a", 12), 1_200);
    expect(hist.back).toEqual([entry("a", 12)]);
  });

  it("go-to-definition across files: back reaches the origin, forward reaches the target", () => {
    let hist = createJumpHistory();
    hist = pushJump(hist, entry("A", 1), 0);
    hist = pushJump(hist, entry("A", 10), 2_000);
    hist = pushJump(hist, entry("B", 40), 2_000);

    const back = jumpBack(hist);
    expect(back.entry).toEqual(entry("A", 10));
    hist = back.hist;

    const fwd = jumpForward(hist);
    expect(fwd.entry).toEqual(entry("B", 40));
  });

  it("retargets keys after rename", () => {
    let hist = createJumpHistory();
    hist = pushJump(hist, entry("old", 3), 1_000);
    hist = pushJump(hist, entry("keep", 1), 2_000);
    hist = retargetJumpHistory(hist, (entry) => ({
      ...entry,
      bufferKey: entry.bufferKey === "old"
        ? "new"
        : entry.bufferKey,
    }));
    expect(hist.back.map((e) => e.bufferKey)).toEqual([
      "new",
      "keep",
    ]);
  });
});
