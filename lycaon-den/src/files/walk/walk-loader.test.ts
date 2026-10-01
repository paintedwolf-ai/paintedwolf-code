import { beforeEach, describe, expect, it, vi } from "vitest";
import { stubClient } from "../../test/client-fixture.ts";
import type { SourceWalkResponse } from "../../api/types.ts";
import { invalidateWalkLoads, loadWalk, resetWalkLoadsForTests, setWalkLoadSubscription } from "./walk-loader.ts";

const empty = (): SourceWalkResponse => ({ baseline: "session:s1", files: [], git_changes: [], commands: [], turns: [], commit_available: false, next_cursor: undefined });
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>(accept => { resolve = accept; });
  return { promise, resolve };
}

beforeEach(() => { resetWalkLoadsForTests(); setWalkLoadSubscription("p"); });
describe("complete Walk acquisition", () => {
  it("shares every page and only caches the completed walk", async () => {
    const second = deferred<SourceWalkResponse>();
    const listProjectSourceWalk = vi.fn().mockResolvedValueOnce({ ...empty(), next_cursor: "cursor-4" }).mockReturnValueOnce(second.promise);
    const client = stubClient({ listProjectSourceWalk });
    const first = loadWalk(client, "p", "s");
    const duplicate = loadWalk(client, "p", "s");
    expect(duplicate).toBe(first);
    let settled = false;
    void first.then(() => { settled = true; });
    await vi.waitFor(() => expect(listProjectSourceWalk).toHaveBeenCalledTimes(2));
    expect(settled).toBe(false);
    second.resolve(empty());
    const walk = await first;
    expect(await loadWalk(client, "p", "s")).toBe(walk);
    expect(listProjectSourceWalk).toHaveBeenCalledTimes(2);
  });
  it("does not retain responses overtaken by invalidation", async () => {
    const old = deferred<SourceWalkResponse>();
    const listProjectSourceWalk = vi.fn().mockReturnValueOnce(old.promise).mockResolvedValue(empty());
    const client = stubClient({ listProjectSourceWalk });
    const stale = loadWalk(client, "p", "s");
    invalidateWalkLoads("p");
    const fresh = await loadWalk(client, "p", "s");
    old.resolve(empty());
    expect(await stale).not.toBe(fresh);
    expect(await loadWalk(client, "p", "s")).toBe(fresh);
    expect(listProjectSourceWalk).toHaveBeenCalledTimes(2);
  });
  it("isolates connections and sessions and bounds retained snapshots", async () => {
    const listProjectSourceWalk = vi.fn().mockResolvedValue(empty());
    const client = stubClient({ listProjectSourceWalk });
    await loadWalk(client, "p", "s");
    await loadWalk(stubClient({ listProjectSourceWalk }), "p", "s");
    for (let index = 0; index < 16; index++) await loadWalk(client, "p", `other-${index}`);
    await loadWalk(client, "p", "s");
    expect(listProjectSourceWalk).toHaveBeenCalledTimes(19);
  });
  it("does not reuse settled snapshots while source events are disconnected", async () => {
    const listProjectSourceWalk = vi.fn().mockResolvedValue(empty());
    const client = stubClient({ listProjectSourceWalk });
    await loadWalk(client, "p", "s");
    setWalkLoadSubscription(null);
    await loadWalk(client, "p", "s");
    await loadWalk(client, "p", "s");
    expect(listProjectSourceWalk).toHaveBeenCalledTimes(3);
  });
  it("does not cache failures", async () => {
    const listProjectSourceWalk = vi.fn().mockRejectedValueOnce(new Error("offline")).mockResolvedValue(empty());
    const client = stubClient({ listProjectSourceWalk });
    await expect(loadWalk(client, "p", "s")).rejects.toThrow("offline");
    await loadWalk(client, "p", "s");
    expect(listProjectSourceWalk).toHaveBeenCalledTimes(2);
  });
});

it("loads commit-only pages into the invoking chapter and links their tool calls", async () => {
  const movement = (id: string, ordinal: number) => ({
    id, ordinal, root_id: "root", kind: "commit" as const, observed_at: "2026-09-18T04:20:00Z",
    session_id: "s", turn: 1, tool_call_id: `call-${id}`, tool_name: "git_commit",
  });
  const turn = {session_id: "s", turn: 1, message_id: "prompt", prompt: "Check in logical groups", observed_at: "2026-09-18T04:15:00Z"};
  const listProjectSourceWalk = vi.fn()
    .mockResolvedValueOnce({...empty(), git_changes: [movement("newer", 20)], turns: [turn], next_cursor: "cursor-20"})
    .mockResolvedValueOnce({...empty(), git_changes: [movement("older", 10)], turns: [turn]});
  const walk = await loadWalk(stubClient({listProjectSourceWalk}), "p", "s");
  expect(walk.steps.map(step => [step.key, step.toolCallId])).toEqual([["git:older", "call-older"], ["git:newer", "call-newer"]]);
  expect(walk.chapters).toHaveLength(1);
  expect(walk.chapters[0]).toMatchObject({sessionId: "s", turn: 1, messageId: "prompt", prompt: "Check in logical groups"});
  expect(listProjectSourceWalk.mock.calls[1]![1].cursor).toBe("cursor-20");
});
