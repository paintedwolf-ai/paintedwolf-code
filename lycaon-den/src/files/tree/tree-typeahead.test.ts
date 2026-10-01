import { describe, expect, it, vi } from "vitest";
import type { SourceTreeFrame } from "../../api/types.ts";
import { findTreePrefix } from "./tree-typeahead.ts";

function frame(names: string[], offset: number): SourceTreeFrame {
  return { span: { start: offset, end: offset + names.length }, rows: names.map(name => ({ name, kind: "file" })) } as SourceTreeFrame;
}
describe("tree typeahead", () => {
  it("finds a name beyond the rendered page, then wraps", async () => {
    const read = vi.fn(async (offset: number, limit: number) => frame(
      Array.from({ length: limit }, (_, index) => offset + index === 225 ? "Target.ts" : "other.ts"), offset));
    const options = { prefix: "tar", from: 5, count: 400, signal: new AbortController().signal, frame: read };
    expect(await findTreePrefix(options)).toBe(225);
    expect(read).toHaveBeenCalledTimes(2);
    expect(await findTreePrefix({ ...options, from: 226 })).toBe(225);
    expect(read.mock.calls.every(([, limit]) => limit <= 200)).toBe(true);
  });
  it("does not publish a result after cancellation", async () => {
    const abort = new AbortController();
    await expect(findTreePrefix({ prefix: "a", from: 0, count: 1, signal: abort.signal,
      frame: async () => { abort.abort(); return frame(["a"], 0); },
    })).rejects.toMatchObject({ name: "AbortError" });
  });
});
